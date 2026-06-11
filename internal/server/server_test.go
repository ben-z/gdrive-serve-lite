package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gdrive-serve-lite/internal/config"
	"gdrive-serve-lite/internal/drive"
)

func TestAPIListAggregatesPagesAndReturnsNextURL(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		q := req.URL.Query()
		switch calls {
		case 1:
			if q.Get("pageSize") != "2" {
				t.Fatalf("first pageSize = %q", q.Get("pageSize"))
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"a","name":"a.mp4"},{"id":"b","name":"b.mp4"}],"nextPageToken":"tok-2"}`), nil
		case 2:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"c","name":"c.mp4"}],"nextPageToken":"tok-3"}`), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/api/list?folderId=folder-1&type=files&pageSize=2&limit=3", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 3 {
		t.Fatalf("Count = %d", body.Count)
	}
	if body.NextPageToken != "tok-3" {
		t.Fatalf("NextPageToken = %q", body.NextPageToken)
	}
	if !strings.Contains(body.Next, "folderId=folder-1") || !strings.Contains(body.Next, "pageToken=tok-3") {
		t.Fatalf("Next = %q", body.Next)
	}
}

func TestAPIListDefaultsToRootFolderResourceKey(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "root-folder/root-key-1" {
			t.Fatalf("resource key header = %q", got)
		}
		query := req.URL.Query().Get("q")
		if !strings.Contains(query, "'root-folder' in parents") {
			t.Fatalf("query = %q", query)
		}
		return jsonResponse(http.StatusOK, `{"files":[{"id":"a","name":"a.mp4"}]}`), nil
	}, Options{RootResourceKey: "root-key-1"})

	req := httptest.NewRequest(http.MethodGet, "/api/list?type=files&pageSize=1000&limit=1000", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.FolderID != "root-folder" || body.ResourceKey != "root-key-1" || body.Count != 1 {
		t.Fatalf("body = %+v", body)
	}
}

func TestAPIListForwardsFolderResourceKey(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "folder-1/folder-key-1" {
			t.Fatalf("resource key header = %q", got)
		}
		query := req.URL.Query().Get("q")
		if !strings.Contains(query, "'folder-1' in parents") {
			t.Fatalf("query = %q", query)
		}
		return jsonResponse(http.StatusOK, `{"files":[{"id":"a","name":"a.mp4"}],"nextPageToken":"tok-2"}`), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/list?folderId=folder-1&resourceKey=folder-key-1&pageSize=1&limit=1", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ResourceKey != "folder-key-1" {
		t.Fatalf("ResourceKey = %q", body.ResourceKey)
	}
	if !strings.Contains(body.Next, "resourceKey=folder-key-1") {
		t.Fatalf("Next = %q", body.Next)
	}
}

func TestAPIListIncludesReadyEntryURLs(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{
			"files":[
				{"id":"folder-1","name":"Shared","mimeType":"application/vnd.google-apps.folder","resourceKey":"folder-key-1"},
				{"id":"file-1","name":"100%/done.mp4","mimeType":"video/mp4","resourceKey":"file-key-1"},
				{"id":"doc-1","name":"Notes","mimeType":"application/vnd.google-apps.document","resourceKey":"doc-key-1","exportLinks":{"application/vnd.openxmlformats-officedocument.wordprocessingml.document":"https://example.invalid/docx"}},
				{"id":"shortcut-1","name":"shortcut.mp4","mimeType":"application/vnd.google-apps.shortcut","shortcutDetails":{"targetId":"file-2","targetMimeType":"video/mp4","targetResourceKey":"target-key-1"}},
				{"id":"shortcut-2","name":"folder shortcut","mimeType":"application/vnd.google-apps.shortcut","shortcutDetails":{"targetId":"folder-2","targetMimeType":"application/vnd.google-apps.folder","targetResourceKey":"folder-target-key"}}
			]
		}`), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/list?folderId=root-folder&pageSize=1000&limit=1000&links=1", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string, len(body.Files))
	for _, file := range body.Files {
		got[file.Name] = file.URL
	}
	want := map[string]string{
		"Shared":          "/browse?folderId=folder-1&resourceKey=folder-key-1",
		"100%/done.mp4":   "/file/file-1/100%25%2Fdone.mp4?resourceKey=file-key-1",
		"Notes":           "/export/doc-1/Notes.docx?resourceKey=doc-key-1",
		"shortcut.mp4":    "/file/file-2/shortcut.mp4?resourceKey=target-key-1",
		"folder shortcut": "/browse?folderId=folder-2&resourceKey=folder-target-key",
	}
	for name, wantURL := range want {
		if got[name] != wantURL {
			t.Fatalf("url for %q = %q, want %q; body=%s", name, got[name], wantURL, rec.Body.String())
		}
	}
}

func TestAPIListOmitsReadyEntryURLsByDefault(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4"}]}`), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/list?folderId=root-folder&pageSize=1000&limit=1000", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(body.Files))
	}
	if body.Files[0].URL != "" {
		t.Fatalf("URL = %q, want empty by default", body.Files[0].URL)
	}
	if strings.Contains(rec.Body.String(), `"url"`) {
		t.Fatalf("body should not include url by default: %s", rec.Body.String())
	}
}

func TestBaseURLPrefixesAPIListLinks(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/drive/v3/files" {
			t.Fatalf("Drive path = %q", req.URL.Path)
		}
		return jsonResponse(http.StatusOK, `{
			"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4"}],
			"nextPageToken":"tok-2"
		}`), nil
	}, Options{BaseURL: "/gdrive/"})

	req := httptest.NewRequest(http.MethodGet, "/gdrive/api/list?folderId=root-folder&pageSize=1&limit=1&links=1", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body.Next, "/gdrive/api/list?") ||
		!strings.Contains(body.Next, "pageToken=tok-2") {
		t.Fatalf("Next = %q", body.Next)
	}
	if len(body.Files) != 1 || body.Files[0].URL != "/gdrive/file/file-1/video.mp4" {
		t.Fatalf("files = %+v", body.Files)
	}
}

func TestBaseURLPrefixesBrowseLinks(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{
			"files":[
				{"id":"folder-1","name":"Shared","mimeType":"application/vnd.google-apps.folder"},
				{"id":"file-1","name":"video.mp4","mimeType":"video/mp4"}
			],
			"nextPageToken":"tok-2"
		}`), nil
	}, Options{BaseURL: "gdrive"})

	req := httptest.NewRequest(http.MethodGet, "/gdrive/?pageSize=1&limit=1", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"/gdrive/browse/Shared?folderId=folder-1",
		"/gdrive/file/file-1/video.mp4",
		"/gdrive/browse/?folderId=root-folder&amp;limit=1&amp;pageSize=1&amp;pageToken=tok-2",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestBaseURLRejectsUnprefixedRequests(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected Drive request: %s", req.URL.String())
		return nil, nil
	}, Options{BaseURL: "/gdrive"})

	for _, path := range []string{
		"/api/list?folderId=root-folder",
		"/gdrive-other/api/list?folderId=root-folder",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestBaseURLPreservesEscapedPathsForResolution(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		target  string
	}{
		{"plain prefix", "/gdrive", "/gdrive/browse/a%2Fb"},
		{"escapable prefix", "/my base", "/my%20base/browse/a%2Fb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			app := testServer(func(req *http.Request) (*http.Response, error) {
				calls++
				switch calls {
				case 1:
					query := req.URL.Query().Get("q")
					if !strings.Contains(query, "name = 'a/b'") {
						t.Fatalf("resolve query = %q", query)
					}
					return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"a/b","mimeType":"application/vnd.google-apps.folder"}]}`), nil
				case 2:
					return jsonResponse(http.StatusOK, `{"files":[]}`), nil
				default:
					t.Fatalf("unexpected request %d", calls)
					return nil, nil
				}
			}, Options{BaseURL: tt.baseURL})

			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if calls != 2 {
				t.Fatalf("calls = %d, want 2", calls)
			}
		})
	}
}

func TestMethodNotAllowedSetsAllowHeader(t *testing.T) {
	tests := []struct {
		path  string
		allow string
	}{
		{"/api/list", "GET"},
		{"/api/file?id=file-1", "GET"},
		{"/api/resolve?path=/folder", "GET"},
		{"/api/export-formats?id=doc-1", "GET"},
		{"/file/file-1/video.mp4", "GET, HEAD"},
		{"/export/doc-1/Notes", "GET, HEAD"},
		{"/path/folder/video.mp4", "GET, HEAD"},
		{"/browse/folder", "GET, HEAD"},
		{"/folder/video.mp4", "GET, HEAD"},
		// Prefix routes without a trailing slash fall through to the root
		// handler's method check rather than the mux's trailing-slash
		// redirect.
		{"/file", "GET, HEAD"},
		{"/export", "GET, HEAD"},
		{"/path", "GET, HEAD"},
		{"/browse", "GET, HEAD"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			var calls int
			app := testServer(func(req *http.Request) (*http.Response, error) {
				calls++
				t.Fatalf("unexpected Drive request for method rejection: %s %s", req.Method, req.URL.String())
				return nil, nil
			})

			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Allow") != tt.allow {
				t.Fatalf("Allow = %q, want %q", rec.Header().Get("Allow"), tt.allow)
			}
			if rec.Body.Len() != 0 {
				t.Fatalf("body = %q, want empty", rec.Body.String())
			}
			if calls != 0 {
				t.Fatalf("calls = %d, want none", calls)
			}
		})
	}
}

func TestBasicAuthRejectsUnauthorizedRequests(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		t.Fatal("Drive transport should not be reached")
		return nil, nil
	}, Options{Username: "ben", Password: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/api/list", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing WWW-Authenticate header")
	}
	if !strings.Contains(rec.Body.String(), "unauthorized") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestBasicAuthAllowsAuthorizedRequests(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(http.StatusOK, `{"files":[{"id":"a","name":"a.mp4"}]}`), nil
	}, Options{Username: "ben", Password: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/api/list", nil)
	req.SetBasicAuth("ben", "secret")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestCORSPreflightBypassesBasicAuth(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected Drive request for preflight: %s %s", req.Method, req.URL.String())
		return nil, nil
	}, Options{Username: "ben", Password: "secret", AllowOrigin: "http://localhost:3000"})

	req := httptest.NewRequest(http.MethodOptions, "/api/list", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Range")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Fatalf("Access-Control-Allow-Origin = %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("Access-Control-Allow-Credentials = %q", rec.Header().Get("Access-Control-Allow-Credentials"))
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Range") {
		t.Fatalf("Access-Control-Allow-Headers = %q", rec.Header().Get("Access-Control-Allow-Headers"))
	}
}

func TestCORSHeadersOnAllowedResponse(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"files":[{"id":"a","name":"a.mp4"}]}`), nil
	}, Options{AllowOrigin: "*"})

	req := httptest.NewRequest(http.MethodGet, "/api/list", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("Access-Control-Allow-Credentials = %q", rec.Header().Get("Access-Control-Allow-Credentials"))
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), "X-Goog-Hash") {
		t.Fatalf("Access-Control-Expose-Headers = %q", rec.Header().Get("Access-Control-Expose-Headers"))
	}
}

func TestCORSRejectsDifferentOrigin(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"files":[{"id":"a","name":"a.mp4"}]}`), nil
	}, Options{AllowOrigin: "http://localhost:3000"})

	req := httptest.NewRequest(http.MethodGet, "/api/list", nil)
	req.Header.Set("Origin", "http://evil.invalid")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestRootPathCanReturnJSONListing(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query().Get("q")
		if !strings.Contains(query, "'root-folder' in parents") ||
			!strings.Contains(query, "mimeType != '"+drive.FolderMIME+"'") {
			t.Fatalf("query = %q", query)
		}
		if req.URL.Query().Get("pageSize") != "1" {
			t.Fatalf("pageSize = %q", req.URL.Query().Get("pageSize"))
		}
		return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4"}],"nextPageToken":"tok-2"}`), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/?format=json&type=files&pageSize=1&limit=1", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	var body ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Path != "/" || body.FolderID != "root-folder" || body.Count != 1 {
		t.Fatalf("body = %+v", body)
	}
	if body.NextPageToken != "tok-2" {
		t.Fatalf("NextPageToken = %q", body.NextPageToken)
	}
	if !strings.Contains(body.Next, "format=json") || !strings.Contains(body.Next, "pageToken=tok-2") {
		t.Fatalf("Next = %q", body.Next)
	}
}

func TestRootPathHTMLLinksPreserveResourceKeysAndShortcutTargets(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query().Get("q")
		if !strings.Contains(query, "'root-folder' in parents") {
			t.Fatalf("query = %q", query)
		}
		return jsonResponse(http.StatusOK, `{
			"files":[
				{"id":"folder-1","name":"Shared","mimeType":"application/vnd.google-apps.folder","resourceKey":"folder-key-1"},
				{"id":"folder-2","name":"A/B?C","mimeType":"application/vnd.google-apps.folder"},
				{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","resourceKey":"file-key-1"},
				{"id":"doc-1","name":"Notes","mimeType":"application/vnd.google-apps.document","resourceKey":"doc-key-1"},
				{"id":"shortcut-1","name":"shortcut.mp4","mimeType":"application/vnd.google-apps.shortcut","shortcutDetails":{"targetId":"file-2","targetMimeType":"video/mp4","targetResourceKey":"target-key-1"}}
			]
		}`), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		">Shared/</a>",
		"/browse/Shared?folderId=folder-1&amp;resourceKey=folder-key-1",
		"/browse/A%2FB%3FC?folderId=folder-2",
		"/file/file-1/video.mp4?resourceKey=file-key-1",
		"/export/doc-1/Notes.docx?resourceKey=doc-key-1",
		"/file/file-2/shortcut.mp4?resourceKey=target-key-1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestBrowsePathPreservesEscapedSlashForResolution(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		query := req.URL.Query().Get("q")
		switch calls {
		case 1:
			if !strings.Contains(query, "'root-folder' in parents") ||
				!strings.Contains(query, "name = 'A/B?C'") {
				t.Fatalf("resolve query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"A/B?C","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		case 2:
			if !strings.Contains(query, "'folder-1' in parents") {
				t.Fatalf("list query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[]}`), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/browse/A%2FB%3FC", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if !strings.Contains(rec.Body.String(), "/A/B?C") {
		t.Fatalf("body missing display path:\n%s", rec.Body.String())
	}
}

func TestBrowseLinksPreserveEscapedParentPath(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query().Get("q")
		if !strings.Contains(query, "'folder-1' in parents") {
			t.Fatalf("query = %q", query)
		}
		return jsonResponse(http.StatusOK, `{
			"files":[
				{"id":"child-1","name":"child","mimeType":"application/vnd.google-apps.folder"}
			],
			"nextPageToken":"tok-2"
		}`), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/browse/A%2FB?folderId=folder-1&pageSize=1&limit=1", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"/browse/A%2FB/child?folderId=child-1",
		"/browse/A%2FB?",
		"folderId=folder-1",
		"pageToken=tok-2",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "%252F") {
		t.Fatalf("body contains double-escaped slash:\n%s", body)
	}
}

func TestRootCompatibleFolderPathListsFolder(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			query := req.URL.Query().Get("q")
			if !strings.Contains(query, "'root-folder' in parents") ||
				!strings.Contains(query, "name = 'folder'") {
				t.Fatalf("resolve query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"folder","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		case 2:
			query := req.URL.Query().Get("q")
			if !strings.Contains(query, "'folder-1' in parents") {
				t.Fatalf("list query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","size":"10"}]}`), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/folder", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if !strings.Contains(rec.Body.String(), "video.mp4") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestRootCompatibleFolderPathCanReturnJSONListing(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			query := req.URL.Query().Get("q")
			if !strings.Contains(query, "'root-folder' in parents") ||
				!strings.Contains(query, "name = 'folder'") {
				t.Fatalf("resolve query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"folder","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		case 2:
			query := req.URL.Query().Get("q")
			if !strings.Contains(query, "'folder-1' in parents") {
				t.Fatalf("list query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","size":"10"}],"nextPageToken":"tok-2"}`), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/folder?type=files&pageSize=1000&limit=1", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	var body ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Path != "/folder" || body.FolderID != "folder-1" || body.Count != 1 {
		t.Fatalf("body = %+v", body)
	}
	if body.Files[0].Name != "video.mp4" {
		t.Fatalf("files = %+v", body.Files)
	}
	if !strings.HasPrefix(body.Next, "/folder?") ||
		!strings.Contains(body.Next, "pageToken=tok-2") ||
		!strings.Contains(body.Next, "type=files") {
		t.Fatalf("Next = %q", body.Next)
	}
}

func TestRootCompatibleJSONNextPreservesEscapedPath(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		query := req.URL.Query().Get("q")
		switch calls {
		case 1:
			if !strings.Contains(query, "'root-folder' in parents") ||
				!strings.Contains(query, "name = 'A/B'") {
				t.Fatalf("resolve query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"A/B","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		case 2:
			if !strings.Contains(query, "'folder-1' in parents") {
				t.Fatalf("list query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4"}],"nextPageToken":"tok-2"}`), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/A%2FB?type=files&pageSize=1&limit=1", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body.Next, "/A%2FB?") ||
		!strings.Contains(body.Next, "pageToken=tok-2") ||
		strings.Contains(body.Next, "%252F") {
		t.Fatalf("Next = %q", body.Next)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestBrowseHeadResolvesFolderButDoesNotList(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		query := req.URL.Query().Get("q")
		if !strings.Contains(query, "'root-folder' in parents") ||
			!strings.Contains(query, "name = 'folder'") {
			t.Fatalf("resolve query = %q", query)
		}
		return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"folder","mimeType":"application/vnd.google-apps.folder"}]}`), nil
	})

	req := httptest.NewRequest(http.MethodHead, "/browse/folder", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want folder resolution only", calls)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestRootCompatibleFilePathStreamsMedia(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"folder","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		case 2:
			query := req.URL.Query().Get("q")
			if !strings.Contains(query, "'folder-1' in parents") ||
				!strings.Contains(query, "name = 'video.mp4'") {
				t.Fatalf("file query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","size":"10"}]}`), nil
		case 3:
			if req.URL.Path != "/drive/v3/files/file-1" {
				t.Fatalf("path = %q", req.URL.Path)
			}
			if req.URL.Query().Get("alt") != "media" {
				t.Fatalf("alt = %q", req.URL.Query().Get("alt"))
			}
			if req.Header.Get("Range") != "bytes=1-3" {
				t.Fatalf("Range = %q", req.Header.Get("Range"))
			}
			resp := textResponse(http.StatusPartialContent, "bcd")
			resp.Header.Set("Content-Type", "video/mp4")
			resp.Header.Set("Content-Range", "bytes 1-3/10")
			return resp, nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/folder/video.mp4", nil)
	req.Header.Set("Range", "bytes=1-3")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if rec.Body.String() != "bcd" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("Content-Range") != "bytes 1-3/10" {
		t.Fatalf("Content-Range = %q", rec.Header().Get("Content-Range"))
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "video.mp4") {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
}

func TestRootCompatibleWorkspacePathExports(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"docs","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		case 2:
			return jsonResponse(http.StatusOK, `{
				"files":[{
					"id":"doc-1",
					"name":"Notes",
					"mimeType":"application/vnd.google-apps.document",
					"exportLinks":{"application/pdf":"https://example.invalid/pdf"}
				}]
			}`), nil
		case 3:
			if req.URL.Path != "/drive/v3/files/doc-1/export" {
				t.Fatalf("path = %q", req.URL.Path)
			}
			if req.URL.Query().Get("mimeType") != "application/pdf" {
				t.Fatalf("mimeType = %q", req.URL.Query().Get("mimeType"))
			}
			return textResponse(http.StatusOK, "%PDF"), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/docs/Notes?format=pdf", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if rec.Body.String() != "%PDF" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "Notes.pdf") {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
}

func TestFileEndpointStreamsMediaInOneDriveRequest(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Path != "/drive/v3/files/file-1" {
			t.Fatalf("path = %q", req.URL.Path)
		}
		if req.URL.Query().Get("alt") != "media" {
			t.Fatalf("alt = %q", req.URL.Query().Get("alt"))
		}
		if req.URL.Query().Get("fields") != "" {
			t.Fatalf("unexpected metadata request fields=%q", req.URL.Query().Get("fields"))
		}
		if req.Header.Get("Range") != "bytes=1-3" {
			t.Fatalf("Range = %q", req.Header.Get("Range"))
		}
		resp := textResponse(http.StatusPartialContent, "bcd")
		resp.Header.Set("Content-Type", "video/mp4")
		resp.Header.Set("Content-Length", "3")
		resp.Header.Set("Content-Range", "bytes 1-3/10")
		resp.Header.Set("Content-Disposition", "attachment; filename=drive-name.mp4")
		resp.Header.Set("Cache-Control", "private, max-age=60")
		resp.Header.Add("X-Goog-Hash", "crc32c=abcd")
		resp.Header.Add("X-Goog-Hash", "md5=efgh")
		return resp, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/file/file-1/video.mp4", nil)
	req.Header.Set("Range", "bytes=1-3")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if rec.Body.String() != "bcd" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("Content-Range") != "bytes 1-3/10" {
		t.Fatalf("Content-Range = %q", rec.Header().Get("Content-Range"))
	}
	if rec.Header().Get("Content-Disposition") != "attachment; filename=drive-name.mp4" {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	if rec.Header().Get("Cache-Control") != "private, max-age=60" {
		t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	if got := rec.Result().Header.Values("X-Goog-Hash"); len(got) != 2 || got[0] != "crc32c=abcd" || got[1] != "md5=efgh" {
		t.Fatalf("X-Goog-Hash = %q", got)
	}
}

func TestFileEndpointPreservesEscapedDecorativeName(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Path != "/drive/v3/files/file-1" {
			t.Fatalf("path = %q", req.URL.Path)
		}
		return textResponse(http.StatusOK, "media-bytes"), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/file/file-1/100%25%2Fdone.mp4", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "100%25%2Fdone.mp4") {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
}

func TestFileEndpointForwardsConditionalHeaders(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Query().Get("alt") != "media" {
			t.Fatalf("alt = %q", req.URL.Query().Get("alt"))
		}
		if req.Header.Get("If-None-Match") != `"etag-1"` {
			t.Fatalf("If-None-Match = %q", req.Header.Get("If-None-Match"))
		}
		if req.Header.Get("If-Range") != `"range-etag"` {
			t.Fatalf("If-Range = %q", req.Header.Get("If-Range"))
		}
		if req.Header.Get("Cookie") != "" {
			t.Fatalf("Cookie should not be forwarded: %q", req.Header.Get("Cookie"))
		}
		resp := textResponse(http.StatusNotModified, "")
		resp.Header.Set("ETag", `"etag-1"`)
		return resp, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/file/file-1/video.mp4", nil)
	req.Header.Set("If-None-Match", `"etag-1"`)
	req.Header.Set("If-Range", `"range-etag"`)
	req.Header.Set("Cookie", "local=only")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if rec.Header().Get("ETag") != `"etag-1"` {
		t.Fatalf("ETag = %q", rec.Header().Get("ETag"))
	}
	if rec.Header().Get("Content-Disposition") != "" {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
}

func TestFileEndpointPassesThroughRangeError(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Query().Get("alt") != "media" {
			t.Fatalf("alt = %q", req.URL.Query().Get("alt"))
		}
		if req.Header.Get("Range") != "bytes=999-1000" {
			t.Fatalf("Range = %q", req.Header.Get("Range"))
		}
		resp := textResponse(http.StatusRequestedRangeNotSatisfiable, "range outside file")
		resp.Header.Set("Content-Range", "bytes */100")
		resp.Header.Set("Content-Type", "text/plain")
		return resp, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/file/file-1/video.mp4", nil)
	req.Header.Set("Range", "bytes=999-1000")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if rec.Header().Get("Content-Range") != "bytes */100" {
		t.Fatalf("Content-Range = %q", rec.Header().Get("Content-Range"))
	}
	if rec.Header().Get("Content-Disposition") != "" {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	if rec.Body.String() != "range outside file" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestFileEndpointForwardsResourceKey(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Query().Get("alt") != "media" {
			t.Fatalf("alt = %q", req.URL.Query().Get("alt"))
		}
		if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "file-1/resource-key-1" {
			t.Fatalf("resource key header = %q", got)
		}
		return textResponse(http.StatusOK, "ok"), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/file/file-1/video.mp4?resourceKey=resource-key-1", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestFileHeadUsesMetadataOnly(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodGet {
			t.Fatalf("method = %q", req.Method)
		}
		if req.URL.Path != "/drive/v3/files/file-1" {
			t.Fatalf("path = %q", req.URL.Path)
		}
		if req.URL.Query().Get("alt") != "" {
			t.Fatalf("unexpected media request alt=%q", req.URL.Query().Get("alt"))
		}
		if req.URL.Query().Get("fields") == "" {
			t.Fatal("expected metadata fields query")
		}
		return jsonResponse(http.StatusOK, `{"id":"file-1","name":"actual.mp4","mimeType":"video/mp4","size":"10","modifiedTime":"2026-06-08T12:34:56.789Z"}`), nil
	})

	req := httptest.NewRequest(http.MethodHead, "/file/file-1/request-name.mp4", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if rec.Header().Get("Content-Length") != "10" {
		t.Fatalf("Content-Length = %q", rec.Header().Get("Content-Length"))
	}
	if rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("Accept-Ranges = %q", rec.Header().Get("Accept-Ranges"))
	}
	if rec.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Last-Modified") != "Mon, 08 Jun 2026 12:34:56 GMT" {
		t.Fatalf("Last-Modified = %q", rec.Header().Get("Last-Modified"))
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "request-name.mp4") {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
}

func TestFileHeadHonorsIfModifiedSince(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Query().Get("alt") != "" {
			t.Fatalf("unexpected media request alt=%q", req.URL.Query().Get("alt"))
		}
		return jsonResponse(http.StatusOK, `{"id":"file-1","name":"actual.mp4","mimeType":"video/mp4","size":"10","modifiedTime":"2026-06-08T12:34:56Z"}`), nil
	})

	req := httptest.NewRequest(http.MethodHead, "/file/file-1/request-name.mp4", nil)
	req.Header.Set("If-Modified-Since", "Mon, 08 Jun 2026 12:34:56 GMT")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if rec.Header().Get("Last-Modified") != "Mon, 08 Jun 2026 12:34:56 GMT" {
		t.Fatalf("Last-Modified = %q", rec.Header().Get("Last-Modified"))
	}
	if rec.Header().Get("Content-Length") != "" {
		t.Fatalf("Content-Length = %q", rec.Header().Get("Content-Length"))
	}
	if rec.Header().Get("Content-Disposition") != "" {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
}

func TestAPIFileMetadata(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/drive/v3/files/file-1" {
			t.Fatalf("path = %q", req.URL.Path)
		}
		if req.URL.Query().Get("fields") == "" {
			t.Fatal("expected fields query")
		}
		return jsonResponse(http.StatusOK, `{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","size":"10"}`), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/file?id=file-1", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var file drive.File
	if err := json.Unmarshal(rec.Body.Bytes(), &file); err != nil {
		t.Fatal(err)
	}
	if file.ID != "file-1" || file.Name != "video.mp4" {
		t.Fatalf("file = %+v", file)
	}
}

func TestExportEndpointDefaultsToRcloneLikeFormat(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if req.URL.Path != "/drive/v3/files/doc-1" {
				t.Fatalf("metadata path = %q", req.URL.Path)
			}
			if req.URL.Query().Get("fields") == "" {
				t.Fatal("expected metadata fields query")
			}
			return jsonResponse(http.StatusOK, `{
				"id":"doc-1",
				"name":"Meeting Notes",
				"mimeType":"application/vnd.google-apps.document",
				"exportLinks":{
					"application/pdf":"https://example.invalid/pdf",
					"application/vnd.openxmlformats-officedocument.wordprocessingml.document":"https://example.invalid/docx"
				}
			}`), nil
		case 2:
			if req.URL.Path != "/drive/v3/files/doc-1/export" {
				t.Fatalf("export path = %q", req.URL.Path)
			}
			if req.URL.Query().Get("mimeType") != docxMIME {
				t.Fatalf("mimeType = %q", req.URL.Query().Get("mimeType"))
			}
			resp := textResponse(http.StatusOK, "docx-bytes")
			resp.Header.Set("Content-Type", docxMIME)
			resp.Header.Set("Content-Length", "10")
			resp.Header.Set("Content-Disposition", "attachment; filename=drive-doc.docx")
			resp.Header.Set("Cache-Control", "private, max-age=60")
			resp.Header.Set("Expires", "Mon, 08 Jun 2026 13:34:56 GMT")
			return resp, nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/export/doc-1/Meeting%20Notes", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if rec.Body.String() != "docx-bytes" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != docxMIME {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Content-Disposition") != "attachment; filename=drive-doc.docx" {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	if rec.Header().Get("Cache-Control") != "private, max-age=60" {
		t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("Expires") != "Mon, 08 Jun 2026 13:34:56 GMT" {
		t.Fatalf("Expires = %q", rec.Header().Get("Expires"))
	}
}

func TestExportEndpointPreservesEscapedDecorativeName(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return jsonResponse(http.StatusOK, `{
				"id":"doc-1",
				"name":"ignored",
				"mimeType":"application/vnd.google-apps.document",
				"exportLinks":{"application/pdf":"https://example.invalid/pdf"}
			}`), nil
		case 2:
			if req.URL.Path != "/drive/v3/files/doc-1/export" {
				t.Fatalf("export path = %q", req.URL.Path)
			}
			return textResponse(http.StatusOK, "%PDF"), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/export/doc-1/Report%20100%25%2Ffinal?format=pdf", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "Report%20100%25%2Ffinal.pdf") {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
}

func TestExportEndpointRejectsUnsupportedFormatBeforeExport(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls != 1 {
			t.Fatalf("unexpected export request %d", calls)
		}
		return jsonResponse(http.StatusOK, `{
			"id":"doc-1",
			"name":"Notes",
			"mimeType":"application/vnd.google-apps.document",
			"exportLinks":{
				"application/pdf":"https://example.invalid/pdf"
			}
		}`), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/export/doc-1/Notes?format=xlsx", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want metadata only", calls)
	}
	if !strings.Contains(rec.Body.String(), "not supported") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestExportHeadHonorsIfModifiedSince(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Query().Get("alt") != "" {
			t.Fatalf("unexpected media request alt=%q", req.URL.Query().Get("alt"))
		}
		return jsonResponse(http.StatusOK, `{
			"id":"doc-1",
			"name":"Notes",
			"mimeType":"application/vnd.google-apps.document",
			"modifiedTime":"2026-06-08T12:34:56Z"
		}`), nil
	})

	req := httptest.NewRequest(http.MethodHead, "/export/doc-1/Notes?format=pdf", nil)
	req.Header.Set("If-Modified-Since", "Mon, 08 Jun 2026 12:34:56 GMT")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want metadata only", calls)
	}
	if rec.Header().Get("Last-Modified") != "Mon, 08 Jun 2026 12:34:56 GMT" {
		t.Fatalf("Last-Modified = %q", rec.Header().Get("Last-Modified"))
	}
	if rec.Header().Get("Content-Type") != "" {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Accept-Ranges") != "" {
		t.Fatalf("Accept-Ranges = %q", rec.Header().Get("Accept-Ranges"))
	}
	if rec.Header().Get("Content-Disposition") != "" {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
}

func TestExportEndpointPassesThroughDriveError(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return jsonResponse(http.StatusOK, `{
				"id":"doc-1",
				"name":"Notes",
				"mimeType":"application/vnd.google-apps.document"
			}`), nil
		case 2:
			if req.URL.Path != "/drive/v3/files/doc-1/export" {
				t.Fatalf("export path = %q", req.URL.Path)
			}
			resp := jsonResponse(http.StatusForbidden, `{"error":"cannotExport"}`)
			resp.Header.Set("ETag", `"drive-error-etag"`)
			return resp, nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/export/doc-1/Notes?format=pdf", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("ETag") != `"drive-error-etag"` {
		t.Fatalf("ETag = %q", rec.Header().Get("ETag"))
	}
	if rec.Header().Get("Content-Disposition") != "" {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	if rec.Body.String() != `{"error":"cannotExport"}` {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestExportEndpointForwardsResourceKey(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "doc-1/resource-key-1" {
			t.Fatalf("request %d resource key header = %q", calls, got)
		}
		switch calls {
		case 1:
			return jsonResponse(http.StatusOK, `{"id":"doc-1","name":"Notes","mimeType":"application/vnd.google-apps.document"}`), nil
		case 2:
			if req.URL.Path != "/drive/v3/files/doc-1/export" {
				t.Fatalf("path = %q", req.URL.Path)
			}
			return textResponse(http.StatusOK, "docx-bytes"), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/export/doc-1/Notes?resourceKey=resource-key-1", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestExportEndpointShortcutUsesTargetResourceKey(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "shortcut-1/shortcut-key-1" {
				t.Fatalf("metadata resource key header = %q", got)
			}
			return jsonResponse(http.StatusOK, `{"id":"shortcut-1","name":"Notes shortcut","mimeType":"application/vnd.google-apps.shortcut","shortcutDetails":{"targetId":"doc-1","targetMimeType":"application/vnd.google-apps.document","targetResourceKey":"target-key-1"}}`), nil
		case 2:
			if req.URL.Path != "/drive/v3/files/doc-1/export" {
				t.Fatalf("path = %q", req.URL.Path)
			}
			if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "doc-1/target-key-1" {
				t.Fatalf("export resource key header = %q", got)
			}
			return textResponse(http.StatusOK, "docx-bytes"), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/export/shortcut-1/Notes?resourceKey=shortcut-key-1", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestExportEndpointRejectsRange(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		t.Fatalf("unexpected Drive request %d: %s %s", calls, req.Method, req.URL.String())
		return nil, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/export/doc-1/Notes", nil)
	req.Header.Set("Range", "bytes=0-9")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 0 {
		t.Fatalf("calls = %d, want no Drive requests", calls)
	}
	if !strings.Contains(rec.Body.String(), "Range is not supported") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestAPIExportFormats(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/drive/v3/files/doc-1" {
			t.Fatalf("path = %q", req.URL.Path)
		}
		return jsonResponse(http.StatusOK, `{
			"id":"doc-1",
			"name":"Notes",
			"mimeType":"application/vnd.google-apps.document",
			"exportLinks":{
				"application/pdf":"https://example.invalid/pdf",
				"application/vnd.openxmlformats-officedocument.wordprocessingml.document":"https://example.invalid/docx"
			}
		}`), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/export-formats?id=doc-1", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"defaultExport"`) ||
		!strings.Contains(rec.Body.String(), `"docx"`) ||
		!strings.Contains(rec.Body.String(), `"pdf"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestAPIResolveFile(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		query := req.URL.Query().Get("q")
		switch calls {
		case 1:
			if !strings.Contains(query, "'root-folder' in parents") ||
				!strings.Contains(query, "name = 'folder'") ||
				!strings.Contains(query, "mimeType = '"+drive.FolderMIME+"'") {
				t.Fatalf("folder query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"folder","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		case 2:
			if !strings.Contains(query, "'folder-1' in parents") ||
				!strings.Contains(query, "name = 'video.mp4'") ||
				!strings.Contains(query, "mimeType != '"+drive.FolderMIME+"'") {
				t.Fatalf("file query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","size":"10"}]}`), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/api/resolve?path=/folder/video.mp4&type=file", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body ResolveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ID != "file-1" || body.FolderID != "" || body.ParentID != "folder-1" || body.IsDir {
		t.Fatalf("body = %+v", body)
	}
	if body.File.Name != "video.mp4" {
		t.Fatalf("file = %+v", body.File)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestPathEndpointStreamsResolvedFile(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"folder","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		case 2:
			query := req.URL.Query().Get("q")
			if !strings.Contains(query, "'folder-1' in parents") ||
				!strings.Contains(query, "name = 'video.mp4'") ||
				!strings.Contains(query, "mimeType != '"+drive.FolderMIME+"'") {
				t.Fatalf("file query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","size":"10"}]}`), nil
		case 3:
			if req.URL.Path != "/drive/v3/files/file-1" {
				t.Fatalf("path = %q", req.URL.Path)
			}
			if req.URL.Query().Get("alt") != "media" {
				t.Fatalf("alt = %q", req.URL.Query().Get("alt"))
			}
			if req.Header.Get("Range") != "bytes=1-3" {
				t.Fatalf("Range = %q", req.Header.Get("Range"))
			}
			resp := textResponse(http.StatusPartialContent, "bcd")
			resp.Header.Set("Content-Type", "video/mp4")
			resp.Header.Set("Content-Length", "3")
			resp.Header.Set("Content-Range", "bytes 1-3/10")
			return resp, nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/path/folder/video.mp4", nil)
	req.Header.Set("Range", "bytes=1-3")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if rec.Body.String() != "bcd" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("Content-Range") != "bytes 1-3/10" {
		t.Fatalf("Content-Range = %q", rec.Header().Get("Content-Range"))
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "video.mp4") {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
}

func TestPathEndpointForwardsResolvedResourceKey(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"folder","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		case 2:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","resourceKey":"resource-key-1"}]}`), nil
		case 3:
			if req.URL.Query().Get("alt") != "media" {
				t.Fatalf("alt = %q", req.URL.Query().Get("alt"))
			}
			if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "file-1/resource-key-1" {
				t.Fatalf("resource key header = %q", got)
			}
			return textResponse(http.StatusOK, "ok"), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/path/folder/video.mp4", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestPathEndpointCarriesFolderResourceKeyDuringResolution(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "" {
				t.Fatalf("first resource key header = %q", got)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"Shared","mimeType":"application/vnd.google-apps.folder","resourceKey":"folder-key-1"}]}`), nil
		case 2:
			if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "folder-1/folder-key-1" {
				t.Fatalf("second resource key header = %q", got)
			}
			query := req.URL.Query().Get("q")
			if !strings.Contains(query, "'folder-1' in parents") ||
				!strings.Contains(query, "name = 'video.mp4'") {
				t.Fatalf("file query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","resourceKey":"file-key-1"}]}`), nil
		case 3:
			if req.URL.Query().Get("alt") != "media" {
				t.Fatalf("alt = %q", req.URL.Query().Get("alt"))
			}
			if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "file-1/file-key-1" {
				t.Fatalf("media resource key header = %q", got)
			}
			return textResponse(http.StatusOK, "ok"), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/path/Shared/video.mp4", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestPathEndpointFollowsShortcutTargetResourceKey(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"folder","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		case 2:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"shortcut-1","name":"video shortcut","mimeType":"application/vnd.google-apps.shortcut","shortcutDetails":{"targetId":"file-1","targetMimeType":"video/mp4","targetResourceKey":"target-resource-key"}}]}`), nil
		case 3:
			if req.URL.Path != "/drive/v3/files/file-1" {
				t.Fatalf("path = %q", req.URL.Path)
			}
			if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "file-1/target-resource-key" {
				t.Fatalf("resource key header = %q", got)
			}
			return textResponse(http.StatusOK, "ok"), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/path/folder/video%20shortcut", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestPathEndpointExportsWorkspaceShortcutTarget(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"folder","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		case 2:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"shortcut-1","name":"Notes shortcut","mimeType":"application/vnd.google-apps.shortcut","shortcutDetails":{"targetId":"doc-1","targetMimeType":"application/vnd.google-apps.document","targetResourceKey":"target-resource-key"}}]}`), nil
		case 3:
			if req.URL.Path != "/drive/v3/files/doc-1/export" {
				t.Fatalf("path = %q", req.URL.Path)
			}
			if req.URL.Query().Get("mimeType") != "application/pdf" {
				t.Fatalf("mimeType = %q", req.URL.Query().Get("mimeType"))
			}
			if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "doc-1/target-resource-key" {
				t.Fatalf("resource key header = %q", got)
			}
			return textResponse(http.StatusOK, "%PDF"), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/path/folder/Notes%20shortcut?format=pdf", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if rec.Body.String() != "%PDF" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "Notes%20shortcut.pdf") {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
}

func TestPathEndpointCachesResolutionButNotMedia(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"folder","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		case 2:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","size":"10"}]}`), nil
		case 3:
			if req.URL.Query().Get("alt") != "media" {
				t.Fatalf("first media alt = %q", req.URL.Query().Get("alt"))
			}
			return textResponse(http.StatusOK, "first"), nil
		case 4:
			if req.URL.Query().Get("alt") != "media" {
				t.Fatalf("second media alt = %q", req.URL.Query().Get("alt"))
			}
			return textResponse(http.StatusOK, "second"), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	for _, wantBody := range []string{"first", "second"} {
		req := httptest.NewRequest(http.MethodGet, "/path/folder/video.mp4", nil)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		if rec.Body.String() != wantBody {
			t.Fatalf("body = %q, want %q", rec.Body.String(), wantBody)
		}
	}
	if calls != 4 {
		t.Fatalf("calls = %d, want 4", calls)
	}
}

func TestPathEndpointExportsWorkspaceFile(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"folder","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		case 2:
			query := req.URL.Query().Get("q")
			if !strings.Contains(query, "'folder-1' in parents") ||
				!strings.Contains(query, "name = 'Notes'") ||
				!strings.Contains(query, "mimeType != '"+drive.FolderMIME+"'") {
				t.Fatalf("file query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{
				"files":[{
					"id":"doc-1",
					"name":"Notes",
					"mimeType":"application/vnd.google-apps.document",
					"exportLinks":{
						"application/pdf":"https://example.invalid/pdf",
						"application/vnd.openxmlformats-officedocument.wordprocessingml.document":"https://example.invalid/docx"
					}
				}]
			}`), nil
		case 3:
			if req.URL.Path != "/drive/v3/files/doc-1/export" {
				t.Fatalf("path = %q", req.URL.Path)
			}
			if req.URL.Query().Get("mimeType") != "application/pdf" {
				t.Fatalf("mimeType = %q", req.URL.Query().Get("mimeType"))
			}
			return textResponse(http.StatusOK, "%PDF"), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/path/folder/Notes?format=pdf", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if rec.Body.String() != "%PDF" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "Notes.pdf") {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
}

func TestPathEndpointRejectsWorkspaceExportRangeBeforeExport(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"folder","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		case 2:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"doc-1","name":"Notes","mimeType":"application/vnd.google-apps.document"}]}`), nil
		default:
			t.Fatalf("unexpected export request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/path/folder/Notes?format=pdf", nil)
	req.Header.Set("Range", "bytes=0-9")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want path resolution only", calls)
	}
	if !strings.Contains(rec.Body.String(), "Range is not supported") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestPathEndpointHeadUsesResolvedMetadata(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Query().Get("alt") != "" {
			t.Fatalf("unexpected media request alt=%q", req.URL.Query().Get("alt"))
		}
		switch calls {
		case 1:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"folder","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		case 2:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","size":"10","modifiedTime":"2026-06-08T12:34:56Z"}]}`), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodHead, "/path/folder/video.mp4", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if rec.Header().Get("Content-Length") != "10" {
		t.Fatalf("Content-Length = %q", rec.Header().Get("Content-Length"))
	}
	if rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("Accept-Ranges = %q", rec.Header().Get("Accept-Ranges"))
	}
	if rec.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Last-Modified") != "Mon, 08 Jun 2026 12:34:56 GMT" {
		t.Fatalf("Last-Modified = %q", rec.Header().Get("Last-Modified"))
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "video.mp4") {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
}

func TestWriteErrorStatusMapping(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		rt         func(req *http.Request) (*http.Response, error)
		wantStatus int
		wantBody   string
	}{
		{
			name:   "path not found",
			target: "/path/missing.mp4",
			rt: func(req *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, `{"files":[]}`), nil
			},
			wantStatus: http.StatusNotFound,
			wantBody:   "not found",
		},
		{
			name:   "ambiguous path",
			target: "/api/resolve?path=/dup",
			rt: func(req *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, `{"files":[{"id":"a","name":"dup","mimeType":"application/vnd.google-apps.folder"},{"id":"b","name":"dup","mimeType":"application/vnd.google-apps.folder"}]}`), nil
			},
			wantStatus: http.StatusConflict,
			wantBody:   "ambiguous",
		},
		{
			name:       "missing id",
			target:     "/api/file",
			wantStatus: http.StatusBadRequest,
			wantBody:   "id is required",
		},
		{
			name:   "list path not found",
			target: "/api/list?path=/missing",
			rt: func(req *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, `{"files":[]}`), nil
			},
			wantStatus: http.StatusNotFound,
			wantBody:   "not found",
		},
		{
			name:   "browse path not found",
			target: "/browse/missing",
			rt: func(req *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, `{"files":[]}`), nil
			},
			wantStatus: http.StatusNotFound,
			wantBody:   "not found",
		},
		{
			name:   "browse list drive error passthrough",
			target: "/browse?folderId=folder-1",
			rt: func(req *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusForbidden, `{"error":"rate"}`), nil
			},
			wantStatus: http.StatusForbidden,
			wantBody:   "Drive API",
		},
		{
			name:   "drive api status passthrough",
			target: "/api/file?id=file-1",
			rt: func(req *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusForbidden, `{"error":"rate"}`), nil
			},
			wantStatus: http.StatusForbidden,
			wantBody:   "Drive API",
		},
		{
			name:       "invalid list type",
			target:     "/api/list?type=bogus",
			wantStatus: http.StatusBadRequest,
			wantBody:   `invalid list type \"bogus\"`,
		},
		{
			name:       "invalid item type",
			target:     "/api/resolve?path=/folder&type=bogus",
			wantStatus: http.StatusBadRequest,
			wantBody:   `invalid item type \"bogus\"`,
		},
		{
			name:   "transport error",
			target: "/api/file?id=file-1",
			rt: func(req *http.Request) (*http.Response, error) {
				return nil, errors.New("connection refused")
			},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "connection refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := tt.rt
			if rt == nil {
				rt = func(req *http.Request) (*http.Response, error) {
					t.Fatalf("unexpected Drive request: %s", req.URL)
					return nil, nil
				}
			}
			app := testServer(rt)
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("body = %q, want substring %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestBrowseJSONNextIncludesFolderResourceKey(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4"}],"nextPageToken":"tok-2"}`), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/browse?folderId=folder-1&resourceKey=folder-key-1&pageSize=1&limit=1", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Next, "folderId=folder-1") ||
		!strings.Contains(body.Next, "resourceKey=folder-key-1") ||
		!strings.Contains(body.Next, "pageToken=tok-2") {
		t.Fatalf("Next = %q", body.Next)
	}
}

func TestPathJSONNextOmitsFolderIDAndResourceKey(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"Shared","mimeType":"application/vnd.google-apps.folder","resourceKey":"folder-key-1"}]}`), nil
		case 2:
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4"}],"nextPageToken":"tok-2"}`), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, nil
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/Shared?format=json&pageSize=1&limit=1", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ResourceKey != "folder-key-1" {
		t.Fatalf("ResourceKey = %q", body.ResourceKey)
	}
	if !strings.HasPrefix(body.Next, "/Shared?") || !strings.Contains(body.Next, "pageToken=tok-2") {
		t.Fatalf("Next = %q", body.Next)
	}
	if strings.Contains(body.Next, "folderId") || strings.Contains(body.Next, "resourceKey") {
		t.Fatalf("Next = %q, want no folderId or resourceKey", body.Next)
	}
}

func TestHealthz(t *testing.T) {
	rt := func(req *http.Request) (*http.Response, error) {
		t.Fatal("unexpected Drive request")
		return nil, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	testServer(rt).Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ok":"true"`) {
		t.Fatalf("body = %q", rec.Body.String())
	}

	authed := testServer(rt, Options{Username: "ben", Password: "secret"})
	rec = httptest.NewRecorder()
	authed.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status with auth enabled = %d, want 401", rec.Code)
	}
}

func TestAPIResolveFolderDefaultsToFolderType(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query().Get("q")
		if !strings.Contains(query, "'root-folder' in parents") ||
			!strings.Contains(query, "name = 'folder'") ||
			!strings.Contains(query, "mimeType = '"+drive.FolderMIME+"'") {
			t.Fatalf("query = %q", query)
		}
		return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"folder","mimeType":"application/vnd.google-apps.folder"}]}`), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/resolve?path=/folder", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body ResolveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ID != "folder-1" || body.FolderID != "folder-1" || !body.IsDir || body.ParentID != "root-folder" {
		t.Fatalf("body = %+v", body)
	}
}

func TestAPIResolveRootPath(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		t.Fatal("unexpected Drive request")
		return nil, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/resolve?path=/", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body ResolveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Path != "/" || body.ID != "root-folder" || body.FolderID != "root-folder" || !body.IsDir {
		t.Fatalf("body = %+v", body)
	}
}

func TestFolderListingJSONTriggers(t *testing.T) {
	tests := []struct {
		name   string
		target string
		accept string
	}{
		{"format query", "/?format=json", ""},
		{"alt query", "/?alt=json", ""},
		{"json flag", "/?json=1", ""},
		{"accept json suffix", "/", "application/ld+json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := testServer(func(req *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, `{"files":[]}`), nil
			})
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
			}
		})
	}
}

func TestFileHeadWithoutNameFallsBackToMetadataName(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"id":"doc-1","name":"Notes","mimeType":"application/vnd.google-apps.document"}`), nil
	})

	req := httptest.NewRequest(http.MethodHead, "/file/doc-1", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Length") != "" {
		t.Fatalf("Content-Length = %q, want empty for sizeless metadata", rec.Header().Get("Content-Length"))
	}
	if rec.Header().Get("Accept-Ranges") != "" {
		t.Fatalf("Accept-Ranges = %q, want empty for workspace file", rec.Header().Get("Accept-Ranges"))
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "Notes") {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
}

func TestExportHeadWithoutNameFallsBackToMetadataName(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("alt") != "" {
			t.Fatalf("unexpected media request alt=%q", req.URL.Query().Get("alt"))
		}
		return jsonResponse(http.StatusOK, `{"id":"doc-1","name":"Notes","mimeType":"application/vnd.google-apps.document","modifiedTime":"2026-06-08T12:34:56Z"}`), nil
	})

	req := httptest.NewRequest(http.MethodHead, "/export/doc-1", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != docxMIME {
		t.Fatalf("Content-Type = %q", got)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "Notes.docx") {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	if rec.Header().Get("Last-Modified") != "Mon, 08 Jun 2026 12:34:56 GMT" {
		t.Fatalf("Last-Modified = %q", rec.Header().Get("Last-Modified"))
	}
}

func TestAPIListUsesExactListCache(t *testing.T) {
	var calls int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(http.StatusOK, `{"files":[{"id":"a","name":"a.mp4"}]}`), nil
	})

	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/api/list?folderId=folder-1&type=files&pageSize=1000&limit=1000", nil)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func BenchmarkAPIListSinglePage(b *testing.B) {
	body := bigListBody()
	client := drive.NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, body), nil
	})})
	client.SetListCacheTTL(0)
	app := New(client, Options{RootFolderID: "root-folder"})
	req := httptest.NewRequest(http.MethodGet, "/api/list?folderId=folder-1&type=files&pageSize=1000&limit=1000", nil)

	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d", rec.Code)
		}
	}
}

func BenchmarkAPIListSinglePageCached(b *testing.B) {
	body := bigListBody()
	app := testServer(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, body), nil
	})
	req := httptest.NewRequest(http.MethodGet, "/api/list?folderId=folder-1&type=files&pageSize=1000&limit=1000", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		b.Fatalf("warm status = %d", rec.Code)
	}

	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d", rec.Code)
		}
	}
}

func BenchmarkFileEndpointRange(b *testing.B) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		resp := textResponse(http.StatusPartialContent, "0123456789")
		resp.Header.Set("Content-Type", "video/mp4")
		resp.Header.Set("Content-Length", "10")
		resp.Header.Set("Content-Range", "bytes 0-9/100")
		return resp, nil
	})
	req := httptest.NewRequest(http.MethodGet, "/file/file-1/video.mp4", nil)
	req.Header.Set("Range", "bytes=0-9")

	b.ReportAllocs()
	for b.Loop() {
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusPartialContent {
			b.Fatalf("status = %d", rec.Code)
		}
	}
}

func testServer(rt func(*http.Request) (*http.Response, error), opts ...Options) *Server {
	o := Options{}
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.RootFolderID == "" {
		o.RootFolderID = "root-folder"
	}
	client := drive.NewClient(testConfig(), &http.Client{Transport: roundTripFunc(rt)})
	return New(client, o)
}

func testConfig() config.Config {
	return config.Config{
		ClientID: "test-client",
		Token:    config.OAuthToken{AccessToken: "test-access"},
	}
}

func bigListBody() string {
	var sb strings.Builder
	sb.WriteString(`{"files":[`)
	for i := range 1000 {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"id":"id-%d","name":"file-%d.mp4","mimeType":"video/mp4","size":"100"}`, i, i)
	}
	sb.WriteString(`]}`)
	return sb.String()
}

const docxMIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(status int, body string) *http.Response {
	resp := textResponse(status, body)
	resp.Header.Set("Content-Type", "application/json")
	return resp
}

func textResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
