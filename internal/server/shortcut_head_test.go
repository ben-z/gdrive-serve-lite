package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShortcutHeadDescribesTarget(t *testing.T) {
	const older = "2026-01-01T00:00:00Z"
	const newer = "2026-02-01T00:00:00Z"
	for _, route := range []struct {
		url, mime, filename string
	}{
		{"/path/Alias%20clip", "video/mp4", "Alias%20clip"},
		{"/Alias%20clip", "video/mp4", "Alias%20clip"},
		{"/path/Alias%20clip?format=pdf", "application/vnd.google-apps.document", "Alias%20clip.pdf"},
		{"/Alias%20clip?format=pdf", "application/vnd.google-apps.document", "Alias%20clip.pdf"},
		{"/export/shortcut/Requested.pdf?format=pdf&resourceKey=shortcut-key", "application/vnd.google-apps.document", "Requested.pdf"},
		{"/export/shortcut/?format=pdf&resourceKey=shortcut-key", "application/vnd.google-apps.document", "Alias%20clip.pdf"},
	} {
		for _, conditional := range []struct {
			name, aliasTime, targetTime, since, ifNoneMatch string
			status                                          int
		}{
			{"unconditional", older, newer, "", "", http.StatusOK},
			{"target newer", older, newer, "Thu, 15 Jan 2026 00:00:00 GMT", "", http.StatusOK},
			{"target unchanged", newer, older, "Thu, 15 Jan 2026 00:00:00 GMT", "", http.StatusNotModified},
			{"etag precedence", newer, older, "Thu, 15 Jan 2026 00:00:00 GMT", `"unknown-etag"`, http.StatusOK},
		} {
			t.Run(route.url+"/"+conditional.name, func(t *testing.T) {
				calls := 0
				alias := fmt.Sprintf(`{"id":"shortcut","name":"Alias clip","mimeType":"application/vnd.google-apps.shortcut","modifiedTime":%q,"shortcutDetails":{"targetId":"target","targetMimeType":%q,"targetResourceKey":"target-key"}}`, conditional.aliasTime, route.mime)
				app := testServer(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Method != http.MethodGet || req.URL.Query().Get("alt") != "" || strings.HasSuffix(req.URL.Path, "/export") {
						t.Fatalf("HEAD fetched file content: %s %s", req.Method, req.URL)
					}
					switch {
					case req.URL.Query().Get("q") != "":
						if query := req.URL.Query().Get("q"); !strings.Contains(query, "name = 'Alias clip'") {
							t.Fatalf("lookup query = %q", query)
						}
						return jsonResponse(http.StatusOK, `{"files":[`+alias+`]}`), nil
					case req.URL.Path == "/drive/v3/files/shortcut":
						if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "shortcut/shortcut-key" {
							t.Fatalf("shortcut key header = %q", got)
						}
						return jsonResponse(http.StatusOK, alias), nil
					case req.URL.Path == "/drive/v3/files/target":
						if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "target/target-key" {
							t.Fatalf("target key header = %q", got)
						}
						return jsonResponse(http.StatusOK, fmt.Sprintf(`{"id":"target","name":"Original file","mimeType":%q,"size":"42","modifiedTime":%q}`, route.mime, conditional.targetTime)), nil
					default:
						t.Fatalf("unexpected metadata request: %s", req.URL)
						return nil, nil
					}
				})
				req := httptest.NewRequest(http.MethodHead, route.url, nil)
				req.Header.Set("If-Modified-Since", conditional.since)
				req.Header.Set("If-None-Match", conditional.ifNoneMatch)
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, req)
				if rec.Code != conditional.status || calls != 2 || rec.Body.Len() != 0 {
					t.Fatalf("status = %d, calls = %d, body = %s; want status %d and two metadata reads", rec.Code, calls, rec.Body, conditional.status)
				}
				modified := "Sun, 01 Feb 2026 00:00:00 GMT"
				if conditional.targetTime == older {
					modified = "Thu, 01 Jan 2026 00:00:00 GMT"
				}
				if got := rec.Header().Get("Last-Modified"); got != modified {
					t.Fatalf("Last-Modified = %q, want target time %q", got, modified)
				}
				if rec.Code == http.StatusNotModified {
					return
				}
				mime, length, ranges := "application/pdf", "", ""
				if route.mime == "video/mp4" {
					mime, length, ranges = route.mime, "42", "bytes"
				}
				if rec.Header().Get("Content-Type") != mime || rec.Header().Get("Content-Length") != length || rec.Header().Get("Accept-Ranges") != ranges {
					t.Fatalf("target read headers = %v", rec.Header())
				}
				if got := rec.Header().Get("Content-Disposition"); got != "inline; filename*=UTF-8''"+route.filename {
					t.Fatalf("Content-Disposition = %q, want alias filename %q", got, route.filename)
				}
			})
		}
	}
}

func TestShortcutHeadPropagatesTargetErrors(t *testing.T) {
	for _, route := range []string{"/path/Alias", "/Alias", "/export/shortcut/Alias?resourceKey=shortcut-key"} {
		for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
			t.Run(fmt.Sprintf("%s/%d", route, status), func(t *testing.T) {
				calls := 0
				alias := `{"id":"shortcut","name":"Alias","mimeType":"application/vnd.google-apps.shortcut","shortcutDetails":{"targetId":"target","targetMimeType":"application/vnd.google-apps.document","targetResourceKey":"target-key"}}`
				app := testServer(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.URL.Query().Get("alt") != "" || strings.HasSuffix(req.URL.Path, "/export") {
						t.Fatalf("HEAD fetched file content: %s", req.URL)
					}
					if req.URL.Path == "/drive/v3/files/target" {
						if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "target/target-key" {
							t.Fatalf("target key header = %q", got)
						}
						return jsonResponse(status, `{"error":{"message":"target unavailable"}}`), nil
					}
					if req.URL.Query().Get("q") != "" {
						return jsonResponse(http.StatusOK, `{"files":[`+alias+`]}`), nil
					}
					return jsonResponse(http.StatusOK, alias), nil
				})
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodHead, route, nil)
				req.Header.Set("If-Modified-Since", "Fri, 01 Jan 2027 00:00:00 GMT")
				app.Handler().ServeHTTP(rec, req)
				if rec.Code != status || calls != 2 || !strings.Contains(rec.Body.String(), "target unavailable") {
					t.Fatalf("status = %d, calls = %d, body = %s; want target error %d", rec.Code, calls, rec.Body, status)
				}
			})
		}
	}
}

func TestShortcutHeadRejectsInvalidExportBeforeTargetLookup(t *testing.T) {
	for _, route := range []string{"/path/Alias?format=invalid", "/Alias?format=invalid", "/export/shortcut/Alias?format=invalid"} {
		t.Run(route, func(t *testing.T) {
			calls := 0
			alias := `{"id":"shortcut","name":"Alias","mimeType":"application/vnd.google-apps.shortcut","shortcutDetails":{"targetId":"target","targetMimeType":"application/vnd.google-apps.document","targetResourceKey":"target-key"}}`
			app := testServer(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path == "/drive/v3/files/target" {
					t.Fatal("invalid export format fetched shortcut target")
				}
				if req.URL.Query().Get("q") != "" {
					return jsonResponse(http.StatusOK, `{"files":[`+alias+`]}`), nil
				}
				return jsonResponse(http.StatusOK, alias), nil
			})
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodHead, route, nil))
			if rec.Code != http.StatusBadRequest || calls != 1 || !strings.Contains(rec.Body.String(), "unknown export format") {
				t.Fatalf("status = %d, calls = %d, body = %s", rec.Code, calls, rec.Body)
			}
		})
	}
}
