package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"gdrive-serve-lite/internal/config"
)

func TestQuoteEscapesDriveQueryString(t *testing.T) {
	got := quote(`quinn's paper\essay`)
	want := `'quinn\'s paper\\essay'`
	if got != want {
		t.Fatalf("quote() = %q, want %q", got, want)
	}
}

func TestNormalizePageSize(t *testing.T) {
	tests := []struct {
		in   int
		want int
	}{
		{0, 1000},
		{-1, 1000},
		{1, 1},
		{100, 100},
		{5000, 1000},
	}
	for _, tt := range tests {
		if got := normalizePageSize(tt.in); got != tt.want {
			t.Fatalf("normalizePageSize(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeLimit(t *testing.T) {
	tests := []struct {
		limit    int
		pageSize int
		want     int
	}{
		{0, 1000, 1000},
		{-1, 500, 500},
		{3, 1000, 3},
		{20000, 1000, 10000},
	}
	for _, tt := range tests {
		if got := normalizeLimit(tt.limit, tt.pageSize); got != tt.want {
			t.Fatalf("normalizeLimit(%d, %d) = %d, want %d", tt.limit, tt.pageSize, got, tt.want)
		}
	}
}

func TestListChildrenAggregatesPagesToLimit(t *testing.T) {
	var calls int
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.URL.Path != "/drive/v3/files" {
				t.Fatalf("path = %q", req.URL.Path)
			}
			if got := req.Header.Get("Authorization"); got != "Bearer test-access" {
				t.Fatalf("Authorization = %q", got)
			}
			q := req.URL.Query()
			switch calls {
			case 1:
				if q.Get("pageToken") != "" {
					t.Fatalf("first pageToken = %q", q.Get("pageToken"))
				}
				if q.Get("pageSize") != "2" {
					t.Fatalf("first pageSize = %q", q.Get("pageSize"))
				}
				if !strings.Contains(q.Get("q"), "'folder-1' in parents") || !strings.Contains(q.Get("q"), "mimeType != ") {
					t.Fatalf("query = %q", q.Get("q"))
				}
				return jsonResponse(http.StatusOK, `{"files":[{"id":"a","name":"a.mp4"},{"id":"b","name":"b.mp4"}],"nextPageToken":"tok-2"}`), nil
			case 2:
				if q.Get("pageToken") != "tok-2" {
					t.Fatalf("second pageToken = %q", q.Get("pageToken"))
				}
				if q.Get("pageSize") != "1" {
					t.Fatalf("second pageSize = %q", q.Get("pageSize"))
				}
				return jsonResponse(http.StatusOK, `{"files":[{"id":"c","name":"c.mp4"}],"nextPageToken":"tok-3","incompleteSearch":true}`), nil
			default:
				t.Fatalf("unexpected request %d", calls)
				return nil, nil
			}
		}),
	})

	list, err := client.ListChildren(context.Background(), ListOptions{
		FolderID: "folder-1",
		Type:     "files",
		PageSize: 2,
		Limit:    3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if len(list.Files) != 3 {
		t.Fatalf("files = %d, want 3", len(list.Files))
	}
	if list.NextPageToken != "tok-3" {
		t.Fatalf("NextPageToken = %q", list.NextPageToken)
	}
	if !list.IncompleteSearch {
		t.Fatal("expected incomplete search to propagate")
	}
}

func TestDownloadForwardsRange(t *testing.T) {
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet {
				t.Fatalf("method = %q", req.Method)
			}
			if req.URL.Path != "/drive/v3/files/file-1" {
				t.Fatalf("path = %q", req.URL.Path)
			}
			if req.URL.Query().Get("alt") != "media" {
				t.Fatalf("alt = %q", req.URL.Query().Get("alt"))
			}
			if req.Header.Get("Range") != "bytes=10-19" {
				t.Fatalf("Range = %q", req.Header.Get("Range"))
			}
			resp := textResponse(http.StatusPartialContent, "0123456789")
			resp.Header.Set("Content-Range", "bytes 10-19/100")
			return resp, nil
		}),
	})

	resp, err := client.Download(context.Background(), http.MethodGet, "file-1", "bytes=10-19")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "0123456789" {
		t.Fatalf("body = %q", body)
	}
	if resp.Header.Get("Content-Range") != "bytes 10-19/100" {
		t.Fatalf("Content-Range = %q", resp.Header.Get("Content-Range"))
	}
}

func TestDownloadForwardsResourceKey(t *testing.T) {
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "file-1/resource-key-1" {
				t.Fatalf("resource key header = %q", got)
			}
			return textResponse(http.StatusOK, "ok"), nil
		}),
	})

	resp, err := client.DownloadWithResourceKey(context.Background(), http.MethodGet, "file-1", "", "resource-key-1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestGetFileForwardsResourceKey(t *testing.T) {
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "file-1/resource-key-1" {
				t.Fatalf("resource key header = %q", got)
			}
			return jsonResponse(http.StatusOK, `{"id":"file-1","name":"video.mp4","mimeType":"video/mp4"}`), nil
		}),
	})

	file, err := client.GetFileWithResourceKey(context.Background(), "file-1", "resource-key-1")
	if err != nil {
		t.Fatal(err)
	}
	if file.ID != "file-1" {
		t.Fatalf("file = %+v", file)
	}
}

func TestExportForwardsResourceKey(t *testing.T) {
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "doc-1/resource-key-1" {
				t.Fatalf("resource key header = %q", got)
			}
			return textResponse(http.StatusOK, "%PDF"), nil
		}),
	})

	resp, err := client.ExportWithResourceKey(context.Background(), "doc-1", "application/pdf", "resource-key-1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestListChildrenRejectsRepeatedPageToken(t *testing.T) {
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{"files":[],"nextPageToken":"same-token"}`), nil
		}),
	})

	_, err := client.ListChildren(context.Background(), ListOptions{
		FolderID: "folder-1",
		PageSize: 1,
		Limit:    2,
	})
	if err == nil || !strings.Contains(err.Error(), "repeated page token") {
		t.Fatalf("error = %v, want repeated page token error", err)
	}
}

func TestListChildrenUsesExactListCache(t *testing.T) {
	var calls int
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return jsonResponse(http.StatusOK, `{"files":[{"id":"a","name":"original.mp4","mimeType":"video/mp4"}],"nextPageToken":"tok-2"}`), nil
		}),
	})
	opts := ListOptions{FolderID: "folder-1", Type: "files", PageSize: 1000, Limit: 1}

	first, err := client.ListChildren(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	first.Files[0].Name = "mutated.mp4"

	second, err := client.ListChildren(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if second.Files[0].Name != "original.mp4" {
		t.Fatalf("cached file name = %q, want original.mp4", second.Files[0].Name)
	}
	if second.NextPageToken != "tok-2" {
		t.Fatalf("NextPageToken = %q", second.NextPageToken)
	}
}

func TestListCacheKeysPaginationOptions(t *testing.T) {
	var calls int
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-`+fmt.Sprint(calls)+`","name":"video.mp4"}]}`), nil
		}),
	})

	options := []ListOptions{
		{FolderID: "folder-1", Type: "files", PageSize: 1000, Limit: 1000},
		{FolderID: "folder-1", Type: "files", PageSize: 1000, Limit: 1000, PageToken: "tok-2"},
		{FolderID: "folder-1", Type: "files", PageSize: 100, Limit: 100},
	}
	for _, opts := range options {
		if _, err := client.ListChildren(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.ListChildren(context.Background(), options[0]); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 exact query cache misses", calls)
	}
}

func TestListCacheCanBeDisabled(t *testing.T) {
	var calls int
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return jsonResponse(http.StatusOK, `{"files":[{"id":"a","name":"a.mp4"}]}`), nil
		}),
	})
	client.SetListCacheTTL(0)
	opts := ListOptions{FolderID: "folder-1", Type: "files", PageSize: 1000, Limit: 1000}

	for range 2 {
		if _, err := client.ListChildren(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestResolveItemPathFile(t *testing.T) {
	var calls int
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.URL.Path != "/drive/v3/files" {
				t.Fatalf("path = %q", req.URL.Path)
			}
			q := req.URL.Query()
			if q.Get("pageSize") != "2" {
				t.Fatalf("pageSize = %q", q.Get("pageSize"))
			}
			if !strings.Contains(q.Get("fields"), "md5Checksum") {
				t.Fatalf("fields = %q", q.Get("fields"))
			}
			switch calls {
			case 1:
				query := q.Get("q")
				if !strings.Contains(query, "'root-folder' in parents") ||
					!strings.Contains(query, "name = 'Clips'") ||
					!strings.Contains(query, "mimeType = '"+FolderMIME+"'") {
					t.Fatalf("folder query = %q", query)
				}
				return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"Clips","mimeType":"application/vnd.google-apps.folder"}]}`), nil
			case 2:
				query := q.Get("q")
				if !strings.Contains(query, "'folder-1' in parents") ||
					!strings.Contains(query, "name = 'video.mp4'") ||
					!strings.Contains(query, "mimeType != '"+FolderMIME+"'") {
					t.Fatalf("file query = %q", query)
				}
				return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","size":"10"}]}`), nil
			default:
				t.Fatalf("unexpected request %d", calls)
				return nil, nil
			}
		}),
	})

	item, err := client.ResolveItemPath(context.Background(), "/Clips/video.mp4", "root-folder", "file")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if item.Path != "/Clips/video.mp4" || item.ParentID != "folder-1" || item.File.ID != "file-1" {
		t.Fatalf("item = %+v", item)
	}
}

func TestResolveItemPathAmbiguous(t *testing.T) {
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{"files":[{"id":"a","name":"duplicate","mimeType":"application/vnd.google-apps.folder"},{"id":"b","name":"duplicate","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		}),
	})

	_, err := client.ResolveItemPath(context.Background(), "/duplicate", "root-folder", "folder")
	var ambiguous *AmbiguousPathError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("error = %v, want AmbiguousPathError", err)
	}
	if ambiguous.Segment != "duplicate" || ambiguous.Parent != "root-folder" || ambiguous.Count != 2 {
		t.Fatalf("ambiguous = %+v", ambiguous)
	}
}

func TestResolveItemPathUsesMetadataCache(t *testing.T) {
	var calls int
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			switch calls {
			case 1:
				return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"Clips","mimeType":"application/vnd.google-apps.folder"}]}`), nil
			case 2:
				return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","size":"10"}]}`), nil
			default:
				t.Fatalf("unexpected request %d", calls)
				return nil, nil
			}
		}),
	})

	for range 2 {
		item, err := client.ResolveItemPath(context.Background(), "/Clips/video.mp4", "root-folder", "file")
		if err != nil {
			t.Fatal(err)
		}
		if item.File.ID != "file-1" {
			t.Fatalf("item = %+v", item)
		}
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestMetadataCacheCanBeDisabled(t *testing.T) {
	var calls int
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if calls%2 == 1 {
				return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"Clips","mimeType":"application/vnd.google-apps.folder"}]}`), nil
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","size":"10"}]}`), nil
		}),
	})
	client.SetMetadataCacheTTL(0)

	for range 2 {
		if _, err := client.ResolveItemPath(context.Background(), "/Clips/video.mp4", "root-folder", "file"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 4 {
		t.Fatalf("calls = %d, want 4", calls)
	}
}

func TestGetFileUsesMetadataCacheUntilExpiry(t *testing.T) {
	var calls int
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return jsonResponse(http.StatusOK, `{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","size":"10"}`), nil
		}),
	})

	for range 2 {
		file, err := client.GetFile(context.Background(), "file-1")
		if err != nil {
			t.Fatal(err)
		}
		if file.Name != "video.mp4" {
			t.Fatalf("file = %+v", file)
		}
	}
	if calls != 1 {
		t.Fatalf("calls before expiry = %d, want 1", calls)
	}

	client.SetMetadataCacheTTL(time.Millisecond)
	if _, err := client.GetFile(context.Background(), "file-1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := client.GetFile(context.Background(), "file-1"); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls after expiry = %d, want 3", calls)
	}
}

func TestAPIErrorPreservesStatus(t *testing.T) {
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusNotFound, `{"error":{"message":"missing"}}`), nil
		}),
	})

	_, err := client.GetFile(context.Background(), "missing")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want APIError", err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("StatusCode = %d", apiErr.StatusCode)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testConfig() config.Config {
	return config.Config{
		ClientID: "test-client",
		Token: config.OAuthToken{
			AccessToken: "test-access",
			TokenType:   "Bearer",
		},
	}
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
