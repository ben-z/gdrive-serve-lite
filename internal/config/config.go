package config

import (
	"errors"
	"fmt"
	"os"
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
func Load(remote, configPath string) (Config, error) {
	if remote != "" {
		return loadRcloneFile(remote, configPath, os.Getenv("RCLONE_CONFIG_PASS"))
	}
	if configPath != "" {
		return Config{}, errors.New("--config requires --remote")
	}
	return loadEnv()
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

func loadEnv() (Config, error) {
	clientID := os.Getenv("GDSL_CLIENT_ID")
	accessToken := os.Getenv("GDSL_ACCESS_TOKEN")
	refreshToken := os.Getenv("GDSL_REFRESH_TOKEN")
	if clientID == "" && accessToken == "" && refreshToken == "" {
		return Config{}, errors.New("no auth config found: set GDSL_* env vars or pass --remote")
	}
	expiry := time.Time{}
	if raw := os.Getenv("GDSL_TOKEN_EXPIRY"); raw != "" {
		var err error
		expiry, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse GDSL_TOKEN_EXPIRY: %w", err)
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
	}, nil
}
