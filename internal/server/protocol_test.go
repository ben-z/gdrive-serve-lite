package server

import (
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCanonicalRedirectsKeepBasePath(t *testing.T) {
	for _, tt := range []struct {
		target string
		want   string
	}{
		{"/prefix//file/item-1/name", "/prefix/file/item-1/name"},
		{"/prefix/a/../file/item-1/name", "/prefix/file/item-1/name"},
		{"/prefix/../file/item-1/name", "/prefix/file/item-1/name"},
		{"/prefix/file", "/prefix/file/"},
		{"/%70refix//file/item-1/name%2Fpart", "/prefix/file/item-1/name%2Fpart"},
		{"/prefix%2F/file/item-1/name", "/prefix/file/item-1/name"},
		{"/prefix/a/../file/item-1/%2E%2E", "/prefix/file/item-1/%2E%2E"},
		{"/prefix//file/item-1/name?token=opaque%2fvalue+here", "/prefix/file/item-1/name?token=opaque%2fvalue+here"},
	} {
		t.Run(tt.target, func(t *testing.T) {
			app := testServer(func(req *http.Request) (*http.Response, error) {
				t.Fatalf("canonical redirect made a Drive request: %s", req.URL)
				return nil, nil
			}, Options{BaseURL: "/prefix"})
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.target, nil))
			if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != tt.want {
				t.Fatalf("status = %d, Location = %q, want %d and %q", rec.Code, rec.Header().Get("Location"), http.StatusTemporaryRedirect, tt.want)
			}
			if !strings.Contains(rec.Body.String(), `href="`+html.EscapeString(tt.want)+`"`) {
				t.Fatalf("redirect body = %s, want link to %q", rec.Body, tt.want)
			}
		})
	}
}

func TestCanonicalRedirectsEscapeBasePath(t *testing.T) {
	for _, tt := range []struct {
		base string
		want string
	}{
		{"/prefix with spaces", "/prefix%20with%20spaces"},
		{"/préfix/路径", "/pr%C3%A9fix/%E8%B7%AF%E5%BE%84"},
		{"/prefix%23part", "/prefix%23part"},
		{"/prefix%3Fpart", "/prefix%3Fpart"},
		{"/prefix%25part", "/prefix%25part"},
	} {
		t.Run(tt.base, func(t *testing.T) {
			app := testServer(func(req *http.Request) (*http.Response, error) {
				t.Fatalf("canonical redirect made a Drive request: %s", req.URL)
				return nil, nil
			}, Options{BaseURL: tt.base})
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.want+"//file/item-1/name?token=a%2fb+c", nil))
			want := tt.want + "/file/item-1/name?token=a%2fb+c"
			if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != want {
				t.Fatalf("status = %d, Location = %q, want %d and %q", rec.Code, rec.Header().Get("Location"), http.StatusTemporaryRedirect, want)
			}
			if !strings.Contains(rec.Body.String(), `href="`+html.EscapeString(want)+`"`) {
				t.Fatalf("redirect body = %s, want link to %q", rec.Body, want)
			}
		})
	}
}

func TestCanonicalRedirectsPreserveAuthAndCORS(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("canonical redirect made a Drive request: %s", req.URL)
		return nil, nil
	}, Options{BaseURL: "/prefix", Username: "user", Password: "pass", AllowOrigin: "https://example.com"})
	for _, authorized := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodHead, "/prefix//file/item-1/name", nil)
		req.Header.Set("Origin", "https://example.com")
		if authorized {
			req.SetBasicAuth("user", "pass")
		}
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		want := http.StatusUnauthorized
		if authorized {
			want = http.StatusTemporaryRedirect
			if rec.Header().Get("Location") != "/prefix/file/item-1/name" {
				t.Fatalf("Location = %q", rec.Header().Get("Location"))
			}
		} else if rec.Header().Get("Location") != "" {
			t.Fatalf("unauthorized response redirected to %q", rec.Header().Get("Location"))
		}
		if rec.Code != want || rec.Header().Get("Access-Control-Allow-Origin") != "https://example.com" || rec.Header().Get("Vary") != "Origin" {
			t.Fatalf("authorized = %v, status = %d, headers = %v", authorized, rec.Code, rec.Header())
		}
		if authorized && rec.Body.Len() != 0 {
			t.Fatalf("HEAD redirect body = %s", rec.Body)
		}
	}
}

func TestBasePathRootRemainsCanonical(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("root HEAD made a Drive request: %s", req.URL)
		return nil, nil
	}, Options{BaseURL: "/prefix"})
	for _, target := range []string{"/prefix", "/%70refix", "/prefix/", "/prefix%2F"} {
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodHead, target, nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" || rec.Body.Len() != 0 {
			t.Fatalf("%s: status = %d, headers = %v, body = %s", target, rec.Code, rec.Header(), rec.Body)
		}
	}
}

func TestBasePathPreservesEscapedDotNames(t *testing.T) {
	for _, tt := range []struct{ target, name string }{
		{"/prefix/path/%2e", "."},
		{"/prefix/path/%2e%2e", ".."},
	} {
		t.Run(tt.target, func(t *testing.T) {
			app := testServer(func(req *http.Request) (*http.Response, error) {
				if req.URL.Query().Get("q") != "" {
					if query := req.URL.Query().Get("q"); !strings.Contains(query, "name = '"+tt.name+"'") {
						t.Fatalf("path lookup query = %q, want name %q", query, tt.name)
					}
					return jsonResponse(http.StatusOK, `{"files":[{"id":"dot-1","mimeType":"video/mp4"}]}`), nil
				}
				return textResponse(http.StatusOK, "media"), nil
			}, Options{BaseURL: "/prefix"})
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.target, nil))
			if rec.Code != http.StatusOK || rec.Body.String() != "media" || rec.Header().Get("Location") != "" {
				t.Fatalf("status = %d, headers = %v, body = %s", rec.Code, rec.Header(), rec.Body)
			}
		})
	}
}

func TestExportHeadIgnoresRange(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("fields") == "" {
			t.Fatalf("HEAD streamed export: %s", req.URL)
		}
		return jsonResponse(http.StatusOK, `{"id":"doc-1","name":"Notes","mimeType":"application/vnd.google-apps.document"}`), nil
	})
	req := httptest.NewRequest(http.MethodHead, "/export/doc-1/Notes?format=pdf", nil)
	req.Header.Set("Range", "bytes=0-9")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/pdf" || rec.Body.Len() != 0 {
		t.Fatalf("status = %d, headers = %v, body = %s", rec.Code, rec.Header(), rec.Body)
	}
}

func TestFolderRepresentationRespectsZeroQuality(t *testing.T) {
	for _, tt := range []struct {
		accept string
		query  string
		want   string
	}{
		{"application/json;q=0", "", "text/html"},
		{"application/json;q=0.000, text/html;q=1", "", "text/html"},
		{"application/problem+json;q=0", "", "text/html"},
		{"APPLICATION/JSON; Q=0", "", "text/html"},
		{"application/json; charset=utf-8; q=0", "", "text/html"},
		{"application/json;q=0.5", "", "application/json"},
		{"application/json;q=0, application/problem+json;q=0.5", "", "application/json"},
		{"application/json;q=0", "?format=json", "application/json"},
	} {
		t.Run(tt.accept+tt.query, func(t *testing.T) {
			app := testServer(func(req *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, `{"files":[]}`), nil
			})
			req := httptest.NewRequest(http.MethodGet, "/"+tt.query, nil)
			req.Header.Set("Accept", tt.accept)
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), tt.want) {
				t.Fatalf("status = %d, Content-Type = %q, want %q", rec.Code, rec.Header().Get("Content-Type"), tt.want)
			}
		})
	}
}

func TestFolderAcceptMediaRanges(t *testing.T) {
	for _, tt := range []struct {
		accept string
		query  string
		status int
		want   string
	}{
		{`application/json;profile="a,b"`, "", http.StatusOK, "application/json"},
		{`application/json;profile="a\",b"`, "", http.StatusOK, "application/json"},
		{`application/json;profile="a\\", text/html`, "", http.StatusOK, "application/json"},
		{"", "", http.StatusOK, "text/html"},
		{", , ", "", http.StatusOK, "text/html"},
		{", application/json;q=0.5, ,", "", http.StatusOK, "application/json"},
		{", application/json;q=0, , text/html,", "", http.StatusOK, "text/html"},
		{"application/json;q=0.", "", http.StatusOK, "text/html"},
		{"application/json;q=1.000", "", http.StatusOK, "application/json"},
		{"not a range", "", http.StatusBadRequest, "application/json"},
		{"application", "", http.StatusBadRequest, "application/json"},
		{"*/json", "", http.StatusBadRequest, "application/json"},
		{`application/json;profile="a,b`, "", http.StatusBadRequest, "application/json"},
		{"application/json;q=bogus", "", http.StatusBadRequest, "application/json"},
		{"application/json;q=-0.1", "", http.StatusBadRequest, "application/json"},
		{"application/json;q=1.1", "", http.StatusBadRequest, "application/json"},
		{"application/json;q=NaN", "", http.StatusBadRequest, "application/json"},
		{"application/json;q=Inf", "", http.StatusBadRequest, "application/json"},
		{"application/json;q=0.0001", "", http.StatusBadRequest, "application/json"},
		{"application/json;q=+0.5", "", http.StatusBadRequest, "application/json"},
		{"application/json;q=.5", "", http.StatusBadRequest, "application/json"},
		{"application/json;q=1e0", "", http.StatusBadRequest, "application/json"},
		{"application/json, text/html;q=bogus", "", http.StatusBadRequest, "application/json"},
		{"not a range", "?format=json", http.StatusOK, "application/json"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(method+" "+tt.accept+tt.query, func(t *testing.T) {
				calls := 0
				app := testServer(func(req *http.Request) (*http.Response, error) {
					calls++
					return jsonResponse(http.StatusOK, `{"files":[]}`), nil
				})
				req := httptest.NewRequest(method, "/"+tt.query, nil)
				req.Header.Set("Accept", tt.accept)
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, req)
				if rec.Code != tt.status || !strings.HasPrefix(rec.Header().Get("Content-Type"), tt.want) {
					t.Fatalf("status = %d, Content-Type = %q, body = %s; want %d %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body, tt.status, tt.want)
				}
				wantCalls := 0
				if method == http.MethodGet && tt.status == http.StatusOK {
					wantCalls = 1
				}
				if calls != wantCalls {
					t.Fatalf("Drive calls = %d, want %d", calls, wantCalls)
				}
				if tt.status == http.StatusBadRequest && !strings.Contains(rec.Body.String(), "Accept") {
					t.Fatalf("missing Accept error detail: %s", rec.Body)
				}
			})
		}
	}
}

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
