package server

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"gdrive-serve-lite/internal/drive"
)

func TestNextLinksUseResolvedFolderResourceKey(t *testing.T) {
	const basePath = "/prefix"
	const pageToken = "next +/=?%#\"'&"
	tests := []struct {
		name        string
		target      string
		folderID    string
		resourceKey string
		html        bool
		pinsID      bool
	}{
		{"API path without key", "/api/list?path=%2FShared&resourceKey=stale-key", "folder-id", "", false, true},
		{"API path with key", "/api/list?path=%2FShared&resourceKey=stale-key", "folder-id", "actual-key", false, true},
		{"HTML path without key", "/browse/Shared?resourceKey=stale-key", "folder-id", "", true, true},
		{"HTML path with key", "/browse/Shared?resourceKey=stale-key", "folder-id", "actual-key", true, true},
		{"API root without key", "/api/list?resourceKey=stale-key", "root-folder", "", false, true},
		{"API root with key", "/api/list?resourceKey=stale-key", "root-folder", "actual-key", false, true},
		{"JSON path without ID", "/Shared?format=json&resourceKey=stale-key", "folder-id", "actual-key", false, false},
		{"JSON explicit ID", "/browse?format=json&folderId=folder-id&resourceKey=actual-key", "folder-id", "actual-key", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listCalls := 0
			opts := Options{BaseURL: basePath}
			if tt.folderID == "root-folder" {
				opts.RootResourceKey = tt.resourceKey
			}
			app := testServer(func(req *http.Request) (*http.Response, error) {
				query := req.URL.Query()
				var page drive.FileList
				if strings.Contains(query.Get("q"), "name = 'Shared'") {
					page.Files = []drive.File{{ID: tt.folderID, Name: "Shared", MIMEType: drive.FolderMIME, ResourceKey: tt.resourceKey}}
				} else {
					listCalls++
					wantHeader := ""
					if tt.resourceKey != "" {
						wantHeader = tt.folderID + "/" + tt.resourceKey
					}
					if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != wantHeader {
						t.Fatalf("page %d resource key = %q, want %q", listCalls, got, wantHeader)
					}
					if !strings.Contains(query.Get("q"), "'"+tt.folderID+"' in parents") ||
						!strings.Contains(query.Get("q"), "mimeType != '"+drive.FolderMIME+"'") ||
						query.Get("orderBy") != "name desc" || query.Get("pageSize") != "1" {
						t.Fatalf("page %d query = %v", listCalls, query)
					}
					switch listCalls {
					case 1:
						if query.Get("pageToken") != "" {
							t.Fatalf("first page token = %q", query.Get("pageToken"))
						}
						page.Files = []drive.File{{ID: "first", Name: "first.mp4", MIMEType: "video/mp4"}}
						page.NextPageToken = pageToken
					case 2:
						if query.Get("pageToken") != pageToken {
							t.Fatalf("second page token = %q, want %q", query.Get("pageToken"), pageToken)
						}
						page.Files = []drive.File{{ID: "last", Name: "last.mp4", MIMEType: "video/mp4"}}
					default:
						t.Fatalf("unexpected list request %d", listCalls)
					}
				}
				body, err := json.Marshal(page)
				if err != nil {
					t.Fatal(err)
				}
				return jsonResponse(http.StatusOK, string(body)), nil
			}, opts)

			target, err := url.Parse(tt.target)
			if err != nil {
				t.Fatal(err)
			}
			query := target.Query()
			for key, value := range map[string]string{
				"type": "files", "orderBy": "name desc", "pageSize": "1", "limit": "1", "links": "1", "custom": "a&b",
			} {
				query.Set(key, value)
			}
			target.RawQuery = query.Encode()
			first := httptest.NewRecorder()
			app.Handler().ServeHTTP(first, httptest.NewRequest(http.MethodGet, basePath+target.String(), nil))
			if first.Code != http.StatusOK {
				t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
			}
			var next string
			if tt.html {
				match := regexp.MustCompile(`<a href="([^"]*)">Next page</a>`).FindStringSubmatch(first.Body.String())
				if len(match) != 2 {
					t.Fatalf("missing next link: %s", first.Body.String())
				}
				next = html.UnescapeString(match[1])
			} else {
				var listing ListResponse
				if err := json.Unmarshal(first.Body.Bytes(), &listing); err != nil {
					t.Fatal(err)
				}
				next = listing.Next
			}
			nextURL, err := url.Parse(next)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(nextURL.Path, basePath+"/") {
				t.Fatalf("next link lost base path: %q", next)
			}
			nextQuery := nextURL.Query()
			for _, key := range []string{"type", "orderBy", "pageSize", "limit", "links", "custom", "path", "format"} {
				if nextQuery.Get(key) != query.Get(key) {
					t.Errorf("next %s = %q, want %q", key, nextQuery.Get(key), query.Get(key))
				}
			}
			wantKey := ""
			if tt.pinsID {
				wantKey = tt.resourceKey
				if nextQuery.Get("folderId") != tt.folderID {
					t.Errorf("next folder ID = %q, want %q", nextQuery.Get("folderId"), tt.folderID)
				}
			} else if nextQuery.Has("folderId") {
				t.Errorf("path-only next link gained folder ID: %q", next)
			}
			if nextQuery.Get("resourceKey") != wantKey || (wantKey == "" && nextQuery.Has("resourceKey")) {
				t.Errorf("next resource key = %q, want %q", nextQuery.Get("resourceKey"), wantKey)
			}
			second := httptest.NewRecorder()
			app.Handler().ServeHTTP(second, httptest.NewRequest(http.MethodGet, next, nil))
			if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), "last.mp4") {
				t.Fatalf("second status = %d, body = %s", second.Code, second.Body.String())
			}
			if listCalls != 2 {
				t.Fatalf("list calls = %d, want 2", listCalls)
			}
		})
	}
}
