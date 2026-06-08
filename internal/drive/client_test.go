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

func TestNewHTTPClientPreservesRawDriveResponses(t *testing.T) {
	client := NewHTTPClient(7 * time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout != 7*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %s", transport.ResponseHeaderTimeout)
	}
	if !transport.DisableCompression {
		t.Fatal("DisableCompression = false, want true")
	}
}

func TestNewClientUsesRawHTTPClient(t *testing.T) {
	client := NewClient(testConfig())
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.httpClient.Transport)
	}
	if !transport.DisableCompression {
		t.Fatal("DisableCompression = false, want true")
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

func TestListChildrenFoldersIncludesFolderShortcuts(t *testing.T) {
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			query := req.URL.Query().Get("q")
			if !strings.Contains(query, "mimeType = '"+FolderMIME+"'") ||
				!strings.Contains(query, "shortcutDetails.targetMimeType = '"+FolderMIME+"'") {
				t.Fatalf("query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"shortcut-1","name":"Shared","mimeType":"application/vnd.google-apps.shortcut","shortcutDetails":{"targetId":"folder-1","targetMimeType":"application/vnd.google-apps.folder"}}]}`), nil
		}),
	})

	list, err := client.ListChildren(context.Background(), ListOptions{
		FolderID: "root-folder",
		Type:     "folders",
		PageSize: 1000,
		Limit:    1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Files) != 1 || list.Files[0].ID != "shortcut-1" {
		t.Fatalf("files = %+v", list.Files)
	}
}

func TestListChildrenFilesExcludesFolderShortcuts(t *testing.T) {
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			query := req.URL.Query().Get("q")
			if !strings.Contains(query, "mimeType != '"+FolderMIME+"'") ||
				!strings.Contains(query, "shortcutDetails.targetMimeType != '"+FolderMIME+"'") {
				t.Fatalf("query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4"}]}`), nil
		}),
	})

	list, err := client.ListChildren(context.Background(), ListOptions{
		FolderID: "root-folder",
		Type:     "files",
		PageSize: 1000,
		Limit:    1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Files) != 1 || list.Files[0].ID != "file-1" {
		t.Fatalf("files = %+v", list.Files)
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

func TestDownloadReturnsRangeErrorResponse(t *testing.T) {
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("Range") != "bytes=999-1000" {
				t.Fatalf("Range = %q", req.Header.Get("Range"))
			}
			resp := textResponse(http.StatusRequestedRangeNotSatisfiable, "range outside file")
			resp.Header.Set("Content-Range", "bytes */100")
			return resp, nil
		}),
	})

	resp, err := client.Download(context.Background(), http.MethodGet, "file-1", "bytes=999-1000")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Range") != "bytes */100" {
		t.Fatalf("Content-Range = %q", resp.Header.Get("Content-Range"))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "range outside file" {
		t.Fatalf("body = %q", body)
	}
}

func TestDownloadForwardsConditionalHeaders(t *testing.T) {
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("If-None-Match") != `"etag-1"` {
				t.Fatalf("If-None-Match = %q", req.Header.Get("If-None-Match"))
			}
			if req.Header.Get("If-Modified-Since") != "Mon, 08 Jun 2026 07:00:00 GMT" {
				t.Fatalf("If-Modified-Since = %q", req.Header.Get("If-Modified-Since"))
			}
			if req.Header.Get("Cookie") != "" {
				t.Fatalf("Cookie should not be forwarded: %q", req.Header.Get("Cookie"))
			}
			return textResponse(http.StatusNotModified, ""), nil
		}),
	})

	headers := http.Header{}
	headers.Set("If-None-Match", `"etag-1"`)
	headers.Set("If-Modified-Since", "Mon, 08 Jun 2026 07:00:00 GMT")
	headers.Set("Cookie", "local=only")
	resp, err := client.DownloadWithResourceKeyAndHeaders(context.Background(), http.MethodGet, "file-1", headers, "")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("status = %d", resp.StatusCode)
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

func TestListChildrenForwardsFolderResourceKey(t *testing.T) {
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "folder-1/folder-key-1" {
				t.Fatalf("resource key header = %q", got)
			}
			query := req.URL.Query().Get("q")
			if !strings.Contains(query, "'folder-1' in parents") {
				t.Fatalf("query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4"}]}`), nil
		}),
	})

	list, err := client.ListChildren(context.Background(), ListOptions{
		FolderID:    "folder-1",
		ResourceKey: "folder-key-1",
		PageSize:    1000,
		Limit:       1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(list.Files))
	}
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

func TestGetFileRefreshesExpiredTokenBeforeRequest(t *testing.T) {
	var calls int
	cfg := testConfig()
	cfg.Token.AccessToken = "stale-access"
	cfg.Token.RefreshToken = "test-refresh"
	cfg.Token.Expiry = time.Now().Add(-time.Hour)
	client := NewClientWithHTTPClient(cfg, &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			switch calls {
			case 1:
				if req.Method != http.MethodPost || req.URL.Host != "oauth2.googleapis.com" || req.URL.Path != "/token" {
					t.Fatalf("refresh request = %s %s", req.Method, req.URL.String())
				}
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				form := string(body)
				if !strings.Contains(form, "client_id=test-client") ||
					!strings.Contains(form, "refresh_token=test-refresh") ||
					!strings.Contains(form, "grant_type=refresh_token") {
					t.Fatalf("refresh form = %q", form)
				}
				return jsonResponse(http.StatusOK, `{"access_token":"fresh-access","token_type":"Bearer","expires_in":3600}`), nil
			case 2:
				if req.URL.Path != "/drive/v3/files/file-1" {
					t.Fatalf("path = %q", req.URL.Path)
				}
				if req.Header.Get("Authorization") != "Bearer fresh-access" {
					t.Fatalf("Authorization = %q", req.Header.Get("Authorization"))
				}
				return jsonResponse(http.StatusOK, `{"id":"file-1","name":"video.mp4","mimeType":"video/mp4"}`), nil
			default:
				t.Fatalf("unexpected request %d", calls)
				return nil, nil
			}
		}),
	})

	file, err := client.GetFile(context.Background(), "file-1")
	if err != nil {
		t.Fatal(err)
	}
	if file.ID != "file-1" {
		t.Fatalf("file = %+v", file)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestDownloadRefreshesAndRetriesUnauthorized(t *testing.T) {
	var calls int
	cfg := testConfig()
	cfg.Token.AccessToken = "stale-access"
	cfg.Token.RefreshToken = "test-refresh"
	client := NewClientWithHTTPClient(cfg, &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			switch calls {
			case 1:
				if req.URL.Path != "/drive/v3/files/file-1" {
					t.Fatalf("path = %q", req.URL.Path)
				}
				if req.Header.Get("Authorization") != "Bearer stale-access" {
					t.Fatalf("Authorization = %q", req.Header.Get("Authorization"))
				}
				return jsonResponse(http.StatusUnauthorized, `{"error":"expired"}`), nil
			case 2:
				if req.Method != http.MethodPost || req.URL.Host != "oauth2.googleapis.com" || req.URL.Path != "/token" {
					t.Fatalf("refresh request = %s %s", req.Method, req.URL.String())
				}
				return jsonResponse(http.StatusOK, `{"access_token":"fresh-access","token_type":"Bearer","expires_in":3600}`), nil
			case 3:
				if req.URL.Path != "/drive/v3/files/file-1" {
					t.Fatalf("path = %q", req.URL.Path)
				}
				if req.Header.Get("Authorization") != "Bearer fresh-access" {
					t.Fatalf("Authorization = %q", req.Header.Get("Authorization"))
				}
				return textResponse(http.StatusOK, "media-bytes"), nil
			default:
				t.Fatalf("unexpected request %d", calls)
				return nil, nil
			}
		}),
	})

	resp, err := client.DownloadWithResourceKey(context.Background(), http.MethodGet, "file-1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "media-bytes" {
		t.Fatalf("body = %q", body)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
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
					!strings.Contains(query, "name = 'RecentClips'") ||
					!strings.Contains(query, "mimeType = '"+FolderMIME+"'") {
					t.Fatalf("folder query = %q", query)
				}
				return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"RecentClips","mimeType":"application/vnd.google-apps.folder"}]}`), nil
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

	item, err := client.ResolveItemPath(context.Background(), "/RecentClips/video.mp4", "root-folder", "file")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if item.Path != "/RecentClips/video.mp4" || item.ParentID != "folder-1" || item.File.ID != "file-1" {
		t.Fatalf("item = %+v", item)
	}
}

func TestResolveItemPathCarriesParentResourceKey(t *testing.T) {
	var calls int
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
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
					t.Fatalf("query = %q", query)
				}
				return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4"}]}`), nil
			default:
				t.Fatalf("unexpected request %d", calls)
				return nil, nil
			}
		}),
	})

	item, err := client.ResolveItemPath(context.Background(), "/Shared/video.mp4", "root-folder", "file")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if item.File.ID != "file-1" || item.ParentID != "folder-1" {
		t.Fatalf("item = %+v", item)
	}
}

func TestResolveItemPathUsesRootResourceKey(t *testing.T) {
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "root-folder/root-key-1" {
				t.Fatalf("resource key header = %q", got)
			}
			query := req.URL.Query().Get("q")
			if !strings.Contains(query, "'root-folder' in parents") ||
				!strings.Contains(query, "name = 'video.mp4'") {
				t.Fatalf("query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4"}]}`), nil
		}),
	})

	item, err := client.ResolveItemPathWithResourceKey(context.Background(), "/video.mp4", "root-folder", "root-key-1", "file")
	if err != nil {
		t.Fatal(err)
	}
	if item.File.ID != "file-1" || item.ParentID != "root-folder" {
		t.Fatalf("item = %+v", item)
	}
}

func TestResolveItemPathCarriesShortcutParentResourceKey(t *testing.T) {
	var calls int
	client := NewClientWithHTTPClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			switch calls {
			case 1:
				return jsonResponse(http.StatusOK, `{"files":[{"id":"shortcut-1","name":"Shared shortcut","mimeType":"application/vnd.google-apps.shortcut","shortcutDetails":{"targetId":"folder-1","targetMimeType":"application/vnd.google-apps.folder","targetResourceKey":"target-folder-key"}}]}`), nil
			case 2:
				if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "folder-1/target-folder-key" {
					t.Fatalf("second resource key header = %q", got)
				}
				query := req.URL.Query().Get("q")
				if !strings.Contains(query, "'folder-1' in parents") {
					t.Fatalf("query = %q", query)
				}
				return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4"}]}`), nil
			default:
				t.Fatalf("unexpected request %d", calls)
				return nil, nil
			}
		}),
	})

	item, err := client.ResolveItemPath(context.Background(), "/Shared%20shortcut/video.mp4", "root-folder", "file")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if item.File.ID != "file-1" || item.ParentID != "folder-1" {
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
				return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"RecentClips","mimeType":"application/vnd.google-apps.folder"}]}`), nil
			case 2:
				return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","size":"10"}]}`), nil
			default:
				t.Fatalf("unexpected request %d", calls)
				return nil, nil
			}
		}),
	})

	for range 2 {
		item, err := client.ResolveItemPath(context.Background(), "/RecentClips/video.mp4", "root-folder", "file")
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
				return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"RecentClips","mimeType":"application/vnd.google-apps.folder"}]}`), nil
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","size":"10"}]}`), nil
		}),
	})
	client.SetMetadataCacheTTL(0)

	for range 2 {
		if _, err := client.ResolveItemPath(context.Background(), "/RecentClips/video.mp4", "root-folder", "file"); err != nil {
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
