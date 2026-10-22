package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHeadRejectsStaleUnmodifiedSinceAcrossRoutes(t *testing.T) {
	const (
		modified = "2026-06-08T12:34:56Z"
		blob     = `{"id":"source-id","name":"video.mp4","mimeType":"video/mp4","size":"12","modifiedTime":"` + modified + `"}`
		document = `{"id":"source-id","name":"Notes","mimeType":"application/vnd.google-apps.document","modifiedTime":"` + modified + `"}`
		shortcut = `{"id":"source-id","name":"link","mimeType":"application/vnd.google-apps.shortcut","modifiedTime":"2026-06-01T12:34:56Z","shortcutDetails":{"targetId":"target-id","targetMimeType":"video/mp4","targetResourceKey":"target-key"}}`
		docLink  = `{"id":"source-id","name":"link","mimeType":"application/vnd.google-apps.shortcut","modifiedTime":"2026-06-01T12:34:56Z","shortcutDetails":{"targetId":"target-id","targetMimeType":"application/vnd.google-apps.document","targetResourceKey":"target-key"}}`
	)
	for _, tt := range []struct {
		name, target, source, targetMetadata string
		calls                                int
	}{
		{"blob ID", "/file/source-id/video.mp4", blob, "", 1},
		{"blob path", "/path/video.mp4", blob, "", 1},
		{"blob root path", "/video.mp4", blob, "", 1},
		{"blob shortcut", "/path/link", shortcut, blob, 2},
		{"export ID", "/export/source-id/Notes.pdf?format=pdf", document, "", 1},
		{"export path", "/path/Notes?format=pdf", document, "", 1},
		{"export root path", "/Notes?format=pdf", document, "", 1},
		{"export shortcut", "/export/source-id/link.pdf?format=pdf", docLink, document, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			app := testServer(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Query().Get("fields") == "" {
					t.Fatalf("HEAD fetched media or an export: %s", req.URL)
				}
				if req.URL.Query().Get("q") != "" {
					return jsonResponse(http.StatusOK, `{"files":[`+tt.source+`]}`), nil
				}
				if req.URL.Path == "/drive/v3/files/target-id" {
					if tt.targetMetadata == "" || req.Header.Get("X-Goog-Drive-Resource-Keys") != "target-id/target-key" {
						t.Fatalf("unexpected shortcut target request: %s, key=%q", req.URL, req.Header.Get("X-Goog-Drive-Resource-Keys"))
					}
					return jsonResponse(http.StatusOK, strings.Replace(tt.targetMetadata, `"id":"source-id"`, `"id":"target-id"`, 1)), nil
				}
				if req.URL.Path != "/drive/v3/files/source-id" {
					t.Fatalf("unexpected metadata request: %s", req.URL)
				}
				return jsonResponse(http.StatusOK, tt.source), nil
			})
			req := httptest.NewRequest(http.MethodHead, tt.target, nil)
			req.Header.Set("If-Unmodified-Since", "Sun, 07 Jun 2026 12:34:56 GMT")
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusPreconditionFailed || rec.Body.Len() != 0 {
				t.Fatalf("status = %d, body = %q; want empty 412", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Last-Modified"); got != "Mon, 08 Jun 2026 12:34:56 GMT" {
				t.Fatalf("Last-Modified = %q, want target modification time", got)
			}
			if calls != tt.calls {
				t.Fatalf("metadata calls = %d, want %d", calls, tt.calls)
			}
		})
	}
}

func TestHeadDatePreconditionOrder(t *testing.T) {
	const (
		modified = "2026-06-08T12:34:56.999Z"
		stale    = "Sun, 07 Jun 2026 12:34:56 GMT"
		equal    = "Mon, 08 Jun 2026 12:34:56 GMT"
		future   = "Tue, 09 Jun 2026 12:34:56 GMT"
	)
	for _, tt := range []struct {
		name              string
		unmodified, since string
		match, noneMatch  string
		modified          string
		status            int
	}{
		{"stale before fresh IMS", stale, future, "", "", modified, http.StatusPreconditionFailed},
		{"stale before If-None-Match", stale, future, "", `"etag"`, modified, http.StatusPreconditionFailed},
		{"If-Match suppresses IUS", stale, "", `"etag"`, "", modified, http.StatusOK},
		{"If-Match retains IMS", stale, future, `"etag"`, "", modified, http.StatusNotModified},
		{"invalid IUS", "not a date", "", "", "", modified, http.StatusOK},
		{"invalid IUS retains IMS", "not a date", future, "", "", modified, http.StatusNotModified},
		{"empty IUS", "", "", "", "", modified, http.StatusOK},
		{"equal IUS", equal, "", "", "", modified, http.StatusOK},
		{"future IUS", future, "", "", "", modified, http.StatusOK},
		{"future IUS retains IMS", future, future, "", "", modified, http.StatusNotModified},
		{"missing modified time", stale, future, "", "", "", http.StatusOK},
		{"invalid modified time", stale, future, "", "", "invalid", http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			app := testServer(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path != "/drive/v3/files/source-id" || req.URL.Query().Get("fields") == "" {
					t.Fatalf("HEAD fetched media or an unexpected file: %s", req.URL)
				}
				modifiedField := ""
				if tt.modified != "" {
					modifiedField = fmt.Sprintf(`,"modifiedTime":%q`, tt.modified)
				}
				return jsonResponse(http.StatusOK, `{"id":"source-id","name":"video.mp4","mimeType":"video/mp4"`+modifiedField+`}`), nil
			})
			req := httptest.NewRequest(http.MethodHead, "/file/source-id/video.mp4", nil)
			req.Header.Set("If-Unmodified-Since", tt.unmodified)
			req.Header.Set("If-Modified-Since", tt.since)
			req.Header.Set("If-Match", tt.match)
			req.Header.Set("If-None-Match", tt.noneMatch)
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			if rec.Code != tt.status || rec.Body.Len() != 0 || calls != 1 {
				t.Fatalf("status = %d, body = %q, metadata calls = %d; want empty %d with one metadata read", rec.Code, rec.Body.String(), calls, tt.status)
			}
		})
	}
}
