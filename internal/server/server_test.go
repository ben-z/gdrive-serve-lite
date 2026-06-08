package server

import (
	"encoding/json"
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
			if q.Get("pageToken") != "tok-2" {
				t.Fatalf("second pageToken = %q", q.Get("pageToken"))
			}
			if q.Get("pageSize") != "1" {
				t.Fatalf("second pageSize = %q", q.Get("pageSize"))
			}
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

func TestAPIListDefaultsToRootFolder(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query().Get("q")
		if !strings.Contains(query, "'root-folder' in parents") {
			t.Fatalf("query = %q", query)
		}
		return jsonResponse(http.StatusOK, `{"files":[{"id":"a","name":"a.mp4"}]}`), nil
	})

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
	if body.FolderID != "root-folder" {
		t.Fatalf("FolderID = %q", body.FolderID)
	}
	if body.Count != 1 {
		t.Fatalf("Count = %d", body.Count)
	}
}

func TestAPIListDefaultsToRootFolderResourceKey(t *testing.T) {
	cfg := config.Config{
		ClientID:     "test-client",
		RootFolderID: "root-folder",
		Token: config.OAuthToken{
			AccessToken: "test-access",
			TokenType:   "Bearer",
		},
	}
	client := drive.NewClientWithHTTPClient(cfg, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "root-folder/root-key-1" {
			t.Fatalf("resource key header = %q", got)
		}
		query := req.URL.Query().Get("q")
		if !strings.Contains(query, "'root-folder' in parents") {
			t.Fatalf("query = %q", query)
		}
		return jsonResponse(http.StatusOK, `{"files":[{"id":"a","name":"a.mp4"}]}`), nil
	})})
	app := NewWithRootResourceKey(client, cfg.RootFolderID, "root-key-1")

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
	if body.FolderID != "root-folder" || body.ResourceKey != "root-key-1" {
		t.Fatalf("body = %+v", body)
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
		var body ListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Count != 1 {
			t.Fatalf("Count = %d", body.Count)
		}
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
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

func TestBasicAuthRejectsUnauthorizedRequests(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		t.Fatal("Drive transport should not be reached")
		return nil, nil
	})
	app.SetBasicAuth("test-user", "test-password")

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
	})
	app.SetBasicAuth("test-user", "test-password")

	req := httptest.NewRequest(http.MethodGet, "/api/list", nil)
	req.SetBasicAuth("test-user", "test-password")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestRootPathListsConfiguredRoot(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query().Get("q")
		if !strings.Contains(query, "'root-folder' in parents") {
			t.Fatalf("query = %q", query)
		}
		return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"folder","mimeType":"application/vnd.google-apps.folder"}]}`), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "folder/") {
		t.Fatalf("body = %s", rec.Body.String())
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
		return jsonResponse(http.StatusOK, `{
			"files":[
				{"id":"folder-1","name":"Shared","mimeType":"application/vnd.google-apps.folder","resourceKey":"folder-key-1"},
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
		"/browse/Shared?folderId=folder-1&amp;resourceKey=folder-key-1",
		"/file/file-1/video.mp4?resourceKey=file-key-1",
		"/export/doc-1/Notes.docx?resourceKey=doc-key-1",
		"/file/file-2/shortcut.mp4?resourceKey=target-key-1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
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
			if req.URL.Query().Get("mimeType") != drive.OpenXMLDocumentMIME {
				t.Fatalf("mimeType = %q", req.URL.Query().Get("mimeType"))
			}
			resp := textResponse(http.StatusOK, "docx-bytes")
			resp.Header.Set("Content-Type", drive.OpenXMLDocumentMIME)
			resp.Header.Set("Content-Length", "10")
			resp.Header.Set("Content-Disposition", "attachment; filename=drive-doc.docx")
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
	if rec.Header().Get("Content-Type") != drive.OpenXMLDocumentMIME {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Content-Disposition") != "attachment; filename=drive-doc.docx" {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
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
		if calls != 1 {
			t.Fatalf("unexpected export request %d", calls)
		}
		return jsonResponse(http.StatusOK, `{"id":"doc-1","name":"Notes","mimeType":"application/vnd.google-apps.document"}`), nil
	})

	req := httptest.NewRequest(http.MethodGet, "/export/doc-1/Notes", nil)
	req.Header.Set("Range", "bytes=0-9")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want metadata only", calls)
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

func BenchmarkAPIListSinglePage(b *testing.B) {
	body := `{"files":[`
	for i := range 1000 {
		if i > 0 {
			body += ","
		}
		body += fmt.Sprintf(`{"id":"id-%d","name":"file-%d.mp4","mimeType":"video/mp4","size":"100"}`, i, i)
	}
	body += `]}`
	cfg := config.Config{
		ClientID:     "test-client",
		RootFolderID: "root-folder",
		Token: config.OAuthToken{
			AccessToken: "test-access",
			TokenType:   "Bearer",
		},
	}
	client := drive.NewClientWithHTTPClient(cfg, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, body), nil
	})})
	client.SetListCacheTTL(0)
	app := New(client, cfg.RootFolderID)
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
	body := `{"files":[`
	for i := range 1000 {
		if i > 0 {
			body += ","
		}
		body += fmt.Sprintf(`{"id":"id-%d","name":"file-%d.mp4","mimeType":"video/mp4","size":"100"}`, i, i)
	}
	body += `]}`
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

func testServer(rt func(*http.Request) (*http.Response, error)) *Server {
	cfg := config.Config{
		ClientID:     "test-client",
		RootFolderID: "root-folder",
		Token: config.OAuthToken{
			AccessToken: "test-access",
			TokenType:   "Bearer",
		},
	}
	client := drive.NewClientWithHTTPClient(cfg, &http.Client{Transport: roundTripFunc(rt)})
	return New(client, cfg.RootFolderID)
}

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
