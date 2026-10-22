package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGoogleVidsExportFailsBeforeReadingContent(t *testing.T) {
	const video = `{"id":"vid","name":"Video","mimeType":"application/vnd.google-apps.vid"}`
	const shortcut = `{"id":"shortcut","name":"Alias","mimeType":"application/vnd.google-apps.shortcut","shortcutDetails":{"targetId":"vid","targetMimeType":"application/vnd.google-apps.vid"}}`
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, target := range []string{
			"/export/vid/Video", "/export/vid/Video?format=mp4",
			"/export/vid/Video?mimeType=video%2Fmp4",
			"/path/Video", "/Video", "/export/shortcut/Alias", "/path/Alias",
		} {
			t.Run(method+" "+target, func(t *testing.T) {
				metadata, content := 0, 0
				app := testServer(func(req *http.Request) (*http.Response, error) {
					if req.URL.Query().Get("alt") == "media" || strings.HasSuffix(req.URL.Path, "/export") {
						content++
						return textResponse(http.StatusBadRequest, "fileNotExportable"), nil
					}
					metadata++
					if req.URL.Path == "/drive/v3/files" {
						if strings.Contains(req.URL.Query().Get("q"), "name = 'Alias'") {
							return jsonResponse(http.StatusOK, `{"files":[`+shortcut+`]}`), nil
						}
						return jsonResponse(http.StatusOK, `{"files":[`+video+`]}`), nil
					}
					if req.URL.Path == "/drive/v3/files/shortcut" {
						return jsonResponse(http.StatusOK, shortcut), nil
					}
					return jsonResponse(http.StatusOK, video), nil
				})
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, httptest.NewRequest(method, target, nil))
				if rec.Code != http.StatusBadRequest || (method == http.MethodGet && !strings.Contains(rec.Body.String(), "unsupported export")) {
					t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
				}
				if metadata != 1 || content != 0 || rec.Header().Get("Content-Disposition") != "" {
					t.Fatalf("metadata = %d, content = %d, headers = %v", metadata, content, rec.Header())
				}
			})
		}
	}
}
