package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const thumbnailPrivateURL = "https://lh3.googleusercontent.com/private-image?secret=provider-fixture"

var thumbnailJPEG = []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0, 0, 0}

func TestThumbnailLinksStayOnSidecarOriginAndRequireLinksOptIn(t *testing.T) {
	for _, route := range []string{"/api/list?folderId=root", "/api/file?id=video", "/api/resolve?path=/Clip.mp4&type=file"} {
		for _, links := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/links=%t", route, links), func(t *testing.T) {
				app := testServer(func(req *http.Request) (*http.Response, error) {
					if strings.Contains(req.URL.Query().Get("fields"), "thumbnailLink") {
						t.Fatal("listing requested signed links")
					}
					file := `{"id":"video","name":"Clip.mp4","mimeType":"video/mp4","resourceKey":"file-key","hasThumbnail":true,"thumbnailVersion":"version-17","thumbnailLink":"` + thumbnailPrivateURL + `"}`
					if req.URL.Path == "/drive/v3/files" {
						return jsonResponse(200, `{"files":[`+file+`]}`), nil
					}
					return jsonResponse(200, file), nil
				}, Options{BaseURL: "https://sidecar.test/prefix"})
				target := "/prefix" + route
				if links {
					target += "&links=1"
				}
				rec := httptest.NewRecorder()
				app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
				if rec.Code != 200 {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				var body map[string]json.RawMessage
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				fileJSON := rec.Body.Bytes()
				if body["file"] != nil {
					fileJSON = body["file"]
				}
				if body["files"] != nil {
					var files []json.RawMessage
					if err := json.Unmarshal(body["files"], &files); err != nil {
						t.Fatal(err)
					}
					fileJSON = files[0]
				}
				var file map[string]any
				if err := json.Unmarshal(fileJSON, &file); err != nil {
					t.Fatal(err)
				}
				if file["hasThumbnail"] != true || file["thumbnailVersion"] != "version-17" {
					t.Fatalf("file=%s", fileJSON)
				}
				url, present := file["thumbnailUrl"]
				if present != links || (links && url != "/prefix/thumbnail/video?resourceKey=file-key") {
					t.Fatalf("thumbnail URL=%v present=%v", url, present)
				}
				if strings.Contains(rec.Body.String(), thumbnailPrivateURL) || strings.Contains(rec.Body.String(), "thumbnailLink") {
					t.Fatal("private link exposed")
				}
			})
		}
	}
}

func TestKnownThumbnailAbsenceIsExplicit(t *testing.T) {
	app := testServer(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"files":[{"id":"video","name":"Clip.mp4","mimeType":"video/mp4"}]}`), nil
	})
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/list?folderId=root&links=1", nil))
	if !strings.Contains(rec.Body.String(), `"hasThumbnail":false`) || strings.Contains(rec.Body.String(), "thumbnailUrl") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestThumbnailRoutePreservesAuthPrefixResourceKeyAndHTTPImageBehavior(t *testing.T) {
	var metadataReads, imageReads int
	app := testServer(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer test-access" || req.Header.Get("Cookie") != "" {
			t.Fatal("wrong credentials forwarded")
		}
		if req.URL.Host == "www.googleapis.com" {
			metadataReads++
			if req.Header.Get("X-Goog-Drive-Resource-Keys") != "video/key" {
				t.Fatal("resource key omitted")
			}
			return jsonResponse(200, `{"id":"video","hasThumbnail":true,"thumbnailLink":"`+thumbnailPrivateURL+`"}`), nil
		}
		imageReads++
		if req.URL.Host != "lh3.googleusercontent.com" {
			t.Fatal("unexpected image destination")
		}
		res := textResponse(200, string(thumbnailJPEG))
		res.Header.Set("Content-Type", "image/jpeg")
		return res, nil
	}, Options{BaseURL: "/prefix", Username: "sidecar-user", Password: "sidecar-password"})
	request := func(method, etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/prefix/thumbnail/video?resourceKey=key", nil)
		req.SetBasicAuth("sidecar-user", "sidecar-password")
		req.Header.Set("Cookie", "local-cookie")
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}
	denied := httptest.NewRecorder()
	app.Handler().ServeHTTP(denied, httptest.NewRequest(http.MethodGet, "/prefix/thumbnail/video", nil))
	if denied.Code != 401 || metadataReads != 0 {
		t.Fatalf("denied=%d reads=%d", denied.Code, metadataReads)
	}
	image := request(http.MethodGet, "")
	if image.Code != 200 || image.Body.String() != string(thumbnailJPEG) || image.Header().Get("Content-Type") != "image/jpeg" || image.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("image status=%d headers=%v", image.Code, image.Header())
	}
	if image.Header().Get("Cache-Control") != "private, no-cache" || image.Header().Get("ETag") == "" || image.Header().Get("Location") != "" {
		t.Fatalf("headers=%v", image.Header())
	}
	head := request(http.MethodHead, "")
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "8" {
		t.Fatalf("head status=%d body=%q headers=%v", head.Code, head.Body.String(), head.Header())
	}
	conditional := request(http.MethodGet, image.Header().Get("ETag"))
	if conditional.Code != 304 || conditional.Body.Len() != 0 {
		t.Fatalf("conditional=%d", conditional.Code)
	}
	post := request(http.MethodPost, "")
	if post.Code != 405 || post.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("post=%d allow=%q", post.Code, post.Header().Get("Allow"))
	}
	if metadataReads != 3 || imageReads != 3 {
		t.Fatalf("metadata=%d image=%d", metadataReads, imageReads)
	}
}

func TestThumbnailErrorsNeverReturnPrivateLinksOrPartialImageBytes(t *testing.T) {
	for _, phase := range []string{"missing link", "metadata error", "image error", "invalid image"} {
		t.Run(phase, func(t *testing.T) {
			app := testServer(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "www.googleapis.com" {
					if phase == "metadata error" {
						return jsonResponse(403, thumbnailPrivateURL), nil
					}
					if phase == "missing link" {
						return jsonResponse(200, `{"id":"video","hasThumbnail":true}`), nil
					}
					return jsonResponse(200, `{"id":"video","hasThumbnail":true,"thumbnailLink":"`+thumbnailPrivateURL+`"}`), nil
				}
				if phase == "image error" {
					return jsonResponse(403, thumbnailPrivateURL), nil
				}
				res := textResponse(200, "<html>"+thumbnailPrivateURL+"</html>")
				res.Header.Set("Content-Type", "image/jpeg")
				return res, nil
			})
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/thumbnail/video", nil))
			expected := 500
			if phase == "missing link" {
				expected = 404
			}
			if phase == "metadata error" || phase == "image error" {
				expected = 403
			}
			if rec.Code != expected || strings.Contains(rec.Body.String(), thumbnailPrivateURL) || strings.Contains(rec.Body.String(), "<html>") {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("thumbnail error cache policy = %q", rec.Header().Get("Cache-Control"))
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
		})
	}
}
