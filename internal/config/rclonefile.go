package config

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/unknwon/goconfig"
	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/text/unicode/norm"
)

const rcloneEncryptedHeader = "RCLONE_ENCRYPT_V0:"

var (
	errConfigFileEncrypted = errors.New("rclone config file is encrypted")
	errConfigFileDecrypt   = errors.New("failed to decrypt rclone config file")
	rcloneInterpolation    = regexp.MustCompile(`%\([^)]+\)s`)
)

func loadRcloneFile(remote, configPath, password string) (Config, error) {
	path, err := resolveRcloneConfigPath(configPath)
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	plain, encrypted, err := decryptRcloneConfig(data, password)
	if err != nil {
		return Config{}, err
	}
	sections, err := goconfig.LoadFromReader(bytes.NewReader(plain))
	if err != nil {
		// Upstream parse errors include the input line, which may contain credentials.
		return Config{}, fmt.Errorf("parse rclone config %s: invalid INI syntax", path)
	}
	entry, err := sections.GetSection(remote)
	if err != nil {
		return Config{}, fmt.Errorf("rclone remote %q not found in %s", remote, path)
	}
	if err := checkRcloneOptions(sections, remote, entry); err != nil {
		return Config{}, err
	}
	cfg, err := configFromRcloneEntry(remote, entry)
	if err != nil {
		return Config{}, err
	}
	source := "rclone-file:" + path
	if encrypted {
		source += ":encrypted"
	}
	cfg.Source = source
	return cfg, nil
}

func resolveRcloneConfigPath(configPath string) (string, error) {
	if configPath == "" {
		configPath = os.Getenv("RCLONE_CONFIG")
	}
	if configPath != "" {
		return filepath.Abs(configPath)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	path := filepath.Join(dir, "rclone", "rclone.conf")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect rclone config %s: %w", path, err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	legacy := filepath.Join(home, ".rclone.conf")
	if _, err := os.Stat(legacy); err == nil {
		return legacy, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect rclone config %s: %w", legacy, err)
	}
	return filepath.Join(home, ".config", "rclone", "rclone.conf"), nil
}

func configFromRcloneEntry(remote string, entry map[string]string) (Config, error) {
	if remoteType := strings.TrimSpace(entry["type"]); !strings.EqualFold(remoteType, "drive") {
		if remoteType == "" {
			return Config{}, fmt.Errorf("rclone remote %q is missing type", remote)
		}
		return Config{}, fmt.Errorf("rclone remote %q has unsupported type; only Google Drive remotes are supported", remote)
	}
	var token OAuthToken
	if raw := entry["token"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &token); err != nil {
			return Config{}, fmt.Errorf("parse rclone token for %q: invalid token JSON or expiry", remote)
		}
	}
	return Config{
		ClientID:        entry["client_id"],
		ClientSecret:    entry["client_secret"],
		Token:           token,
		DriveID:         entry["team_drive"],
		RootFolderID:    entry["root_folder_id"],
		RootResourceKey: entry["resource_key"],
		Source:          "rclone:" + remote,
	}, nil
}

func decryptRcloneConfig(data []byte, password string) ([]byte, bool, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), len(data)+1)
	var encoded []string
	foundHeader := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !foundHeader {
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			if line == rcloneEncryptedHeader {
				foundHeader = true
				continue
			}
			if strings.HasPrefix(line, "RCLONE_ENCRYPT_V") {
				return nil, false, fmt.Errorf("%w: unsupported rclone config encryption header", errConfigFileDecrypt)
			}
			return data, false, nil
		}
		encoded = append(encoded, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, foundHeader, err
	}
	if !foundHeader {
		return data, false, nil
	}
	if password == "" {
		return nil, true, fmt.Errorf("%w: set RCLONE_CONFIG_PASS or use GDSL_* env vars", errConfigFileEncrypted)
	}

	box, err := base64.StdEncoding.DecodeString(strings.Join(encoded, ""))
	if err != nil {
		return nil, true, fmt.Errorf("%w: bad base64 data: %v", errConfigFileDecrypt, err)
	}
	if len(box) < 24+secretbox.Overhead {
		return nil, true, fmt.Errorf("%w: encrypted data too short", errConfigFileDecrypt)
	}
	key, err := rcloneConfigKey(password)
	if err != nil {
		return nil, true, fmt.Errorf("%w: %v", errConfigFileDecrypt, err)
	}
	var nonce [24]byte
	copy(nonce[:], box[:24])
	out, ok := secretbox.Open(nil, box[24:], &nonce, &key)
	if !ok {
		return nil, true, fmt.Errorf("%w: incorrect password or corrupt config", errConfigFileDecrypt)
	}
	return out, true, nil
}

func rcloneConfigKey(password string) ([32]byte, error) {
	if !utf8.ValidString(password) {
		return [32]byte{}, errors.New("password contains invalid utf8 characters")
	}
	if strings.TrimSpace(password) == "" {
		return [32]byte{}, errors.New("no characters in password")
	}
	password = norm.NFKC.String(password)
	return sha256.Sum256([]byte("[" + password + "][rclone-config]")), nil
}

// Read raw options so interpolation and dotted parent inheritance cannot
// silently change the selected remote's credentials or Drive root.
func checkRcloneOptions(sections *goconfig.ConfigFile, remote string, entry map[string]string) error {
	options := []string{"type", "client_id", "client_secret", "token", "team_drive", "root_folder_id", "resource_key"}
	for _, key := range options {
		if rcloneInterpolation.MatchString(entry[key]) {
			return fmt.Errorf("rclone remote %q uses unsupported interpolation in option %q", remote, key)
		}
	}
	for parent := remote; strings.Contains(parent, "."); {
		parent = parent[:strings.LastIndex(parent, ".")]
		ancestor, err := sections.GetSection(parent)
		if err != nil {
			// Rclone also stops inheritance at the first missing parent section.
			break
		}
		for _, key := range options {
			if _, local := entry[key]; local {
				continue
			}
			if _, inherited := ancestor[key]; inherited {
				return fmt.Errorf("rclone remote %q inherits option %q; define it directly in that remote", remote, key)
			}
		}
	}
	return nil
}
