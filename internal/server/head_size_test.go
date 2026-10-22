package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gdrive-serve-lite/internal/drive"
)

func TestFileHeadValidatesSizeBeforeWritingHeaders(t *testing.T) {
	const canary = "SYNTHETIC_SIZE_SECRET"
	const future = "Tue, 09 Jun 2026 12:34:56 GMT"
	for _, tt := range []struct {
		name, size, target, mimeType, since, unmodified string
		status                                          int
	}{
		{"text", "abc", "/file/source/Clip", "video/mp4", "", "", 500},
		{"negative", "-1", "/file/source/Clip", "video/mp4", "", "", 500},
		{"sign", "+1", "/file/source/Clip", "video/mp4", "", "", 500},
		{"overflow", "9223372036854775808", "/file/source/Clip", "video/mp4", "", "", 500},
		{"sensitive", canary, "/file/source/Clip", "video/mp4", "", "", 500},
		{"newline", "1\r\n" + canary, "/file/source/Clip", "video/mp4", "", "", 500},
		{"invalid before 304", "abc", "/file/source/Clip", "video/mp4", future, "", 500},
		{"invalid before 412", "abc", "/file/source/Clip", "video/mp4", "", "Sun, 07 Jun 2026 12:34:56 GMT", 500},
		{"path", "abc", "/path/Clip", "video/mp4", "", "", 500},
		{"root path", "abc", "/Clip", "video/mp4", "", "", 500},
		{"shortcut target", "abc", "/path/Alias", "video/mp4", "", "", 500},
		{"omitted", "", "/file/source/Clip", "video/mp4", "", "", 200},
		{"zero", "0", "/file/source/Clip", "video/mp4", "", "", 200},
		{"leading zeros", "01", "/file/source/Clip", "video/mp4", "", "", 200},
		{"maximum", "9223372036854775807", "/file/source/Clip", "video/mp4", "", "", 200},
		{"valid 304", "12", "/file/source/Clip", "video/mp4", future, "", 304},
		{"export ignores source size", "abc", "/export/source/Clip?format=pdf", "application/vnd.google-apps.document", "", "", 200},
		{"folder ignores source size", "abc", "/Clip", drive.FolderMIME, "", "", 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			app := testServer(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				if req.URL.Query().Get("fields") == "" {
					t.Errorf("HEAD fetched content: %s", req.URL)
				}
				file := drive.File{ID: "source", Name: "Clip", MIMEType: tt.mimeType, Size: tt.size, ModifiedTime: "2026-06-08T12:34:56Z"}
				if req.URL.Path == "/drive/v3/files/recovery" {
					file.ID, file.Size = "recovery", "7"
				} else if req.URL.Path == "/drive/v3/files/target" {
					file.ID = "target"
					if req.Header.Get("X-Goog-Drive-Resource-Keys") != "target/target-key" {
						t.Error("shortcut target lost its resource key")
					}
				} else if tt.name == "shortcut target" {
					file = drive.File{ID: "alias", Name: "Alias", MIMEType: "application/vnd.google-apps.shortcut", Size: "42", ShortcutDetails: &drive.ShortcutDetails{TargetID: "target", TargetMIMEType: "video/mp4", TargetResourceKey: "target-key"}}
				}
				var metadata any = file
				if req.URL.Query().Get("q") != "" {
					metadata = drive.FileList{Files: []drive.File{file}}
				}
				body, err := json.Marshal(metadata)
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
				t.Fatalf("unreadable HEAD response: %v", err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || len(body) != 0 || res.StatusCode != tt.status {
				t.Fatalf("status = %d, body = %q, error = %v; want empty %d", res.StatusCode, body, err, tt.status)
			}
			if tt.status == http.StatusInternalServerError {
				if res.Header.Get("Content-Type") != "application/json; charset=utf-8" || res.Header.Get("Last-Modified") != "" || res.Header.Get("Content-Disposition") != "" || res.Header.Get("Accept-Ranges") != "" || strings.Contains(res.Header.Get("Content-Length"), tt.size) {
					t.Fatalf("error contains file headers: %v", res.Header)
				}
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodHead, tt.target, nil))
				if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "invalid file size in Drive metadata") || strings.Contains(rec.Body.String(), canary) {
					t.Fatalf("unsafe size diagnostic: status = %d, body = %s", rec.Code, rec.Body.String())
				}
			} else if tt.status == http.StatusOK && tt.mimeType == "video/mp4" {
				ranges := ""
				if tt.size != "" {
					ranges = "bytes"
				}
				if res.Header.Get("Content-Length") != tt.size || res.Header.Get("Accept-Ranges") != ranges {
					t.Fatalf("valid size headers = %v", res.Header)
				}
			} else if res.Header.Get("Content-Length") != "" || res.Header.Get("Accept-Ranges") != "" {
				t.Fatalf("non-blob response gained size headers: %v", res.Header)
			}
			wantCalls := int32(1)
			if tt.name == "shortcut target" {
				wantCalls = 2
			}
			if tt.name == "sensitive" {
				res, err := front.Client().Get(front.URL + "/api/file?id=source")
				if err != nil {
					t.Fatal(err)
				}
				var metadata drive.File
				err = json.NewDecoder(res.Body).Decode(&metadata)
				res.Body.Close()
				if err != nil || res.StatusCode != http.StatusOK || metadata.Size != tt.size {
					t.Fatalf("metadata API changed: file = %+v, error = %v", metadata, err)
				}
				res, err = front.Client().Head(front.URL + "/file/recovery/Ready")
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()
				if res.StatusCode != http.StatusOK || res.Header.Get("Content-Length") != "7" {
					t.Fatalf("later valid HEAD = %d, %v", res.StatusCode, res.Header)
				}
				wantCalls++
			}
			if calls.Load() != wantCalls {
				t.Fatalf("metadata requests = %d; want %d", calls.Load(), wantCalls)
			}
		})
	}
}
