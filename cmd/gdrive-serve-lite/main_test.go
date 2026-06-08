package main

import (
	"net/http"
	"testing"
	"time"
)

func TestSplitRemotePath(t *testing.T) {
	tests := []struct {
		in       string
		remote   string
		rootPath string
	}{
		{"gdrive-ro", "gdrive-ro", ""},
		{"gdrive-ro:", "gdrive-ro", ""},
		{"gdrive-ro:/teslacam1/RecentClips", "gdrive-ro", "/teslacam1/RecentClips"},
		{"gdrive-ro:teslacam1/RecentClips", "gdrive-ro", "/teslacam1/RecentClips"},
		{" gdrive-ro:/teslacam1//RecentClips/ ", "gdrive-ro", "/teslacam1/RecentClips"},
	}
	for _, tt := range tests {
		remote, rootPath := splitRemotePath(tt.in)
		if remote != tt.remote || rootPath != tt.rootPath {
			t.Fatalf("splitRemotePath(%q) = %q, %q; want %q, %q", tt.in, remote, rootPath, tt.remote, tt.rootPath)
		}
	}
}

func TestCleanRootPath(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"/", "/"},
		{"folder", "/folder"},
		{"/folder//child/", "/folder/child"},
	}
	for _, tt := range tests {
		if got := cleanRootPath(tt.in); got != tt.want {
			t.Fatalf("cleanRootPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDriveHTTPClientSetsResponseHeaderTimeout(t *testing.T) {
	client := driveHTTPClient(7 * time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout != 7*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %s", transport.ResponseHeaderTimeout)
	}
}
