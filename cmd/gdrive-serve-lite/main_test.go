package main

import (
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
)

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
