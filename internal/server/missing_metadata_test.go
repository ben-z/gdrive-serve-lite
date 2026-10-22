package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMissingMetadataIDFailsBeforeServingFile(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"name":"Report","mimeType":"application/vnd.google-apps.document"}`} {
		for _, route := range []struct{ method, target string }{
			{http.MethodGet, "/api/file?id=requested"},
			{http.MethodGet, "/api/export-formats?id=requested"},
			{http.MethodHead, "/file/requested/Report"},
			{http.MethodHead, "/export/requested/Report?format=pdf"},
			{http.MethodGet, "/export/requested/Report?format=pdf"},
		} {
			t.Run(route.method+" "+route.target+" "+body, func(t *testing.T) {
				metadata, content := 0, 0
				app := testServer(func(req *http.Request) (*http.Response, error) {
					if req.URL.Path == "/drive/v3/files/requested" && req.URL.Query().Get("alt") != "media" {
						metadata++
						return jsonResponse(http.StatusOK, body), nil
					}
					content++
					return textResponse(http.StatusOK, "unexpected content"), nil
				})
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, httptest.NewRequest(route.method, route.target, nil))
				if rec.Code != http.StatusInternalServerError || (route.method != http.MethodHead && !strings.Contains(rec.Body.String(), "missing file ID")) {
					t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
				}
				if metadata != 1 || content != 0 || rec.Header().Get("Content-Disposition") != "" {
					t.Fatalf("metadata = %d, content = %d, headers = %v", metadata, content, rec.Header())
				}
			})
		}
	}
}

func TestMissingLookupIDFailsBeforeServingFile(t *testing.T) {
	for _, tt := range []struct{ method, target, body string }{
		{http.MethodGet, "/api/resolve?path=/Alias&type=any", `{}`},
		{http.MethodGet, "/api/resolve?path=/Alias&type=file", `null`},
		{http.MethodGet, "/api/resolve?path=/Alias", `{"mimeType":"application/vnd.google-apps.folder"}`},
		{http.MethodHead, "/path/Alias", `{"mimeType":"application/vnd.google-apps.document"}`},
		{http.MethodGet, "/api/resolve?path=/Alias&type=file", `{"mimeType":"application/vnd.google-apps.shortcut","shortcutDetails":{"targetId":"target","targetMimeType":"text/plain"}}`},
	} {
		t.Run(tt.method+" "+tt.target+" "+tt.body, func(t *testing.T) {
			lookups, content := 0, 0
			app := testServer(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/drive/v3/files" && strings.Contains(req.URL.Query().Get("q"), "name = 'Alias'") {
					lookups++
					return jsonResponse(http.StatusOK, `{"files":[`+tt.body+`]}`), nil
				}
				content++
				return textResponse(http.StatusOK, "unexpected content"), nil
			})
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, httptest.NewRequest(tt.method, tt.target, nil))
			if rec.Code != http.StatusInternalServerError || (tt.method != http.MethodHead && !strings.Contains(rec.Body.String(), "missing file ID")) {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if lookups != 1 || content != 0 || rec.Header().Get("Content-Disposition") != "" {
				t.Fatalf("lookups = %d, content = %d, headers = %v", lookups, content, rec.Header())
			}
		})
	}
}

func TestMissingLookupIDDoesNotPoisonFolderCache(t *testing.T) {
	lookups, lists := 0, 0
	app := testServer(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/drive/v3/files" {
			t.Fatalf("unexpected request: %s", req.URL)
		}
		q := req.URL.Query().Get("q")
		if strings.Contains(q, "name = 'Alias'") {
			lookups++
			if req.Header.Get("X-Goog-Drive-Resource-Keys") != "root-folder/root-key" {
				t.Fatalf("lookup resource key = %q", req.Header.Get("X-Goog-Drive-Resource-Keys"))
			}
			if lookups == 1 {
				return jsonResponse(http.StatusOK, `{"files":[{"mimeType":"application/vnd.google-apps.folder"}]}`), nil
			}
			return jsonResponse(http.StatusOK, `{"files":[{"id":"canonical-folder","mimeType":"application/vnd.google-apps.folder","resourceKey":"actual-key"}]}`), nil
		}
		lists++
		if !strings.Contains(q, "'canonical-folder' in parents") || req.Header.Get("X-Goog-Drive-Resource-Keys") != "canonical-folder/actual-key" {
			t.Fatalf("folder list query = %q, resource key = %q", q, req.Header.Get("X-Goog-Drive-Resource-Keys"))
		}
		return jsonResponse(http.StatusOK, `{"files":[{"id":"child","name":"Child"}]}`), nil
	}, Options{RootResourceKey: "root-key"})
	for i := range 3 {
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/list?path=/Alias", nil))
		if i == 0 {
			if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "missing file ID") {
				t.Errorf("invalid response: status = %d, body = %s", rec.Code, rec.Body.String())
			}
			continue
		}
		var body ListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK || body.FolderID != "canonical-folder" || body.ResourceKey != "actual-key" || len(body.Files) != 1 || body.Files[0].ID != "child" {
			t.Fatalf("later valid response: status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}
	if lookups != 2 || lists != 1 {
		t.Fatalf("lookups = %d, lists = %d, want invalid lookup then cached valid folder and list", lookups, lists)
	}
}
