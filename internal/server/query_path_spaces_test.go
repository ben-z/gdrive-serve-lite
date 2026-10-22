package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"gdrive-serve-lite/internal/drive"
)

func TestQueryPathsPreserveNameSpaces(t *testing.T) {
	for _, path := range []struct{ input, name string }{
		{" leading", " leading"}, {"/ leading", " leading"},
		{"trailing ", "trailing "}, {"/trailing ", "trailing "},
		{"   ", "   "}, {"/   ", "   "},
		{"A%2FB", "A/B"}, {"A%252FB", "A%2FB"}, {"trailing%20", "trailing "},
	} {
		for _, route := range []struct {
			endpoint, itemType string
			list               bool
		}{
			{"/api/resolve", "folder", false},
			{"/api/resolve", "file", false},
			{"/api/list", "folder", true},
			{"/browse/", "folder", true},
		} {
			t.Run(fmt.Sprintf("%s %s %q", route.endpoint, route.itemType, path.input), func(t *testing.T) {
				lookups, lists := 0, 0
				app := testServer(func(req *http.Request) (*http.Response, error) {
					q := req.URL.Query().Get("q")
					if strings.Contains(q, "name = '") {
						lookups++
						if !strings.Contains(q, "'root-folder' in parents") || !strings.Contains(q, "name = '"+path.name+"'") {
							t.Errorf("name lookup = %q", q)
							return jsonResponse(http.StatusOK, `{"files":[]}`), nil
						}
						mime := drive.FolderMIME
						if route.itemType == "file" {
							mime = "text/plain"
							if !strings.Contains(q, "mimeType != '"+drive.FolderMIME+"'") {
								t.Errorf("file lookup = %q", q)
							}
						}
						body, err := json.Marshal(drive.FileList{Files: []drive.File{{ID: "target", Name: path.name, MIMEType: mime}}})
						if err != nil {
							t.Fatal(err)
						}
						return jsonResponse(http.StatusOK, string(body)), nil
					}
					lists++
					if !strings.Contains(q, "'target' in parents") {
						t.Errorf("listed wrong folder: %q", q)
					}
					return jsonResponse(http.StatusOK, `{"files":[{"id":"child","name":"child","mimeType":"text/plain"}]}`), nil
				})
				query := url.Values{"path": {path.input}, "type": {route.itemType}}
				req := httptest.NewRequest(http.MethodGet, route.endpoint+"?"+query.Encode(), nil)
				req.Header.Set("Accept", "application/json")
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
				}
				if route.list {
					var body ListResponse
					if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					if body.FolderID != "target" || body.Count != 1 || lists != 1 {
						t.Fatalf("listing = %+v, list requests = %d", body, lists)
					}
				} else {
					var body ResolveResponse
					if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					if body.ID != "target" || body.Path != "/"+path.name || body.File.Name != path.name || body.IsDir != (route.itemType == "folder") || lists != 0 {
						t.Fatalf("resolved = %+v, list requests = %d", body, lists)
					}
				}
				if lookups != 1 {
					t.Fatalf("name lookups = %d, want 1", lookups)
				}
			})
		}
	}
}

func TestMissingAllSpaceQueryPathDoesNotUseRoot(t *testing.T) {
	for _, endpoint := range []string{"/api/resolve", "/api/list", "/browse/"} {
		t.Run(endpoint, func(t *testing.T) {
			lookups := 0
			app := testServer(func(req *http.Request) (*http.Response, error) {
				lookups++
				if q := req.URL.Query().Get("q"); !strings.Contains(q, "name = '   '") {
					t.Errorf("expected all-space name lookup, got %q", q)
				}
				return jsonResponse(http.StatusOK, `{"files":[]}`), nil
			})
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, endpoint+"?path=+++", nil))
			if rec.Code != http.StatusNotFound || lookups != 1 {
				t.Fatalf("status = %d, requests = %d, body = %s", rec.Code, lookups, rec.Body.String())
			}
		})
	}
}
