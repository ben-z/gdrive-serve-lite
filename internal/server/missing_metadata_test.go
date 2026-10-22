package server

import (
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
