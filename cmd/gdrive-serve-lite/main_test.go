package main

import (
	"context"
	"flag"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gdrive-serve-lite/internal/config"
)

func TestRootOverridesKeepResourceKeysWithTheirFolder(t *testing.T) {
	for _, tt := range []struct {
		name              string
		cfg               config.Config
		folderID          string
		driveID           string
		resourceKey       string
		wantRoot, wantKey string
	}{
		{
			name:     "configured folder unchanged",
			cfg:      config.Config{RootFolderID: "folder-a", RootResourceKey: "key-a"},
			wantRoot: "folder-a", wantKey: "key-a",
		},
		{
			name:     "same folder override keeps key",
			cfg:      config.Config{RootFolderID: "folder-a", RootResourceKey: "key-a"},
			folderID: "folder-a", wantRoot: "folder-a", wantKey: "key-a",
		},
		{
			name:     "new folder clears old key",
			cfg:      config.Config{RootFolderID: "folder-a", RootResourceKey: "key-a"},
			folderID: "folder-b", wantRoot: "folder-b",
		},
		{
			name:     "new folder uses explicit key",
			cfg:      config.Config{RootFolderID: "folder-a", RootResourceKey: "key-a"},
			folderID: "folder-b", resourceKey: "key-b", wantRoot: "folder-b", wantKey: "key-b",
		},
		{
			name:    "same drive root keeps key",
			cfg:     config.Config{DriveID: "drive-a", RootResourceKey: "key-a"},
			driveID: "drive-a", wantRoot: "drive-a", wantKey: "key-a",
		},
		{
			name:    "new implicit drive root clears key",
			cfg:     config.Config{DriveID: "drive-a", RootResourceKey: "key-a"},
			driveID: "drive-b", wantRoot: "drive-b",
		},
		{
			name:    "new implicit drive root uses explicit key",
			cfg:     config.Config{DriveID: "drive-a", RootResourceKey: "key-a"},
			driveID: "drive-b", resourceKey: "key-b", wantRoot: "drive-b", wantKey: "key-b",
		},
		{
			name:    "drive override preserves explicit folder key",
			cfg:     config.Config{DriveID: "drive-a", RootFolderID: "folder-a", RootResourceKey: "key-a"},
			driveID: "drive-b", wantRoot: "folder-a", wantKey: "key-a",
		},
		{
			name:     "explicit root equal to old implicit root keeps key",
			cfg:      config.Config{DriveID: "drive-a", RootResourceKey: "key-a"},
			folderID: "drive-a", driveID: "drive-b", wantRoot: "drive-a", wantKey: "key-a",
		},
		{
			name:        "explicit key replaces same root key",
			cfg:         config.Config{RootFolderID: "folder-a", RootResourceKey: "old-key"},
			resourceKey: "new-key", wantRoot: "folder-a", wantKey: "new-key",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := applyRootOverrides(tt.cfg, tt.folderID, tt.driveID, tt.resourceKey)
			if got.EffectiveRootFolderID() != tt.wantRoot || got.RootResourceKey != tt.wantKey {
				t.Fatalf("root = %q, key = %q; want root %q, key %q", got.EffectiveRootFolderID(), got.RootResourceKey, tt.wantRoot, tt.wantKey)
			}
			if tt.driveID != "" && got.DriveID != tt.driveID {
				t.Fatalf("DriveID = %q, want %q", got.DriveID, tt.driveID)
			}
		})
	}
}

func TestSplitRemotePath(t *testing.T) {
	tests := []struct {
		in       string
		remote   string
		rootPath string
	}{
		{"gdrive-ro", "gdrive-ro", ""},
		{"gdrive-ro:", "gdrive-ro", ""},
		{"gdrive-ro:/", "gdrive-ro", "/"},
		{"gdrive-ro:folder", "gdrive-ro", "/folder"},
		{"gdrive-ro:/folder//child/", "gdrive-ro", "/folder/child"},
		{"gdrive-ro:/Videos/Clips", "gdrive-ro", "/Videos/Clips"},
		{"gdrive-ro:Videos/Clips", "gdrive-ro", "/Videos/Clips"},
		{" gdrive-ro:/Videos//Clips/ ", "gdrive-ro", "/Videos/Clips"},
	}
	for _, tt := range tests {
		remote, rootPath := splitRemotePath(tt.in)
		if remote != tt.remote || rootPath != tt.rootPath {
			t.Fatalf("splitRemotePath(%q) = %q, %q; want %q, %q", tt.in, remote, rootPath, tt.remote, tt.rootPath)
		}
	}
}

func TestStartupRejectsInvalidOptions(t *testing.T) {
	if os.Getenv("GDSL_TEST_MAIN") == "1" {
		// The child runs the actual entry point so log.Fatal and flag parsing
		// have their normal process behavior without exiting the test runner.
		os.Args = append([]string{os.Args[0]}, os.Args[3:]...)
		flag.CommandLine = flag.NewFlagSet("gdrive-serve-lite", flag.ExitOnError)
		main()
		return
	}
	for _, name := range []string{"GDSL_CLIENT_ID", "GDSL_ACCESS_TOKEN", "GDSL_REFRESH_TOKEN"} {
		t.Setenv(name, "")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"metadata TTL", []string{"--metadata-cache-ttl=-1s"}, "--metadata-cache-ttl must be non-negative"},
		{"list TTL", []string{"--list-cache-ttl=-1s"}, "--list-cache-ttl must be non-negative"},
		{"response timeout", []string{"--drive-response-header-timeout=-1s"}, "--drive-response-header-timeout must be non-negative"},
		{"empty remote", []string{"--remote=:/Videos"}, "--remote requires a remote name"},
		{"config without remote", []string{"--config=selected.conf"}, "--config requires --remote"},
		{"partial auth", []string{"--user=ben"}, "--user and --pass must be set together"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"-test.run=^TestStartupRejectsInvalidOptions$", "--"}, tt.args...)
			command := exec.Command(executable, args...)
			command.Env = append(os.Environ(), "GDSL_TEST_MAIN=1")
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), tt.want) {
				t.Fatalf("startup error = %v, output = %s; want %q", err, output, tt.want)
			}
		})
	}
}

func TestStartupResolvesLiteralRootPaths(t *testing.T) {
	const reachedDrive = "literal root path reached Drive"
	if expected := os.Getenv("GDSL_TEST_LITERAL_ROOT"); expected != "" {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			query := req.URL.Query().Get("q")
			if !strings.Contains(query, "name = '"+expected+"'") {
				http.Error(w, "unexpected name query: "+query, http.StatusBadRequest)
				return
			}
			// Stop startup after the real entry point has sent the correct lookup.
			http.Error(w, reachedDrive, http.StatusTeapot)
		}))
		defer upstream.Close()
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.DialTLSContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
		}
		http.DefaultTransport = transport
		os.Args = append([]string{os.Args[0]}, os.Args[3:]...)
		flag.CommandLine = flag.NewFlagSet("gdrive-serve-lite", flag.ExitOnError)
		main()
		return
	}

	for _, name := range []string{"GDSL_CLIENT_ID", "GDSL_CLIENT_SECRET", "GDSL_REFRESH_TOKEN", "GDSL_TOKEN_EXPIRY", "GDSL_DRIVE_ID", "GDSL_ROOT_RESOURCE_KEY"} {
		t.Setenv(name, "")
	}
	t.Setenv("GDSL_ACCESS_TOKEN", "fixture-access")
	t.Setenv("GDSL_ROOT_FOLDER_ID", "root")
	configPath := filepath.Join(t.TempDir(), "rclone.conf")
	if err := os.WriteFile(configPath, []byte("[fixture]\ntype = drive\ntoken = {\"access_token\":\"fixture-access\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"50% off", "literal%2Fname", "literal%252Fname", "question?and#fragment", "日本語", "name with spaces"} {
		for _, source := range []string{"remote", "root path", "root path overrides remote"} {
			t.Run(source+"/"+name, func(t *testing.T) {
				// An invalid listen address also makes a skipped root lookup exit.
				args := []string{"-test.run=^TestStartupResolvesLiteralRootPaths$", "--", "--addr=invalid"}
				switch source {
				case "remote":
					args = append(args, "--config="+configPath, "--remote=fixture:/"+name)
				case "root path":
					args = append(args, "--root-path=/"+name)
				case "root path overrides remote":
					args = append(args, "--config="+configPath, "--remote=fixture:/ignored", "--root-path=/"+name)
				}
				command := exec.Command(executable, args...)
				command.Env = append(os.Environ(), "GDSL_TEST_LITERAL_ROOT="+name)
				output, err := command.CombinedOutput()
				if err == nil || !strings.Contains(string(output), reachedDrive) {
					t.Fatalf("startup error = %v, output = %s; want literal name lookup", err, output)
				}
			})
		}
	}
}
