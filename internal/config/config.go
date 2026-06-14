package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

type OAuthToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
}

type Config struct {
	ClientID        string
	ClientSecret    string
	Token           OAuthToken
	DriveID         string
	RootFolderID    string
	RootResourceKey string
	Source          string
}

// Load reads auth config from the named rclone remote, or from GDSL_* env
// vars when no remote is given.
func Load(ctx context.Context, remote, configPath string) (Config, error) {
	if remote == "" {
		if cfg, ok := loadEnv(); ok {
			return cfg, nil
		}
		return Config{}, errors.New("no auth config found: set GDSL_* env vars or pass --remote")
	}

	cfg, err := loadRcloneFile(remote, configPath, os.Getenv("RCLONE_CONFIG_PASS"))
	if err == nil {
		return cfg, nil
	}
	if errors.Is(err, errConfigFileEncrypted) || errors.Is(err, errConfigFileDecrypt) {
		return Config{}, err
	}

	dumpCfg, dumpErr := loadRcloneDump(ctx, remote)
	if dumpErr != nil {
		return Config{}, fmt.Errorf("load rclone config file: %v; rclone config dump fallback: %w", err, dumpErr)
	}
	return dumpCfg, nil
}

func (c Config) Validate() error {
	if c.Token.AccessToken == "" && c.Token.RefreshToken == "" {
		return errors.New("missing access token or refresh token")
	}
	if c.Token.RefreshToken != "" && c.ClientID == "" {
		return errors.New("refresh token is set but client ID is missing")
	}
	return nil
}

func (c Config) EffectiveRootFolderID() string {
	if c.RootFolderID != "" {
		return c.RootFolderID
	}
	if c.DriveID != "" {
		return c.DriveID
	}
	return "root"
}

func loadEnv() (Config, bool) {
	clientID := os.Getenv("GDSL_CLIENT_ID")
	accessToken := os.Getenv("GDSL_ACCESS_TOKEN")
	refreshToken := os.Getenv("GDSL_REFRESH_TOKEN")
	if clientID == "" && accessToken == "" && refreshToken == "" {
		return Config{}, false
	}
	expiry := time.Time{}
	if raw := os.Getenv("GDSL_TOKEN_EXPIRY"); raw != "" {
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			expiry = t
		}
	}
	return Config{
		ClientID:     clientID,
		ClientSecret: os.Getenv("GDSL_CLIENT_SECRET"),
		Token: OAuthToken{
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			Expiry:       expiry,
		},
		DriveID:         os.Getenv("GDSL_DRIVE_ID"),
		RootFolderID:    os.Getenv("GDSL_ROOT_FOLDER_ID"),
		RootResourceKey: os.Getenv("GDSL_ROOT_RESOURCE_KEY"),
		Source:          "env",
	}, true
}

func loadRcloneDump(ctx context.Context, remote string) (Config, error) {
	out, err := exec.CommandContext(ctx, "rclone", "config", "dump").Output()
	if err != nil {
		return Config{}, fmt.Errorf("rclone config dump: %w; if the config is encrypted, set RCLONE_CONFIG_PASS or use GDSL_* env vars", err)
	}

	var dump map[string]map[string]string
	if err := json.Unmarshal(out, &dump); err != nil {
		return Config{}, fmt.Errorf("parse rclone config dump: %w", err)
	}
	entry, ok := dump[remote]
	if !ok {
		return Config{}, fmt.Errorf("rclone remote %q not found", remote)
	}
	return configFromRcloneEntry(remote, entry)
}
