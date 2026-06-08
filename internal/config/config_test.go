package config

import "testing"

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
