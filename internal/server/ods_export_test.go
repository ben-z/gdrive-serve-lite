package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"gdrive-serve-lite/internal/drive"
)

func TestODSExportsUseDocumentedMIMEType(t *testing.T) {
	const canonical = "application/vnd.oasis.opendocument.spreadsheet"
	const legacy = "application/x-vnd.oasis.opendocument.spreadsheet"
	for _, tt := range []struct {
		name, mimeType, query, filename string
		exportLinks                     bool
	}{
		{"named canonical", canonical, "format=ods", "Budget", true},
		{"default canonical", canonical, "", "Budget", true},
		{"raw canonical", canonical, "mimeType=" + url.QueryEscape(canonical), "Budget.ods", true},
		{"raw legacy", legacy, "mimeType=" + url.QueryEscape(legacy), "Budget.ods", true},
		{"named without links", canonical, "format=ods", "Budget", false},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(tt.name+" "+method, func(t *testing.T) {
				file := drive.File{ID: "sheet", Name: "Budget", MIMEType: "application/vnd.google-apps.spreadsheet"}
				if tt.exportLinks {
					file.ExportLinks = map[string]string{tt.mimeType: "https://example.invalid/ods"}
				}
				body, err := json.Marshal(file)
				if err != nil {
					t.Fatal(err)
				}
				metadata, exports := 0, 0
				app := testServer(func(req *http.Request) (*http.Response, error) {
					if req.URL.Path == "/drive/v3/files/sheet/export" {
						exports++
						if req.Method != http.MethodGet || req.URL.Query().Get("mimeType") != tt.mimeType {
							t.Errorf("outgoing export = %s %s", req.Method, req.URL)
						}
						return textResponse(http.StatusOK, "ODS fixture"), nil
					}
					metadata++
					if req.URL.Path != "/drive/v3/files/sheet" {
						t.Errorf("metadata request = %s", req.URL)
					}
					return jsonResponse(http.StatusOK, string(body)), nil
				})
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, httptest.NewRequest(method, "/export/sheet/"+tt.filename+"?"+tt.query, nil))
				if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != tt.mimeType || !strings.Contains(rec.Header().Get("Content-Disposition"), "Budget.ods") {
					t.Fatalf("status = %d, headers = %v, body = %s", rec.Code, rec.Header(), rec.Body.String())
				}
				if metadata != 1 || (method == http.MethodGet && (exports != 1 || rec.Body.String() != "ODS fixture")) || (method == http.MethodHead && (exports != 0 || rec.Body.Len() != 0)) {
					t.Fatalf("metadata = %d, exports = %d, body = %s", metadata, exports, rec.Body.String())
				}
			})
		}
	}
}
