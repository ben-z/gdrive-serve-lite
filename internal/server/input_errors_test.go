package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

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
