package drive

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestGetFileRejectsMissingIDWithoutCaching(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"id":""}`, `{"name":"Report","mimeType":"application/vnd.google-apps.document"}`} {
		t.Run(body, func(t *testing.T) {
			calls := 0
			client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return jsonResponse(http.StatusOK, body), nil
				}
				return jsonResponse(http.StatusOK, `{"id":"requested"}`), nil
			})})
			file, err := client.GetFile(context.Background(), "requested", "")
			if file != nil || err == nil || !strings.Contains(err.Error(), "missing file ID") {
				t.Fatalf("file = %+v, error = %v", file, err)
			}
			for range 2 {
				file, err = client.GetFile(context.Background(), "requested", "")
				if err != nil || file.ID != "requested" {
					t.Fatalf("later valid metadata = %+v, error = %v", file, err)
				}
			}
			if calls != 2 {
				t.Fatalf("requests = %d, want invalid response then cached valid response", calls)
			}
		})
	}
}

func TestGetFileAcceptsSparseMetadataAndCanonicalRootID(t *testing.T) {
	for _, tt := range []struct{ requested, returned string }{
		{"requested", "requested"}, {"root", "canonical-root"},
	} {
		t.Run(tt.requested, func(t *testing.T) {
			calls := 0
			client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return jsonResponse(http.StatusOK, `{"id":"`+tt.returned+`"}`), nil
			})})
			for range 2 {
				file, err := client.GetFile(context.Background(), tt.requested, "")
				if err != nil || file.ID != tt.returned || file.Name != "" || file.MIMEType != "" {
					t.Fatalf("metadata = %+v, error = %v", file, err)
				}
			}
			if calls != 1 {
				t.Fatalf("metadata requests = %d, want cached valid response", calls)
			}
		})
	}
}
