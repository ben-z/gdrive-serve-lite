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
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/text/unicode/norm"
)

const rcloneEncryptedHeader = "RCLONE_ENCRYPT_V0:"

var (
	errConfigFileEncrypted = errors.New("rclone config file is encrypted")
	errConfigFileDecrypt   = errors.New("failed to decrypt rclone config file")
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
	sections, err := parseRcloneINI(plain)
	if err != nil {
		return Config{}, err
	}
	entry, ok := sections[remote]
	if !ok {
		return Config{}, fmt.Errorf("rclone remote %q not found in %s", remote, path)
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
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		path := filepath.Join(dir, "rclone", "rclone.conf")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		legacy := filepath.Join(home, ".rclone.conf")
		if _, err := os.Stat(legacy); err == nil {
			return legacy, nil
		}
		return filepath.Join(home, ".config", "rclone", "rclone.conf"), nil
	}
	return "", errors.New("could not determine rclone config path")
}

func configFromRcloneEntry(remote string, entry map[string]string) (Config, error) {
	var token OAuthToken
	if raw := entry["token"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &token); err != nil {
			return Config{}, fmt.Errorf("parse rclone token for %q: %w", remote, err)
		}
	}
	return Config{
		ClientID:     entry["client_id"],
		ClientSecret: entry["client_secret"],
		Token:        token,
		DriveID:      entry["team_drive"],
		RootFolderID: entry["root_folder_id"],
		Source:       "rclone:" + remote,
	}, nil
}

func decryptRcloneConfig(data []byte, password string) ([]byte, bool, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
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
				return nil, false, fmt.Errorf("%w: unsupported rclone config encryption header %q", errConfigFileDecrypt, line)
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
	trimmed := strings.TrimSpace(password)
	password = norm.NFKC.String(password)
	if len(password) == 0 || len(trimmed) == 0 {
		return [32]byte{}, errors.New("no characters in password")
	}
	return sha256.Sum256([]byte("[" + password + "][rclone-config]")), nil
}

func parseRcloneINI(data []byte) (map[string]map[string]string, error) {
	sections := map[string]map[string]string{}
	var current string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = strings.TrimSpace(line[1 : len(line)-1])
			if current == "" {
				return nil, fmt.Errorf("empty section name at line %d", lineNo)
			}
			if _, ok := sections[current]; !ok {
				sections[current] = map[string]string{}
			}
			continue
		}
		if current == "" {
			return nil, fmt.Errorf("key outside section at line %d", lineNo)
		}
		key, value, ok := strings.Cut(raw, "=")
		if !ok {
			return nil, fmt.Errorf("invalid config line %d", lineNo)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("empty key at line %d", lineNo)
		}
		sections[current][key] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return sections, nil
}
