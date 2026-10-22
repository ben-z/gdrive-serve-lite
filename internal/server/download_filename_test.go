package server

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gdrive-serve-lite/internal/drive"
)

func TestGeneratedLinksPreserveDownloadFilenames(t *testing.T) {
	for _, name := range []string{"alice@example.com", "a:b", "a=b", "semi;quote\" 50%/done+", "雪<&>", "literal%20name", "literal%2Fname"} {
		for _, export := range []bool{false, true} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				t.Run(fmt.Sprintf("%s/export=%t/%s", name, export, method), func(t *testing.T) {
					file := drive.File{ID: "target", Name: name, MIMEType: "text/plain", Size: "5", ResourceKey: "target-key"}
					wantName := name
					if export {
						file.MIMEType = "application/vnd.google-apps.document"
						file.ExportLinks = map[string]string{"application/pdf": "https://example.invalid/pdf"}
						wantName += ".pdf"
					}
					metadata, err := json.Marshal(file)
					if err != nil {
						t.Fatal(err)
					}
					listing, err := json.Marshal(map[string][]drive.File{"files": {file}})
					if err != nil {
						t.Fatal(err)
					}
					app := testServer(func(req *http.Request) (*http.Response, error) {
						if req.URL.Path == "/drive/v3/files" {
							return jsonResponse(http.StatusOK, string(listing)), nil
						}
						if req.Header.Get("X-Goog-Drive-Resource-Keys") != "target/target-key" {
							t.Errorf("resource key header = %q", req.Header.Get("X-Goog-Drive-Resource-Keys"))
						}
						if req.URL.Query().Get("alt") == "media" || strings.HasSuffix(req.URL.Path, "/export") {
							return textResponse(http.StatusOK, "hello"), nil
						}
						return jsonResponse(http.StatusOK, string(metadata)), nil
					}, Options{BaseURL: "/prefix"})
					front := httptest.NewServer(app.Handler())
					defer front.Close()
					listed, err := http.Get(front.URL + "/prefix/api/list?links=1")
					if err != nil {
						t.Fatal(err)
					}
					var body ListResponse
					err = json.NewDecoder(listed.Body).Decode(&body)
					listed.Body.Close()
					if err != nil || listed.StatusCode != http.StatusOK || len(body.Files) != 1 {
						t.Fatalf("listing status = %d, files = %d, error = %v", listed.StatusCode, len(body.Files), err)
					}
					req, err := http.NewRequest(method, front.URL+body.Files[0].URL, nil)
					if err != nil {
						t.Fatal(err)
					}
					res, err := http.DefaultClient.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(res.Body)
					res.Body.Close()
					wantBody := "hello"
					if method == http.MethodHead {
						wantBody = ""
					}
					if err != nil || res.StatusCode != http.StatusOK || string(data) != wantBody {
						t.Fatalf("read status = %d, body = %q, error = %v", res.StatusCode, data, err)
					}
					disposition := res.Header.Get("Content-Disposition")
					mediaType, params, err := mime.ParseMediaType(disposition)
					if err != nil || mediaType != "inline" || params["filename"] != wantName {
						t.Fatalf("Content-Disposition = %q, parsed %q %v, error %v; want inline filename %q", disposition, mediaType, params, err, wantName)
					}
					// Browsers can decode percent escapes in plain filename= values.
					if strings.HasPrefix(name, "literal%") && !strings.Contains(disposition, "filename*=") {
						t.Fatalf("Content-Disposition must preserve literal percent escapes: %q", disposition)
					}
				})
			}
		}
	}
}

func TestEmptyDownloadFilename(t *testing.T) {
	if got := contentDisposition(""); got != "inline" {
		t.Fatalf("Content-Disposition = %q, want inline", got)
	}
}
