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

func BenchmarkAPIListSinglePage(b *testing.B) {
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
