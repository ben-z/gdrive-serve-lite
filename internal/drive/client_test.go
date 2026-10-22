package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gdrive-serve-lite/internal/config"
)

func TestResolveItemPathEscapesDriveQueryStrings(t *testing.T) {
	client := NewClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			query := req.URL.Query().Get("q")
			if !strings.Contains(query, `name = 'quinn\'s paper\\essay'`) {
				t.Fatalf("query = %q", query)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"quinn's paper\\essay","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		}),
	})

	item, err := client.ResolveItemPath(context.Background(), `/quinn's paper\essay`, "root-folder", "", "folder")
	if err != nil {
		t.Fatal(err)
	}
	if item.File.ID != "folder-1" {
		t.Fatalf("item = %+v", item)
	}
}

func TestListChildrenClampsPageSizeSentToDrive(t *testing.T) {
	client := NewClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.URL.Query().Get("pageSize"); got != "1000" {
				t.Fatalf("pageSize = %q, want clamped 1000", got)
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"a","name":"a.mp4"}]}`), nil
		}),
	})

	if _, err := client.ListChildren(context.Background(), ListOptions{
		FolderID: "folder-1",
		PageSize: 5000,
		Limit:    1000,
	}); err != nil {
		t.Fatal(err)
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

func TestListChildrenAggregatesPagesToLimit(t *testing.T) {
	var calls int
	client := NewClient(testConfig(), &http.Client{
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

func TestDownloadForwardsConditionalHeaders(t *testing.T) {
	client := NewClient(testConfig(), &http.Client{
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
	resp, err := client.Download(context.Background(), "file-1", headers, "")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestGetFileRefreshesExpiredTokenBeforeRequest(t *testing.T) {
	var calls int
	cfg := testConfig()
	cfg.Token.AccessToken = "stale-access"
	cfg.Token.RefreshToken = "test-refresh"
	cfg.Token.Expiry = time.Now().Add(-time.Hour)
	client := NewClient(cfg, &http.Client{
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

	file, err := client.GetFile(context.Background(), "file-1", "")
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
	client := NewClient(cfg, &http.Client{
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

	resp, err := client.Download(context.Background(), "file-1", nil, "")
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
	for _, tc := range []struct {
		name, pageToken string
		limit           int
		responses       []string
	}{
		{"empty pages", "", 2, []string{
			`{"files":[],"nextPageToken":"same-token"}`,
			`{"files":[],"nextPageToken":"same-token"}`,
		}},
		{"full page repeats request token", "same-token", 1, []string{
			`{"files":[{"id":"a"}],"nextPageToken":"same-token"}`,
		}},
		{"full page cycles to earlier token", "first", 2, []string{
			`{"files":[{"id":"a"}],"nextPageToken":"second"}`,
			`{"files":[{"id":"b"}],"nextPageToken":"first"}`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := NewClient(testConfig(), &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if calls >= len(tc.responses) {
						t.Fatal("requested another page after a repeated token")
					}
					response := tc.responses[calls]
					calls++
					return jsonResponse(http.StatusOK, response), nil
				}),
			})

			_, err := client.ListChildren(context.Background(), ListOptions{
				FolderID:  "folder-1",
				PageSize:  1,
				Limit:     tc.limit,
				PageToken: tc.pageToken,
			})
			if err == nil || !strings.Contains(err.Error(), "repeated page token") {
				t.Fatalf("error = %v, want repeated page token error", err)
			}
		})
	}
}

func TestListChildrenUsesExactListCache(t *testing.T) {
	var calls int
	client := NewClient(testConfig(), &http.Client{
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
	client := NewClient(testConfig(), &http.Client{
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
	client := NewClient(testConfig(), &http.Client{
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
	client := NewClient(testConfig(), &http.Client{
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

	item, err := client.ResolveItemPath(context.Background(), "/Clips/video.mp4", "root-folder", "", "file")
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

func TestResolveItemPathUsesRootResourceKey(t *testing.T) {
	client := NewClient(testConfig(), &http.Client{
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

	item, err := client.ResolveItemPath(context.Background(), "/video.mp4", "root-folder", "root-key-1", "file")
	if err != nil {
		t.Fatal(err)
	}
	if item.File.ID != "file-1" || item.ParentID != "root-folder" {
		t.Fatalf("item = %+v", item)
	}
}

func TestResolveItemPathCarriesShortcutParentResourceKey(t *testing.T) {
	var calls int
	client := NewClient(testConfig(), &http.Client{
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

	item, err := client.ResolveItemPath(context.Background(), "/Shared%20shortcut/video.mp4", "root-folder", "", "file")
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
	client := NewClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{"files":[{"id":"a","name":"duplicate","mimeType":"application/vnd.google-apps.folder"},{"id":"b","name":"duplicate","mimeType":"application/vnd.google-apps.folder"}]}`), nil
		}),
	})

	_, err := client.ResolveItemPath(context.Background(), "/duplicate", "root-folder", "", "folder")
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
	client := NewClient(testConfig(), &http.Client{
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
		item, err := client.ResolveItemPath(context.Background(), "/Clips/video.mp4", "root-folder", "", "file")
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
	client := NewClient(testConfig(), &http.Client{
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
		if _, err := client.ResolveItemPath(context.Background(), "/Clips/video.mp4", "root-folder", "", "file"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 4 {
		t.Fatalf("calls = %d, want 4", calls)
	}
}

func TestGetFileUsesMetadataCacheUntilExpiry(t *testing.T) {
	var calls int
	client := NewClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return jsonResponse(http.StatusOK, `{"id":"file-1","name":"video.mp4","mimeType":"video/mp4","size":"10"}`), nil
		}),
	})

	for range 2 {
		file, err := client.GetFile(context.Background(), "file-1", "")
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
	if _, err := client.GetFile(context.Background(), "file-1", ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := client.GetFile(context.Background(), "file-1", ""); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls after expiry = %d, want 3", calls)
	}
}

func TestAPIErrorPreservesStatus(t *testing.T) {
	client := NewClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusNotFound, `{"error":{"message":"missing"}}`), nil
		}),
	})

	_, err := client.GetFile(context.Background(), "missing", "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want APIError", err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("StatusCode = %d", apiErr.StatusCode)
	}
}

func TestResourceKeyHeaderForwarding(t *testing.T) {
	tests := []struct {
		name string
		want string
		do   func(ctx context.Context, c *Client) error
	}{
		{
			name: "download",
			want: "file-1/resource-key-1",
			do: func(ctx context.Context, c *Client) error {
				res, err := c.Download(ctx, "file-1", nil, "resource-key-1")
				if err == nil {
					res.Body.Close()
				}
				return err
			},
		},
		{
			name: "get file",
			want: "file-1/resource-key-1",
			do: func(ctx context.Context, c *Client) error {
				_, err := c.GetFile(ctx, "file-1", "resource-key-1")
				return err
			},
		},
		{
			name: "export",
			want: "doc-1/resource-key-1",
			do: func(ctx context.Context, c *Client) error {
				res, err := c.Export(ctx, "doc-1", "application/pdf", "resource-key-1")
				if err == nil {
					res.Body.Close()
				}
				return err
			},
		},
		{
			name: "list children",
			want: "folder-1/folder-key-1",
			do: func(ctx context.Context, c *Client) error {
				_, err := c.ListChildren(ctx, ListOptions{FolderID: "folder-1", ResourceKey: "folder-key-1", PageSize: 1000, Limit: 1000})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(testConfig(), &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != tt.want {
						t.Fatalf("resource key header = %q, want %q", got, tt.want)
					}
					return jsonResponse(http.StatusOK, `{"id":"x","name":"x"}`), nil
				}),
			})
			if err := tt.do(context.Background(), client); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestListChildrenShortcutTypeFilters(t *testing.T) {
	tests := []struct {
		listType  string
		wantQuery []string
		fixture   string
		wantID    string
	}{
		{
			listType: "folders",
			wantQuery: []string{
				"mimeType = '" + FolderMIME + "'",
				"shortcutDetails.targetMimeType = '" + FolderMIME + "'",
			},
			fixture: `{"files":[{"id":"shortcut-1","name":"Shared","mimeType":"application/vnd.google-apps.shortcut","shortcutDetails":{"targetId":"folder-1","targetMimeType":"application/vnd.google-apps.folder"}}]}`,
			wantID:  "shortcut-1",
		},
		{
			listType: "files",
			wantQuery: []string{
				"mimeType != '" + FolderMIME + "'",
				"shortcutDetails.targetMimeType != '" + FolderMIME + "'",
			},
			fixture: `{"files":[{"id":"file-1","name":"video.mp4","mimeType":"video/mp4"}]}`,
			wantID:  "file-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.listType, func(t *testing.T) {
			client := NewClient(testConfig(), &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					query := req.URL.Query().Get("q")
					for _, want := range tt.wantQuery {
						if !strings.Contains(query, want) {
							t.Fatalf("query = %q, missing %q", query, want)
						}
					}
					return jsonResponse(http.StatusOK, tt.fixture), nil
				}),
			})
			list, err := client.ListChildren(context.Background(), ListOptions{
				FolderID: "root-folder",
				Type:     tt.listType,
				PageSize: 1000,
				Limit:    1000,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(list.Files) != 1 || list.Files[0].ID != tt.wantID {
				t.Fatalf("files = %+v", list.Files)
			}
		})
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

func TestResolveItemPathFollowsPartialPages(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(fmt.Sprintf("duplicate=%t", duplicate), func(t *testing.T) {
			var calls int
			client := NewClient(testConfig(), &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					q := req.URL.Query()
					if !strings.Contains(q.Get("fields"), "nextPageToken") {
						t.Fatal("path lookup did not request pagination tokens")
					}
					if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "root-folder/root-key" {
						t.Fatalf("resource keys = %q", got)
					}
					switch q.Get("pageToken") {
					case "":
						return jsonResponse(http.StatusOK, `{"files":[],"nextPageToken":"partial"}`), nil
					case "partial":
						return jsonResponse(http.StatusOK, `{"files":[{"id":"first","name":"clip.mp4","mimeType":"video/mp4"}],"nextPageToken":"last"}`), nil
					case "last":
						if q.Get("pageSize") != "1" {
							t.Fatalf("last page size = %q, want 1", q.Get("pageSize"))
						}
						if duplicate {
							return jsonResponse(http.StatusOK, `{"files":[{"id":"second","name":"clip.mp4","mimeType":"video/mp4"}]}`), nil
						}
						return jsonResponse(http.StatusOK, `{"files":[]}`), nil
					default:
						t.Fatalf("unexpected page token = %q", q.Get("pageToken"))
						return nil, nil
					}
				}),
			})
			item, err := client.ResolveItemPath(context.Background(), "/clip.mp4", "root-folder", "root-key", "file")
			if duplicate {
				var ambiguous *AmbiguousPathError
				if !errors.As(err, &ambiguous) || ambiguous.Count != 2 {
					t.Fatalf("error = %v, want two ambiguous matches", err)
				}
			} else if err != nil || item.File.ID != "first" {
				t.Fatalf("item = %+v, error = %v", item, err)
			}
			if calls != 3 {
				t.Fatalf("calls = %d, want 3", calls)
			}
		})
	}
}

func TestResolveItemPathRejectsUnreliableSearch(t *testing.T) {
	for _, tc := range []struct {
		name, response, want string
	}{
		{"repeated token", `{"files":[],"nextPageToken":"same"}`, "repeated page token"},
		{"incomplete", `{"files":[{"id":"a","name":"clip.mp4","mimeType":"video/mp4"}],"incompleteSearch":true}`, "incomplete search"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient(testConfig(), &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					return jsonResponse(http.StatusOK, tc.response), nil
				}),
			})
			_, err := client.ResolveItemPath(context.Background(), "/clip.mp4", "root-folder", "", "file")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestConcurrentUnauthorizedRequestsShareRefresh(t *testing.T) {
	const requests = 8
	var staleRequests, refreshes atomic.Int32
	allStale := make(chan struct{})
	cfg := testConfig()
	cfg.Token.RefreshToken = "refresh-token"
	client := NewClient(cfg, &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host == "oauth2.googleapis.com" {
				refreshes.Add(1)
				return jsonResponse(http.StatusOK, `{"access_token":"fresh-token","expires_in":3600}`), nil
			}
			if req.Header.Get("Authorization") == "Bearer test-access" {
				if staleRequests.Add(1) == requests {
					close(allStale)
				}
				<-allStale
				return textResponse(http.StatusUnauthorized, "expired"), nil
			}
			return textResponse(http.StatusOK, "media"), nil
		}),
	})
	errors := make(chan error, requests)
	for range requests {
		go func() {
			resp, err := client.Download(context.Background(), "clip", nil, "")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					err = fmt.Errorf("status = %d", resp.StatusCode)
				}
			}
			errors <- err
		}()
	}
	for range requests {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if got := refreshes.Load(); got != 1 {
		t.Fatalf("refreshes = %d, want 1", got)
	}
}

func TestTokenRefreshRejectsInvalidExpiry(t *testing.T) {
	cfg := testConfig()
	cfg.Token.AccessToken = ""
	cfg.Token.RefreshToken = "refresh-token"
	client := NewClient(cfg, &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{"access_token":"fresh-token","expires_in":0}`), nil
		}),
	})
	_, err := client.GetFile(context.Background(), "clip", "")
	if err == nil || !strings.Contains(err.Error(), "invalid expires_in") {
		t.Fatalf("error = %v, want invalid expires_in", err)
	}
	if client.token.AccessToken != "" {
		t.Fatal("invalid refresh response replaced the access token")
	}
}

func TestCacheWritesRemoveExpiredEntries(t *testing.T) {
	cache := newTTLCache(time.Hour, func(value string) string { return value })
	cache.entries["expired"] = ttlEntry[string]{value: "old", expiresAt: time.Now().Add(-time.Second)}
	cache.put("fresh", "new")
	if len(cache.entries) != 1 {
		t.Fatalf("retained %d entries, want only the fresh entry", len(cache.entries))
	}
	if value, ok := cache.get("fresh"); !ok || value != "new" {
		t.Fatalf("fresh cache lookup = %q, %t", value, ok)
	}
}

func TestMetadataCacheClonesNestedValues(t *testing.T) {
	client := NewClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{"id":"doc","name":"Notes","mimeType":"application/vnd.google-apps.document","exportLinks":{"application/pdf":"original"},"shortcutDetails":{"targetId":"original"}}`), nil
		}),
	})
	first, err := client.GetFile(context.Background(), "doc", "")
	if err != nil {
		t.Fatal(err)
	}
	first.ExportLinks["application/pdf"] = "changed"
	first.ExportFormats[0].Format = "changed"
	first.DefaultExport.Format = "changed"
	first.ShortcutDetails.TargetID = "changed"
	second, err := client.GetFile(context.Background(), "doc", "")
	if err != nil {
		t.Fatal(err)
	}
	if second.ExportLinks["application/pdf"] != "original" || second.ExportFormats[0].Format != "pdf" ||
		second.DefaultExport.Format != "pdf" || second.ShortcutDetails.TargetID != "original" {
		t.Fatalf("cached metadata was mutated: %+v", second)
	}
}

func TestUpstreamErrorBodyReadFailuresPropagate(t *testing.T) {
	readErr := errors.New("truncated upstream response")
	for _, refresh := range []bool{false, true} {
		t.Run(fmt.Sprintf("refresh=%t", refresh), func(t *testing.T) {
			cfg := testConfig()
			if refresh {
				cfg.Token.AccessToken = ""
				cfg.Token.RefreshToken = "refresh-token"
			}
			client := NewClient(cfg, &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					resp := textResponse(http.StatusServiceUnavailable, "")
					resp.Body = io.NopCloser(failingReader{readErr})
					return resp, nil
				}),
			})
			_, err := client.GetFile(context.Background(), "clip", "")
			if !errors.Is(err, readErr) {
				t.Fatalf("error = %v, want upstream body read failure", err)
			}
		})
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }
