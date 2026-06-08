package config

import (
	"context"
	"errors"
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
	t.Setenv("GDSL_ROOT_RESOURCE_KEY", "")

	cfg, err := Load(context.Background(), LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source != "env" {
		t.Fatalf("Source = %q, want env", cfg.Source)
	}
	if cfg.ClientID != "env-client" || cfg.Token.AccessToken != "env-access" || cfg.RootFolderID != "env-root" {
		t.Fatalf("cfg = %+v", cfg)
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

func TestLoadEnvReadsRootResourceKey(t *testing.T) {
	t.Setenv("GDSL_CLIENT_ID", "")
	t.Setenv("GDSL_CLIENT_SECRET", "")
	t.Setenv("GDSL_ACCESS_TOKEN", "test-access")
	t.Setenv("GDSL_REFRESH_TOKEN", "")
	t.Setenv("GDSL_TOKEN_EXPIRY", "")
	t.Setenv("GDSL_DRIVE_ID", "")
	t.Setenv("GDSL_ROOT_FOLDER_ID", "root-folder")
	t.Setenv("GDSL_ROOT_RESOURCE_KEY", "root-key-1")

	cfg, ok := loadEnv()
	if !ok {
		t.Fatal("loadEnv() returned ok=false")
	}
	if cfg.RootFolderID != "root-folder" {
		t.Fatalf("RootFolderID = %q", cfg.RootFolderID)
	}
	if cfg.RootResourceKey != "root-key-1" {
		t.Fatalf("RootResourceKey = %q", cfg.RootResourceKey)
	}
}
