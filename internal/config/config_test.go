package config

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestLoadUsesEnvWhenRemoteOmitted(t *testing.T) {
	t.Setenv("GDSL_CLIENT_ID", "env-client")
	t.Setenv("GDSL_CLIENT_SECRET", "")
	t.Setenv("GDSL_ACCESS_TOKEN", "env-access")
	t.Setenv("GDSL_REFRESH_TOKEN", "")
	t.Setenv("GDSL_TOKEN_EXPIRY", "")
	t.Setenv("GDSL_DRIVE_ID", "")
	t.Setenv("GDSL_ROOT_FOLDER_ID", "env-root")
	t.Setenv("GDSL_ROOT_RESOURCE_KEY", "env-root-key")

	cfg, err := Load(context.Background(), LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source != "env" {
		t.Fatalf("Source = %q, want env", cfg.Source)
	}
	if cfg.ClientID != "env-client" || cfg.Token.AccessToken != "env-access" ||
		cfg.RootFolderID != "env-root" || cfg.RootResourceKey != "env-root-key" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"no tokens", Config{}, "missing access token or refresh token"},
		{"refresh without client id", Config{Token: OAuthToken{RefreshToken: "r"}}, "client ID is missing"},
		{"access token only", Config{Token: OAuthToken{AccessToken: "a"}}, ""},
		{"refresh with client id", Config{ClientID: "c", Token: OAuthToken{RefreshToken: "r"}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestEffectiveRootFolderID(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"root folder wins", Config{RootFolderID: "folder-1", DriveID: "drive-1"}, "folder-1"},
		{"drive id fallback", Config{DriveID: "drive-1"}, "drive-1"},
		{"root default", Config{}, "root"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.EffectiveRootFolderID(); got != tt.want {
				t.Fatalf("EffectiveRootFolderID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadPrefersExplicitRemoteOverEnv(t *testing.T) {
	t.Setenv("GDSL_CLIENT_ID", "env-client")
	t.Setenv("GDSL_ACCESS_TOKEN", "env-access")
	path := writeFixture(t, "rclone.conf", fakePlainRcloneConfig)

	cfg, err := Load(context.Background(), LoadOptions{Remote: "fake-drive", ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	assertFakeConfig(t, cfg)
	if cfg.Source != "rclone-file:"+path {
		t.Fatalf("Source = %q, want rclone file source", cfg.Source)
	}
}

func TestLoadExplicitEncryptedRemoteDoesNotFallBackToEnv(t *testing.T) {
	t.Setenv("GDSL_CLIENT_ID", "env-client")
	t.Setenv("GDSL_ACCESS_TOKEN", "env-access")
	t.Setenv("RCLONE_CONFIG_PASS", "")
	path := writeFixture(t, "rclone.conf", fakeEncryptedRcloneConfig)

	_, err := Load(context.Background(), LoadOptions{Remote: "fake-drive", ConfigPath: path})
	if !errors.Is(err, errConfigFileEncrypted) {
		t.Fatalf("error = %v, want errConfigFileEncrypted", err)
	}
}
