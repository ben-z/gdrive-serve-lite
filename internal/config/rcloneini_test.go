package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestLoadRcloneINISyntax(t *testing.T) {
	const token = `{"access_token":"fixture-access"}`
	for _, tt := range []struct {
		name, data string
		encrypted  bool
	}{
		{"UTF-8 BOM", "\ufeff[drive]\ntype = drive\ntoken = " + token, false},
		{"colon separators", "[drive]\ntype: drive\ntoken: " + token, false},
		{"triple-quoted token", "[drive]\ntype = drive\ntoken = \"\"\"" + token + "\"\"\"", false},
		{"backtick-quoted token", "[drive]\ntype = drive\ntoken = `" + token + "`", false},
		{"double-quoted duplicate key", "[drive]\ntype = drive\ntoken = {\"access_token\":\"old\"}\n\"token\" = " + token, false},
		{"backtick-quoted duplicate key", "[drive]\ntype = drive\ntoken = {\"access_token\":\"old\"}\n`token` = " + token, false},
		{"duplicate section", "[drive]\ntype = drive\ntoken = {\"access_token\":\"old\"}\n[drive]\ntoken = " + token, false},
		{"encrypted quoted syntax", "[drive]\n`type`: drive\n\"token\": \"\"\"" + token + "\"\"\"", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := tt.data
			if tt.encrypted {
				t.Setenv("RCLONE_CONFIG_PASS", "fixture-password")
				data = encryptedRcloneFixture(t, []byte(data), "fixture-password")
			}
			cfg, err := Load("drive", writeFixture(t, "rclone.conf", data))
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			if cfg.Token.AccessToken != "fixture-access" {
				t.Fatalf("access token = %q, want final selected value", cfg.Token.AccessToken)
			}
		})
	}
}

func TestLoadRcloneErrorsOmitInputValues(t *testing.T) {
	const secret = "SYNTHETIC_CONFIG_SECRET"
	t.Setenv("GDSL_ACCESS_TOKEN", "env-fallback")
	t.Setenv("RCLONE_CONFIG", writeFixture(t, "default.conf", fakePlainRcloneConfig))
	for _, tt := range []struct{ name, data, want string }{
		{"INI syntax", "[drive]\n" + secret, "invalid INI syntax"},
		{"unsupported type", "[drive]\ntype = " + secret, "unsupported type"},
		{"quoted type override", "[drive]\ntype = drive\n\"type\" = " + secret, "unsupported type"},
		{"token shape", "[drive]\ntype = drive\ntoken = " + secret, "parse rclone token"},
		{"token expiry", "[drive]\ntype = drive\ntoken = {\"access_token\":\"fixture\",\"expiry\":\"" + secret + "\"}", "parse rclone token"},
		{"encryption header", "RCLONE_ENCRYPT_V9: " + secret, "unsupported rclone config encryption header"},
		{"interpolation", "[drive]\ntype = drive\ntoken = {\"access_token\":\"%(" + secret + ")s\"}", "unsupported interpolation"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load("drive", writeFixture(t, "selected.conf", tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), secret) {
				t.Fatalf("error = %v; want safe %q diagnosis", err, tt.want)
			}
		})
	}
}

func TestLoadRcloneRejectsConsumedInterpolation(t *testing.T) {
	for _, key := range []string{"type", "client_id", "client_secret", "token", "team_drive", "root_folder_id", "resource_key"} {
		t.Run(key, func(t *testing.T) {
			data := "[drive]\ntype = drive\ntoken = {\"access_token\":\"fixture\"}\nvalue = fixture\n" + key + " = %(value)s\n"
			_, err := Load("drive", writeFixture(t, "rclone.conf", data))
			if err == nil || !strings.Contains(err.Error(), "unsupported interpolation") || !strings.Contains(err.Error(), key) {
				t.Fatalf("error = %v, want explicit interpolation error for %s", err, key)
			}
		})
	}
}

func TestLoadRcloneUsesOnlySelectedOptions(t *testing.T) {
	data := "[DEFAULT]\nclient_secret = default-secret\n" +
		"[other]\ntype = s3\ntoken = %(missing)s\nclient_secret = %(missing)s\n" +
		"[drive]\ntype = drive\ntoken = {\"access_token\":\"literal-%(unfinished\"}\nunused = %(missing)s\n"
	cfg, err := Load("drive", writeFixture(t, "rclone.conf", data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientSecret != "" || cfg.Token.AccessToken != "literal-%(unfinished" {
		t.Fatalf("selected options changed: secret=%q token=%q", cfg.ClientSecret, cfg.Token.AccessToken)
	}
}

func TestLoadRcloneRejectsInheritedConsumedOptions(t *testing.T) {
	options := []struct{ key, value string }{
		{"type", "drive"}, {"client_id", "fixture-client"}, {"client_secret", "fixture-secret"},
		{"token", `{"access_token":"fixture"}`}, {"team_drive", "fixture-drive"},
		{"root_folder_id", "fixture-root"}, {"resource_key", "fixture-key"},
	}
	for _, inherited := range options {
		t.Run(inherited.key, func(t *testing.T) {
			var data strings.Builder
			fmt.Fprintf(&data, "[parent]\n%s = %s\n[parent.child]\n", inherited.key, inherited.value)
			for _, local := range options {
				if local.key != inherited.key {
					fmt.Fprintf(&data, "%s = %s\n", local.key, local.value)
				}
			}
			_, err := Load("parent.child", writeFixture(t, "rclone.conf", data.String()))
			if err == nil || !strings.Contains(err.Error(), "inherits option") || !strings.Contains(err.Error(), inherited.key) {
				t.Fatalf("error = %v, want explicit inheritance error for %s", err, inherited.key)
			}
		})
	}
}

func TestLoadRcloneDottedSectionBoundaries(t *testing.T) {
	const local = "type = drive\ntoken = {\"access_token\":\"fixture\"}\n"
	for _, tt := range []struct {
		name, remote, data, wantSecret string
		wantError                      bool
	}{
		{"local override", "parent.child", "[parent]\nclient_secret = parent-secret\n[parent.child]\n" + local + "client_secret = local-secret\n", "local-secret", false},
		{"empty local override", "parent.child", "[parent]\nclient_secret = parent-secret\n[parent.child]\n" + local + "client_secret =\n", "", false},
		{"missing parent stops lookup", "grand.missing.child", "[grand]\nclient_secret = parent-secret\n[grand.missing.child]\n" + local, "", false},
		{"empty parent reaches grandparent", "grand.parent.child", "[grand]\nclient_secret = parent-secret\n[grand.parent]\n[grand.parent.child]\n" + local, "", true},
		{"empty prefix reaches DEFAULT", ".child", "[DEFAULT]\nclient_secret = default-secret\n[.child]\n" + local, "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(tt.remote, writeFixture(t, "rclone.conf", tt.data))
			if tt.wantError {
				if err == nil || !strings.Contains(err.Error(), "inherits option") {
					t.Fatalf("error = %v, want inherited option error", err)
				}
				return
			}
			if err != nil || cfg.ClientSecret != tt.wantSecret {
				t.Fatalf("error = %v, secret = %q, want %q", err, cfg.ClientSecret, tt.wantSecret)
			}
		})
	}
}
