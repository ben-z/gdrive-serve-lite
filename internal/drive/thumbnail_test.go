package drive

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"
)

const privateThumbnailLink = "https://lh3.googleusercontent.com/private-signed-image?secret=fixture"

var jpegThumbnail = []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0, 0, 0}

func thumbnailMetadata(link string, available bool) *http.Response {
	body, _ := json.Marshal(map[string]any{"id": "video", "hasThumbnail": available, "thumbnailLink": link})
	return jsonResponse(http.StatusOK, string(body))
}
func thumbnailResponse(mimeType string, body []byte) *http.Response {
	res := jsonResponse(http.StatusOK, string(body))
	res.Header.Set("Content-Type", mimeType)
	res.ContentLength = int64(len(body))
	return res
}

func TestThumbnailLinkIsDemandOnlyAndNeverCached(t *testing.T) {
	var metadataReads, imageReads int
	client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer test-access" {
			t.Fatal("missing Drive credentials")
		}
		if req.URL.Host == "www.googleapis.com" {
			metadataReads++
			if got := req.Header.Get("X-Goog-Drive-Resource-Keys"); got != "video/resource-key" {
				t.Fatalf("resource key=%q", got)
			}
			if got := req.URL.Query().Get("fields"); got != "id,hasThumbnail,thumbnailLink" {
				t.Fatalf("fields=%q", got)
			}
			if metadataReads == 1 {
				return thumbnailMetadata("", false), nil
			}
			return thumbnailMetadata(privateThumbnailLink, true), nil
		}
		imageReads++
		if req.URL.String() != privateThumbnailLink {
			t.Fatal("unexpected thumbnail destination")
		}
		if req.Header.Get("Cookie") != "" || req.Header.Get("Range") != "" {
			t.Fatal("caller headers reached thumbnail host")
		}
		return thumbnailResponse("image/jpeg", jpegThumbnail), nil
	})})
	if _, err := client.GetThumbnail(context.Background(), "video", "resource-key"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("first read=%v", err)
	}
	for range 2 {
		image, err := client.GetThumbnail(context.Background(), "video", "resource-key")
		if err != nil || image.MIMEType != "image/jpeg" || string(image.Body) != string(jpegThumbnail) {
			t.Fatalf("image=%v err=%v", image, err)
		}
	}
	if metadataReads != 3 || imageReads != 2 {
		t.Fatalf("reads metadata=%d image=%d", metadataReads, imageReads)
	}
}

func TestOrdinaryMetadataIncludesAvailabilityAndVersionWithoutSignedLinks(t *testing.T) {
	client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		fields := req.URL.Query().Get("fields")
		if strings.Contains(fields, "thumbnailLink") || !strings.Contains(fields, "hasThumbnail") || !strings.Contains(fields, "thumbnailVersion") {
			t.Fatalf("fields=%q", fields)
		}
		payload := `{"id":"video","name":"Clip.mp4","mimeType":"video/mp4","hasThumbnail":true,"thumbnailVersion":"opaque-version","thumbnailLink":"` + privateThumbnailLink + `"}`
		if req.URL.Path == "/drive/v3/files" {
			return jsonResponse(http.StatusOK, `{"files":[`+payload+`]}`), nil
		}
		return jsonResponse(http.StatusOK, payload), nil
	})})
	file, err := client.GetFile(context.Background(), "video", "")
	if err != nil {
		t.Fatal(err)
	}
	list, err := client.ListChildren(context.Background(), ListOptions{FolderID: "root"})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range append(list.Files, *file) {
		data, err := json.Marshal(file)
		if err != nil {
			t.Fatal(err)
		}
		if !file.HasThumbnail || file.ThumbnailVersion != "opaque-version" || strings.Contains(string(data), privateThumbnailLink) || strings.Contains(string(data), "thumbnailLink") {
			t.Fatalf("public metadata=%s", data)
		}
	}
	data, _ := json.Marshal(File{ID: "no-preview"})
	if !strings.Contains(string(data), `"hasThumbnail":false`) {
		t.Fatalf("availability omitted: %s", data)
	}
}

func TestThumbnailRejectsUnsafeHostsBeforeSendingCredentials(t *testing.T) {
	for _, link := range []string{"http://lh3.googleusercontent.com/image", "https://evil.invalid/image", "https://lh3.googleusercontent.com.evil.invalid/image", "https://user:password@lh3.googleusercontent.com/image", "https://lh3.googleusercontent.com:444/image", "https://127.0.0.1/image", "https://lh3.googleusercontent.com/image#fragment", "//lh3.googleusercontent.com/image"} {
		t.Run(link, func(t *testing.T) {
			calls := 0
			client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return thumbnailMetadata(link, true), nil })})
			_, err := client.GetThumbnail(context.Background(), "video", "")
			if err == nil || calls != 1 || strings.Contains(err.Error(), link) {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestThumbnailRefusesAllRedirectsWithoutMutatingSharedClient(t *testing.T) {
	for _, target := range []string{"https://evil.invalid/private", "https://lh3.googleusercontent.com/another"} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Host == "www.googleapis.com" {
					return thumbnailMetadata(privateThumbnailLink, true), nil
				}
				if req.URL.String() != privateThumbnailLink {
					t.Fatal("followed thumbnail redirect")
				}
				res := jsonResponse(http.StatusFound, "private response body")
				res.Header.Set("Location", target)
				return res, nil
			})}
			client := NewClient(testConfig(), httpClient)
			_, err := client.GetThumbnail(context.Background(), "video", "")
			if err == nil || calls != 2 || strings.Contains(err.Error(), target) || httpClient.CheckRedirect != nil {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestThumbnailAcceptsOnlyBoundedMatchingRasterImages(t *testing.T) {
	for _, tt := range []struct {
		name, mime string
		body       []byte
		valid      bool
	}{
		{"jpeg", "image/jpeg", jpegThumbnail, true},
		{"png", "image/png", []byte("\x89PNG\r\n\x1a\nfixture"), true},
		{"gif", "image/gif", []byte("GIF89afixture"), true},
		{"webp", "image/webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 fixture"), true},
		{"svg", "image/svg+xml", []byte("<svg/>"), false},
		{"html disguised as image", "image/jpeg", []byte("<html><script>private</script></html>"), false},
		{"mismatched raster", "image/jpeg", []byte("GIF89afixture"), false},
		{"empty", "image/jpeg", nil, false},
		{"oversized", "image/jpeg", append(jpegThumbnail, make([]byte, MaxThumbnailBytes)...), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "www.googleapis.com" {
					return thumbnailMetadata(privateThumbnailLink, true), nil
				}
				res := thumbnailResponse(tt.mime, tt.body)
				res.ContentLength = -1
				return res, nil
			})})
			_, err := client.GetThumbnail(context.Background(), "video", "")
			if (err == nil) != tt.valid {
				t.Fatalf("err=%v valid=%v", err, tt.valid)
			}
		})
	}
}

func TestThumbnailSanitizesAllProviderAndTransportErrors(t *testing.T) {
	canary := privateThumbnailLink + " bearer-secret"
	for _, phase := range []string{"metadata", "image"} {
		for _, status := range []int{401, 403, 404, 500} {
			t.Run(phase+http.StatusText(status), func(t *testing.T) {
				client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if phase == "metadata" || req.URL.Host != "www.googleapis.com" {
						return jsonResponse(status, canary), nil
					}
					return thumbnailMetadata(privateThumbnailLink, true), nil
				})})
				_, err := client.GetThumbnail(context.Background(), "video", "")
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.StatusCode != status || strings.Contains(err.Error(), canary) {
					t.Fatalf("err=%v", err)
				}
			})
		}
	}
	client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "www.googleapis.com" {
			return thumbnailMetadata(privateThumbnailLink, true), nil
		}
		return nil, errors.New(canary)
	})})
	_, err := client.GetThumbnail(context.Background(), "video", "")
	if err == nil || strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), privateThumbnailLink) {
		t.Fatalf("err=%v", err)
	}
}

type thumbnailBody struct {
	reads  int
	closed bool
}

func (b *thumbnailBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New(privateThumbnailLink)
}
func (b *thumbnailBody) Close() error { b.closed = true; return nil }
func TestThumbnailRejectsDeclaredOversizeBeforeReadingAndClosesBodies(t *testing.T) {
	body := &thumbnailBody{}
	client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "www.googleapis.com" {
			return thumbnailMetadata(privateThumbnailLink, true), nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"image/jpeg"}}, ContentLength: MaxThumbnailBytes + 1, Body: body}, nil
	})})
	_, err := client.GetThumbnail(context.Background(), "video", "")
	if err == nil || body.reads != 0 || !body.closed {
		t.Fatalf("err=%v reads=%d closed=%v", err, body.reads, body.closed)
	}
}
func TestThumbnailInterruptedImageNeverReturnsPartialBytes(t *testing.T) {
	client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "www.googleapis.com" {
			return thumbnailMetadata(privateThumbnailLink, true), nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"image/jpeg"}}, Body: io.NopCloser(io.MultiReader(strings.NewReader(string(jpegThumbnail)), &thumbnailBody{}))}, nil
	})})
	image, err := client.GetThumbnail(context.Background(), "video", "")
	if err == nil || image != nil || strings.Contains(err.Error(), privateThumbnailLink) {
		t.Fatalf("image=%v err=%v", image, err)
	}
}

func TestThumbnailMetadataRedirectCannotForwardCredentialsOrResourceKeys(t *testing.T) {
	calls := 0
	client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "www.googleapis.com" {
			t.Fatal("metadata redirect followed")
		}
		res := jsonResponse(302, "private redirect body")
		res.Header.Set("Location", "https://evil.invalid/capture")
		return res, nil
	})})
	_, err := client.GetThumbnail(context.Background(), "video", "private-resource-key")
	if err == nil || calls != 1 || strings.Contains(err.Error(), "capture") {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestThumbnailIgnoresSharedCookieJar(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	host, _ := url.Parse("https://lh3.googleusercontent.com")
	jar.SetCookies(host, []*http.Cookie{{Name: "private-cookie", Value: "secret"}})
	client := NewClient(testConfig(), &http.Client{Jar: jar, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Cookie") != "" {
			t.Fatal("cookie jar reached thumbnail read")
		}
		if req.URL.Host == "www.googleapis.com" {
			return thumbnailMetadata(privateThumbnailLink, true), nil
		}
		return thumbnailResponse("image/jpeg", jpegThumbnail), nil
	})})
	if _, err := client.GetThumbnail(context.Background(), "video", ""); err != nil {
		t.Fatal(err)
	}
}

func TestThumbnailRetainsExistingSingleOAuthRefreshOnUnauthorizedImage(t *testing.T) {
	cfg := testConfig()
	cfg.Token.RefreshToken = "synthetic-refresh"
	cfg.Token.Expiry = time.Now().Add(time.Hour)
	images, refreshes := 0, 0
	client := NewClient(cfg, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "www.googleapis.com":
			return thumbnailMetadata(privateThumbnailLink, true), nil
		case "oauth2.googleapis.com":
			refreshes++
			return jsonResponse(200, `{"access_token":"new-access","expires_in":3600}`), nil
		case "lh3.googleusercontent.com":
			images++
			if images == 1 {
				return jsonResponse(401, "private denial"), nil
			}
			if req.Header.Get("Authorization") != "Bearer new-access" {
				t.Fatal("refreshed access token missing")
			}
			return thumbnailResponse("image/jpeg", jpegThumbnail), nil
		default:
			t.Fatal("unexpected credential destination")
			return nil, nil
		}
	})})
	if _, err := client.GetThumbnail(context.Background(), "video", ""); err != nil {
		t.Fatal(err)
	}
	if images != 2 || refreshes != 1 {
		t.Fatalf("images=%d refreshes=%d", images, refreshes)
	}
}

func TestThumbnailMetadataMustMatchRequestedFile(t *testing.T) {
	for _, body := range []string{`{"hasThumbnail":true}`, `{"id":"other","hasThumbnail":true}`} {
		calls := 0
		client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return jsonResponse(200, body), nil })})
		if _, err := client.GetThumbnail(context.Background(), "video", ""); err == nil || calls != 1 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	}
}
