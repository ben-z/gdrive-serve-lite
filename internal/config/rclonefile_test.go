package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const fakePlainRcloneConfig = `[fake-drive]
type = drive
client_id = fake-client-id
client_secret = fake-client-secret
scope = drive.readonly
team_drive = fake-team-drive
root_folder_id = fake-root-folder
token = {"access_token":"fake-access","token_type":"Bearer","refresh_token":"fake-refresh","expiry":"2026-06-07T21:18:38-07:00"}
`

// Generated from fakePlainRcloneConfig with:
//
//	RCLONE_CONFIG_PASS=fixture-pass rclone --config tmp/plain-rclone.conf --password-command 'printf fixture-pass' config encryption set
const fakeEncryptedRcloneConfig = `# Encrypted rclone configuration File

RCLONE_ENCRYPT_V0:
eiUB+mEXO52EUeQZ7R4N2jsgBxB4v40WMWOU7BJDxulc6AiuVR3XwEFE1TaiWR1r8n0uTyxk9jwTckHSx6Rdlp2pFAuw02fKP5UjYhdILAcMtZ9ngcFIjpKt/VvWsMMNqTF8lPXVQzQr+3735MIQqythZdgY+gipWq/zPzBIsJkS7uOPegczCbOYbw95CPuMJNtuTuzRcJislXhJlQJM15zedTQn8SsUJEQyTBIcxBmp31oW5YX62NrkPyP3+C4/qQURdQmX6IPPVcobLqptw8mm0gzomJbM6GVMXv4zDogfoFuafcchHAHPXo7p7YKYMPNvWc68eCaQc9PTJkwWQIY0m9grXqdUd3vhu+MsD3GXaAtbuis87sbb5A4YsEVOBaojoma7khvor4Gb0xvOHT0CwMGGP6Kb0JbI2sbyWpaoCV7ZobjN1tlcAjSfFWBD+9fkR2szRlA=
`

func TestLoadPlainRcloneFile(t *testing.T) {
	path := writeFixture(t, "rclone.conf", fakePlainRcloneConfig)
	cfg, err := loadRcloneFile("fake-drive", path, "")
	if err != nil {
		t.Fatal(err)
	}
	assertFakeConfig(t, cfg)
	if cfg.Source != "rclone-file:"+path {
		t.Fatalf("Source = %q, want rclone-file:%s", cfg.Source, path)
	}
}

func TestLoadEncryptedRcloneFile(t *testing.T) {
	path := writeFixture(t, "rclone.conf", fakeEncryptedRcloneConfig)
	cfg, err := loadRcloneFile("fake-drive", path, "fixture-pass")
	if err != nil {
		t.Fatal(err)
	}
	assertFakeConfig(t, cfg)
	if cfg.Source != "rclone-file:"+path+":encrypted" {
		t.Fatalf("Source = %q, want encrypted rclone file source", cfg.Source)
	}
}

func TestEncryptedRcloneFileRequiresPassword(t *testing.T) {
	path := writeFixture(t, "rclone.conf", fakeEncryptedRcloneConfig)
	_, err := loadRcloneFile("fake-drive", path, "")
	if !errors.Is(err, errConfigFileEncrypted) {
		t.Fatalf("error = %v, want errConfigFileEncrypted", err)
	}
}

func TestEncryptedRcloneFileRejectsWrongPassword(t *testing.T) {
	path := writeFixture(t, "rclone.conf", fakeEncryptedRcloneConfig)
	_, err := loadRcloneFile("fake-drive", path, "wrong")
	if !errors.Is(err, errConfigFileDecrypt) {
		t.Fatalf("error = %v, want errConfigFileDecrypt", err)
	}
}

func TestRcloneConfigKeyUsesNFKCNormalization(t *testing.T) {
	a, err := rcloneConfigKey("Å")
	if err != nil {
		t.Fatal(err)
	}
	b, err := rcloneConfigKey("Å")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("expected canonically compatible passwords to produce the same config key")
	}
}

func writeFixture(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertFakeConfig(t *testing.T, cfg Config) {
	t.Helper()
	if cfg.ClientID != "fake-client-id" {
		t.Fatalf("ClientID = %q", cfg.ClientID)
	}
	if cfg.ClientSecret != "fake-client-secret" {
		t.Fatalf("ClientSecret = %q", cfg.ClientSecret)
	}
	if cfg.DriveID != "fake-team-drive" {
		t.Fatalf("DriveID = %q", cfg.DriveID)
	}
	if cfg.RootFolderID != "fake-root-folder" {
		t.Fatalf("RootFolderID = %q", cfg.RootFolderID)
	}
	if cfg.Token.AccessToken != "fake-access" {
		t.Fatalf("AccessToken = %q", cfg.Token.AccessToken)
	}
	if cfg.Token.RefreshToken != "fake-refresh" {
		t.Fatalf("RefreshToken = %q", cfg.Token.RefreshToken)
	}
}
