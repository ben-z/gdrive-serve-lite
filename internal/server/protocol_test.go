package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEncodedBaseSeparatorPreservesDownload(t *testing.T) {
	for _, target := range []string{"/prefix%2Ffile/item-1/name%2Fpart", "/%70refix%2ffile/item-1/name%2Fpart"} {
		t.Run(target, func(t *testing.T) {
			app := testServer(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path != "/drive/v3/files/item-1" || req.URL.Query().Get("alt") != "media" {
					t.Fatalf("upstream URL = %s", req.URL)
				}
				return textResponse(http.StatusOK, "media"), nil
			}, Options{BaseURL: "/prefix"})
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
			if rec.Code != http.StatusOK || rec.Body.String() != "media" || !strings.Contains(rec.Header().Get("Content-Disposition"), "name%2Fpart") {
				t.Fatalf("status = %d, headers = %v, body = %s", rec.Code, rec.Header(), rec.Body)
			}
		})
	}
}

func TestEncodedRoutePrefixesPreserveEscapedNames(t *testing.T) {
	for _, tt := range []struct {
		target string
		folder bool
	}{
		{"/br%6fwse/a%2Fb", true},
		{"/p%61th/a%2Fb", false},
	} {
		t.Run(tt.target, func(t *testing.T) {
			calls := 0
			app := testServer(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					if query := req.URL.Query().Get("q"); !strings.Contains(query, "name = 'a/b'") {
						t.Fatalf("resolve query = %q, want one a/b segment", query)
					}
					if tt.folder {
						return jsonResponse(http.StatusOK, `{"files":[{"id":"folder-1","name":"a/b","mimeType":"application/vnd.google-apps.folder"}]}`), nil
					}
					return jsonResponse(http.StatusOK, `{"files":[{"id":"file-1","name":"a/b","mimeType":"video/mp4"}]}`), nil
				}
				if calls != 2 {
					t.Fatalf("unexpected Drive request %d", calls)
				}
				if tt.folder {
					if query := req.URL.Query().Get("q"); !strings.Contains(query, "'folder-1' in parents") {
						t.Fatalf("list query = %q", query)
					}
					return jsonResponse(http.StatusOK, `{"files":[]}`), nil
				}
				if req.URL.Path != "/drive/v3/files/file-1" || req.URL.Query().Get("alt") != "media" {
					t.Fatalf("download URL = %s", req.URL)
				}
				return textResponse(http.StatusOK, "media"), nil
			})
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.target, nil))
			if rec.Code != http.StatusOK || calls != 2 {
				t.Fatalf("status = %d, calls = %d, body = %s", rec.Code, calls, rec.Body)
			}
		})
	}
}

func TestEncodedReadPrefixesPreserveFileID(t *testing.T) {
	for _, target := range []string{"/f%69le/item-1/name%2Fpart", "/ex%70ort/item-1/name%2Fpart?format=pdf"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(method+" "+target, func(t *testing.T) {
				app := testServer(func(req *http.Request) (*http.Response, error) {
					if req.URL.Path != "/drive/v3/files/item-1" && req.URL.Path != "/drive/v3/files/item-1/export" {
						t.Fatalf("upstream path = %q, want item-1", req.URL.Path)
					}
					if req.URL.Query().Get("fields") != "" {
						return jsonResponse(http.StatusOK, `{"id":"item-1","name":"name/part","mimeType":"application/vnd.google-apps.document"}`), nil
					}
					if method == http.MethodHead {
						t.Fatal("HEAD streamed media")
					}
					return textResponse(http.StatusOK, "media"), nil
				})
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, httptest.NewRequest(method, target, nil))
				if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), "name%2Fpart") {
					t.Fatalf("status = %d, disposition = %q, body = %s", rec.Code, rec.Header().Get("Content-Disposition"), rec.Body)
				}
			})
		}
	}
}

func TestFolderRepresentationsVaryByAccept(t *testing.T) {
	for _, target := range []string{"/", "/browse/"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, accept := range []string{"text/html", "application/json"} {
				t.Run(method+" "+target+" "+accept, func(t *testing.T) {
					app := testServer(func(req *http.Request) (*http.Response, error) {
						if method == http.MethodHead {
							t.Fatal("root folder HEAD listed Drive")
						}
						return jsonResponse(http.StatusOK, `{"files":[]}`), nil
					}, Options{AllowOrigin: "*"})
					req := httptest.NewRequest(method, target, nil)
					req.Header.Set("Accept", accept)
					rec := httptest.NewRecorder()
					app.Handler().ServeHTTP(rec, req)
					if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), accept) {
						t.Fatalf("status = %d, content type = %q", rec.Code, rec.Header().Get("Content-Type"))
					}
					vary := strings.Join(rec.Header().Values("Vary"), ", ")
					if !strings.Contains(vary, "Accept") || !strings.Contains(vary, "Origin") {
						t.Fatalf("Vary = %q, want Accept and Origin", vary)
					}
				})
			}
		}
	}
}
