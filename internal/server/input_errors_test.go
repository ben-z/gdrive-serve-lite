package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"gdrive-serve-lite/internal/drive"
)

func TestAPIListValidatesOptionsBeforeResolvingPath(t *testing.T) {
	for _, prefix := range []string{"", "/prefix"} {
		for _, query := range []string{
			"pageSize=bad", "limit=1.5",
			"pageSize=999999999999999999999999999", "limit=999999999999999999999999999",
			"type=bogus", "type=any",
		} {
			t.Run(prefix+query, func(t *testing.T) {
				app := testServer(func(*http.Request) (*http.Response, error) {
					t.Fatal("invalid list options reached Drive")
					return nil, nil
				}, Options{BaseURL: prefix})
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, prefix+"/api/list?path=/Shared&"+query, nil))
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
				}
			})
		}
	}
}

func TestAPIListPreservesTypeDefaultsAndAliases(t *testing.T) {
	for _, listType := range []string{"", "all", " FILES ", "dirs"} {
		t.Run(listType, func(t *testing.T) {
			calls := 0
			app := testServer(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-id","name":"Shared","mimeType":"application/vnd.google-apps.folder","resourceKey":"actual-key"}]}`), nil
				}
				if calls != 2 || !strings.Contains(req.URL.Query().Get("q"), "'folder-id' in parents") || req.Header.Get("X-Goog-Drive-Resource-Keys") != "folder-id/actual-key" {
					t.Fatalf("unexpected list request: %s, headers = %v", req.URL, req.Header)
				}
				query := req.URL.Query().Get("q")
				if (listType == "" || listType == "all") && strings.Contains(query, "mimeType") {
					t.Fatalf("default listing gained a type filter: %q", query)
				}
				if listType == " FILES " && !strings.Contains(query, "mimeType != '"+drive.FolderMIME+"'") || listType == "dirs" && !strings.Contains(query, "mimeType = '"+drive.FolderMIME+"'") {
					t.Fatalf("type filter missing: %q", query)
				}
				return jsonResponse(http.StatusOK, `{"files":[]}`), nil
			})
			query := url.Values{"path": {"/Shared"}, "type": {listType}, "resourceKey": {"stale-key"}}
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/list?"+query.Encode(), nil))
			if rec.Code != http.StatusOK || calls != 2 {
				t.Fatalf("status = %d, calls = %d, body = %s", rec.Code, calls, rec.Body.String())
			}
		})
	}
}

func TestInvalidSemanticPathReturnsBadRequestBeforeDrive(t *testing.T) {
	for _, route := range []string{"/api/resolve", "/api/list", "/browse"} {
		for _, path := range []string{"/%", "/parent/%ZZ"} {
			t.Run(route+path, func(t *testing.T) {
				app := testServer(func(*http.Request) (*http.Response, error) {
					t.Fatal("invalid semantic path reached Drive")
					return nil, nil
				}, Options{BaseURL: "/prefix"})
				query := url.Values{"path": {path}}
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/prefix"+route+"?"+query.Encode(), nil))
				if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid URL escape") {
					t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
				}
			})
		}
	}
}

func TestBlankExportSelectionReturnsBadRequest(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, query := range []url.Values{
			{"format": {" "}},
			{"mimeType": {" \t"}, "format": {"pdf"}},
		} {
			t.Run(method+"/"+query.Encode(), func(t *testing.T) {
				calls := 0
				app := testServer(func(req *http.Request) (*http.Response, error) {
					calls++
					if calls != 1 || req.URL.Query().Get("fields") == "" {
						t.Fatal("invalid export selection fetched content")
					}
					return jsonResponse(http.StatusOK, `{"id":"doc","name":"Notes","mimeType":"application/vnd.google-apps.document"}`), nil
				})
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, httptest.NewRequest(method, "/export/doc/Notes?"+query.Encode(), nil))
				if rec.Code != http.StatusBadRequest || calls != 1 || !strings.Contains(rec.Body.String(), "export format or MIME type is required") {
					t.Fatalf("status = %d, calls = %d, body = %s", rec.Code, calls, rec.Body.String())
				}
			})
		}
	}
}
