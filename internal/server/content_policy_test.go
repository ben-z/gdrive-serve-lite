package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"gdrive-serve-lite/internal/drive"
)

func TestStreamedContentBlocksScriptsWithoutChangingReads(t *testing.T) {
	for _, tt := range []struct {
		name, target, body, mimeType, disposition, contentRange string
		status                                                  int
	}{
		{"blob", "/file/blob/Clip.html", "<p>Static HTML</p>", "text/html", "", "", 200},
		{"range", "/file/blob/Clip", "part", "video/mp4", `attachment; filename="segment.bin"`, "bytes 0-3/20", 206},
		{"blob error", "/file/blob/Clip", "upstream denied", "text/plain", `attachment; filename="error.txt"`, "", 403},
		{"range error", "/file/blob/Clip", "invalid range", "text/plain", "", "bytes */20", 416},
		{"export", "/export/doc/Diagram?format=svg", `<svg xmlns="http://www.w3.org/2000/svg"><circle r="10"/></svg>`, "image/svg+xml", "", "", 200},
		{"export error", "/export/doc/Diagram?format=svg", "export denied", "text/plain", "", "", 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			app := testServer(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				if req.URL.Query().Get("fields") != "" {
					return jsonResponse(http.StatusOK, `{"id":"doc","name":"Diagram","mimeType":"application/vnd.google-apps.drawing"}`), nil
				}
				if tt.status == http.StatusPartialContent && req.Header.Get("Range") != "bytes=0-3" {
					t.Error("media request lost Range header")
				}
				res := textResponse(tt.status, tt.body)
				res.Header.Set("Content-Type", tt.mimeType)
				res.Header.Set("Content-Length", strconv.Itoa(len(tt.body)))
				res.Header.Set("Content-Security-Policy", "script-src 'unsafe-inline'")
				res.Header.Set("ETag", `"upstream"`)
				res.Header.Set("Cache-Control", "private, max-age=60")
				if tt.disposition != "" {
					res.Header.Set("Content-Disposition", tt.disposition)
				}
				if tt.contentRange != "" {
					res.Header.Set("Content-Range", tt.contentRange)
				}
				return res, nil
			}, Options{BaseURL: "/prefix", AllowOrigin: "https://viewer.invalid"})
			front := httptest.NewServer(app.Handler())
			defer front.Close()
			req, err := http.NewRequest(http.MethodGet, front.URL+"/prefix"+tt.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Origin", "https://viewer.invalid")
			if tt.status == http.StatusPartialContent {
				req.Header.Set("Range", "bytes=0-3")
			}
			res, err := front.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || res.StatusCode != tt.status || string(body) != tt.body {
				t.Fatalf("response = %d/%q, error = %v", res.StatusCode, body, err)
			}
			if values := res.Header.Values("Content-Security-Policy"); len(values) != 1 || values[0] != "script-src 'none'" {
				t.Errorf("content policy = %q", values)
			}
			if res.Header.Get("Content-Length") != strconv.Itoa(len(tt.body)) {
				t.Errorf("Content-Length = %q", res.Header.Get("Content-Length"))
			}
			if res.Header.Get("Content-Type") != tt.mimeType || res.Header.Get("Content-Range") != tt.contentRange || res.Header.Get("ETag") != `"upstream"` || res.Header.Get("Cache-Control") != "private, max-age=60" || res.Header.Get("Access-Control-Allow-Origin") != "https://viewer.invalid" {
				t.Errorf("read/CORS headers changed: %v", res.Header)
			}
			disposition := tt.disposition
			if disposition == "" && tt.status == http.StatusOK {
				disposition = "inline; filename*=UTF-8''Clip.html"
				if tt.name == "export" {
					disposition = "inline; filename*=UTF-8''Diagram.svg"
				}
			}
			if res.Header.Get("Content-Disposition") != disposition {
				t.Errorf("Content-Disposition = %q; want %q", res.Header.Get("Content-Disposition"), disposition)
			}
			wantCalls := int32(1)
			if strings.HasPrefix(tt.target, "/export/") {
				wantCalls++
			}
			if calls.Load() != wantCalls {
				t.Errorf("Drive requests = %d; want %d", calls.Load(), wantCalls)
			}
		})
	}
}

func TestMetadataHeadUsesReadContentPolicy(t *testing.T) {
	const future = "Tue, 09 Jun 2026 12:34:56 GMT"
	const stale = "Sun, 07 Jun 2026 12:34:56 GMT"
	for _, tt := range []struct {
		name, target, mimeType, size, since, unmodified string
		status                                          int
	}{
		{"blob", "/file/source/Clip", "text/html", "3", "", "", 200},
		{"blob304", "/file/source/Clip", "text/html", "3", future, "", 304},
		{"blob412", "/file/source/Clip", "text/html", "3", "", stale, 412},
		{"invalid size", "/file/source/Clip", "text/html", "invalid", future, "", 500},
		{"export", "/export/source/Diagram?format=svg", "application/vnd.google-apps.drawing", "invalid", "", "", 200},
		{"export304", "/export/source/Diagram?format=svg", "application/vnd.google-apps.drawing", "", future, "", 304},
		{"shortcut blob", "/path/Alias", "text/html", "3", "", "", 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			app := testServer(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				if req.URL.Query().Get("fields") == "" {
					t.Errorf("HEAD fetched content: %s", req.URL)
				}
				file := drive.File{ID: "source", Name: "Clip", MIMEType: tt.mimeType, Size: tt.size, ModifiedTime: "2026-06-08T12:34:56Z"}
				if req.URL.Path == "/drive/v3/files/target" {
					file.ID = "target"
					if req.Header.Get("X-Goog-Drive-Resource-Keys") != "target/target-key" {
						t.Error("shortcut target lost its resource key")
					}
				} else if strings.HasPrefix(tt.name, "shortcut") {
					file = drive.File{ID: "alias", Name: "Alias", MIMEType: "application/vnd.google-apps.shortcut", ShortcutDetails: &drive.ShortcutDetails{TargetID: "target", TargetMIMEType: tt.mimeType, TargetResourceKey: "target-key"}}
				}
				var value any = file
				if req.URL.Query().Get("q") != "" {
					value = drive.FileList{Files: []drive.File{file}}
				}
				body, err := json.Marshal(value)
				if err != nil {
					t.Error(err)
				}
				return jsonResponse(http.StatusOK, string(body)), nil
			})
			front := httptest.NewServer(app.Handler())
			defer front.Close()
			req, err := http.NewRequest(http.MethodHead, front.URL+tt.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("If-Modified-Since", tt.since)
			req.Header.Set("If-Unmodified-Since", tt.unmodified)
			res, err := front.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || len(body) != 0 || res.StatusCode != tt.status {
				t.Fatalf("HEAD = %d/%q, error = %v", res.StatusCode, body, err)
			}
			policy := "script-src 'none'"
			if tt.status == http.StatusInternalServerError {
				policy = ""
				if res.Header.Get("Content-Type") != "application/json; charset=utf-8" || res.Header.Get("Last-Modified") != "" {
					t.Error("invalid size response gained file headers")
				}
			}
			if res.Header.Get("Content-Security-Policy") != policy {
				t.Errorf("HEAD policy = %q; want %q", res.Header.Get("Content-Security-Policy"), policy)
			}
			wantCalls := int32(1)
			if strings.HasPrefix(tt.name, "shortcut") {
				wantCalls++
			}
			if calls.Load() != wantCalls {
				t.Errorf("metadata requests = %d; want %d", calls.Load(), wantCalls)
			}
		})
	}
}

func TestTrustedResponsesDoNotUseFileContentPolicy(t *testing.T) {
	for _, target := range []string{"/", "/api/list?folderId=root", "/healthz"} {
		t.Run(target, func(t *testing.T) {
			app := testServer(func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, `{"files":[]}`), nil
			})
			front := httptest.NewServer(app.Handler())
			defer front.Close()
			res, err := front.Client().Get(front.URL + target)
			if err != nil {
				t.Fatal(err)
			}
			_, err = io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if err != nil || res.StatusCode != http.StatusOK || res.Header.Get("Content-Security-Policy") != "" {
				t.Fatalf("trusted response = %d, policy = %q, error = %v", res.StatusCode, res.Header.Get("Content-Security-Policy"), err)
			}
		})
	}
}
