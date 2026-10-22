package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	cfg, err := Load("", "")
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
		{"expired access token only", Config{Token: OAuthToken{AccessToken: "a", Expiry: time.Now().Add(-time.Hour)}}, "access token is expired"},
		{"valid access token only", Config{Token: OAuthToken{AccessToken: "a", Expiry: time.Now().Add(time.Hour)}}, ""},
		{"refresh with client id", Config{ClientID: "c", Token: OAuthToken{RefreshToken: "r"}}, ""},
		{"expired refreshable token", Config{ClientID: "c", Token: OAuthToken{AccessToken: "a", RefreshToken: "r", Expiry: time.Now().Add(-time.Hour)}}, ""},
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

	cfg, err := Load("fake-drive", path)
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

	_, err := Load("fake-drive", path)
	if !errors.Is(err, errConfigFileEncrypted) {
		t.Fatalf("error = %v, want errConfigFileEncrypted", err)
	}
}

func TestLoadRejectsInvalidTokenExpiry(t *testing.T) {
	t.Setenv("GDSL_ACCESS_TOKEN", "env-access")
	t.Setenv("GDSL_TOKEN_EXPIRY", "next Thursday")
	_, err := Load("", "")
	if err == nil || !strings.Contains(err.Error(), "parse GDSL_TOKEN_EXPIRY") {
		t.Fatalf("Load() error = %v, want invalid expiry", err)
	}
}

func TestLoadPreservesTokenExpiry(t *testing.T) {
	t.Setenv("GDSL_ACCESS_TOKEN", "env-access")
	t.Setenv("GDSL_TOKEN_EXPIRY", "2026-06-08T12:34:56.123456789-07:00")
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Token.Expiry.Format(time.RFC3339Nano); got != "2026-06-08T12:34:56.123456789-07:00" {
		t.Fatalf("expiry = %q", got)
	}
}

func TestLoadConfigFileRequiresRemote(t *testing.T) {
	t.Setenv("GDSL_ACCESS_TOKEN", "env-access")
	_, err := Load("", "selected.conf")
	if err == nil || !strings.Contains(err.Error(), "--config requires --remote") {
		t.Fatalf("Load() error = %v, want missing remote", err)
	}
}

func TestLoadSelectedConfigErrors(t *testing.T) {
	t.Setenv("GDSL_ACCESS_TOKEN", "env-access")
	// A valid default file must not replace an explicitly selected broken file.
	t.Setenv("RCLONE_CONFIG", writeFixture(t, "default.conf", fakePlainRcloneConfig))
	for _, tt := range []struct {
		name, contents, want string
	}{
		{"malformed INI", "invalid", "key outside section"},
		{"missing remote", "[other]\ntype = drive\n", `remote "fake-drive" not found`},
		{"unsupported remote", "[fake-drive]\ntype = s3\n", `unsupported type "s3"`},
		{"malformed token", "[fake-drive]\ntype = drive\ntoken = invalid\n", "parse rclone token"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFixture(t, "selected.conf", tt.contents)
			_, err := Load("fake-drive", path)
			if err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), "config dump") {
				t.Fatalf("Load() error = %v, want selected config error %q", err, tt.want)
			}
		})
	}
	_, err := Load("fake-drive", filepath.Join(t.TempDir(), "missing.conf"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load() error = %v, want missing selected file", err)
	}
}
