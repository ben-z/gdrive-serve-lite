package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type OAuthToken struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
}

type Config struct {
	ClientID     string
	ClientSecret string
	Token        OAuthToken
	DriveID      string
	RootFolderID string
	Source       string
}

func Load(ctx context.Context, remote string) (Config, error) {
	if cfg, ok := loadEnv(); ok {
		return cfg, nil
	}
	if remote == "" {
		return Config{}, errors.New("no auth config found: set GDSL_* env vars or pass --remote")
	}
	return loadRclone(ctx, remote)
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
			TokenType:    "Bearer",
			RefreshToken: refreshToken,
			Expiry:       expiry,
		},
		DriveID:      os.Getenv("GDSL_DRIVE_ID"),
		RootFolderID: os.Getenv("GDSL_ROOT_FOLDER_ID"),
		Source:       "env",
	}, true
}

func loadRclone(ctx context.Context, remote string) (Config, error) {
	remote = strings.TrimSuffix(remote, ":")
	cmd := exec.CommandContext(ctx, "rclone", "config", "dump")
	out, err := cmd.Output()
	if err != nil {
		return Config{}, errors.New("rclone config dump failed; if the config is encrypted, set RCLONE_CONFIG_PASS or use GDSL_* env vars")
	}

	var dump map[string]map[string]string
	if err := json.Unmarshal(out, &dump); err != nil {
		return Config{}, fmt.Errorf("parse rclone config dump: %w", err)
	}
	entry, ok := dump[remote]
	if !ok {
		return Config{}, fmt.Errorf("rclone remote %q not found", remote)
	}

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
