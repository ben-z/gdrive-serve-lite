package drive

import (
	"context"
	"errors"
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

func TestResolveItemPathRejectsMissingIDWithoutCaching(t *testing.T) {
	for _, tt := range []struct{ name, body, itemType string }{
		{"null", `null`, "file"},
		{"empty object", `{}`, "any"},
		{"empty ID", `{"id":""}`, "file"},
		{"folder", `{"mimeType":"application/vnd.google-apps.folder"}`, "folder"},
		{"shortcut", `{"mimeType":"application/vnd.google-apps.shortcut","shortcutDetails":{"targetId":"target","targetMimeType":"text/plain"}}`, "file"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Header.Get("X-Goog-Drive-Resource-Keys") != "root/root-key" {
					t.Fatalf("resource key header = %q", req.Header.Get("X-Goog-Drive-Resource-Keys"))
				}
				if calls == 1 {
					return jsonResponse(http.StatusOK, `{"files":[`+tt.body+`]}`), nil
				}
				mimeType := "text/plain"
				if tt.itemType == "folder" {
					mimeType = FolderMIME
				}
				return jsonResponse(http.StatusOK, `{"files":[{"id":"canonical","mimeType":"`+mimeType+`","resourceKey":"actual-key"}]}`), nil
			})})
			item, err := client.ResolveItemPath(context.Background(), "/Alias", "root", "root-key", tt.itemType)
			if item != nil || err == nil || err.Error() != "decode Drive response: missing file ID" {
				t.Errorf("item = %+v, error = %v", item, err)
			}
			for range 2 {
				item, err = client.ResolveItemPath(context.Background(), "/Alias", "root", "root-key", tt.itemType)
				if err != nil || item.File.ID != "canonical" || item.File.ResourceKey != "actual-key" {
					t.Fatalf("later valid metadata = %+v, error = %v", item, err)
				}
			}
			if calls != 2 {
				t.Fatalf("requests = %d, want invalid response then cached valid response", calls)
			}
		})
	}
}

func TestResolveItemPathAcceptsSparseAndShortcutIDs(t *testing.T) {
	for _, tt := range []struct{ name, body, itemType, targetID, targetKey string }{
		{"sparse", `{"id":"canonical"}`, "any", "canonical", ""},
		{"folder without name", `{"id":"canonical","mimeType":"application/vnd.google-apps.folder"}`, "folder", "canonical", ""},
		{"shortcut", `{"id":"canonical","mimeType":"application/vnd.google-apps.shortcut","shortcutDetails":{"targetId":"target","targetMimeType":"application/vnd.google-apps.folder","targetResourceKey":"target-key"}}`, "folder", "target", "target-key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return jsonResponse(http.StatusOK, `{"files":[`+tt.body+`]}`), nil
			})})
			for range 2 {
				item, err := client.ResolveItemPath(context.Background(), "/Alias", "root", "", tt.itemType)
				if err != nil {
					t.Fatal(err)
				}
				id, key := item.File.TargetIDAndKey()
				if item.File.ID != "canonical" || id != tt.targetID || key != tt.targetKey {
					t.Fatalf("metadata = %+v, target = %q/%q", item.File, id, key)
				}
			}
			if calls != 1 {
				t.Fatalf("requests = %d, want cached valid response", calls)
			}
		})
	}
}

func TestResolveItemPathMissingIDPreservesCardinalityErrors(t *testing.T) {
	for _, tt := range []struct{ name, body string }{
		{"not found", `{"files":[]}`},
		{"ambiguous", `{"files":[{"mimeType":"application/vnd.google-apps.folder"},{"id":"other","mimeType":"application/vnd.google-apps.folder"}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return jsonResponse(http.StatusOK, tt.body), nil
			})})
			for range 2 {
				item, err := client.ResolveItemPath(context.Background(), "/Alias", "root", "", "folder")
				var ambiguous *AmbiguousPathError
				if item != nil || (tt.name == "not found" && !errors.Is(err, ErrNotFound)) || (tt.name == "ambiguous" && (!errors.As(err, &ambiguous) || ambiguous.Count != 2)) {
					t.Fatalf("item = %+v, error = %v", item, err)
				}
			}
			if calls != 2 {
				t.Fatalf("requests = %d, want uncached cardinality error", calls)
			}
		})
	}
}
