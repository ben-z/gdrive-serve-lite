package main

import "testing"

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
