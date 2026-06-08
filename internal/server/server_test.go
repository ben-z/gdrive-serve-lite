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

func TestBasicAuthRejectsUnauthorizedRequests(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		t.Fatal("Drive transport should not be reached")
		return nil, nil
	})
	app.SetBasicAuth("ben", "secret")

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
	app.SetBasicAuth("ben", "secret")

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
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "video.mp4") {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
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
		return jsonResponse(http.StatusOK, `{"id":"file-1","name":"actual.mp4","mimeType":"video/mp4","size":"10"}`), nil
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
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "request-name.mp4") {
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
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "Meeting%20Notes.docx") {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
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
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","size":"10"}]}`), nil
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
