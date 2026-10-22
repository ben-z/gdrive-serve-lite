package drive

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestResolveItemPathRejectsInvalidEscapesBeforeLookup(t *testing.T) {
	for _, path := range []string{"/%", "/%2", "/%ZZ", "/parent/%", "/parent/child/%2", "/parent/%GG/child"} {
		t.Run(path, func(t *testing.T) {
			client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid path reached Drive")
				return nil, nil
			})})
			_, err := client.ResolveItemPath(context.Background(), path, "root", "root-key", "folder")
			var badRequest *BadRequestError
			if !errors.As(err, &badRequest) || !strings.Contains(err.Error(), "invalid URL escape") {
				t.Fatalf("error = %v, want invalid path argument", err)
			}
		})
	}
}

func TestResolveItemPathDecodesSegmentsOnce(t *testing.T) {
	for _, tt := range []struct {
		path, itemType string
		names          []string
	}{
		{"/  Parent  //literal%252Fname/", "file", []string{"  Parent  ", "literal%2Fname"}},
		{"/A%2FB/50%25%20off", "folder", []string{"A/B", "50% off"}},
		{" leading ", "any", []string{" leading "}},
		{"   ", "folder", []string{"   "}},
	} {
		t.Run(tt.path, func(t *testing.T) {
			calls := 0
			client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if calls >= len(tt.names) {
					t.Fatal("unexpected extra path lookup")
				}
				query := req.URL.Query().Get("q")
				if !strings.Contains(query, "name = '"+tt.names[calls]+"'") {
					t.Fatalf("query = %q, want name %q", query, tt.names[calls])
				}
				itemType := "folder"
				if calls == len(tt.names)-1 {
					itemType = tt.itemType
				}
				switch itemType {
				case "file":
					if !strings.Contains(query, "mimeType != '"+FolderMIME+"'") {
						t.Fatalf("leaf query lost file filter: %s", query)
					}
				case "folder":
					if !strings.Contains(query, "mimeType = '"+FolderMIME+"'") {
						t.Fatalf("folder query lost type filter: %s", query)
					}
				case "any":
					if strings.Contains(query, "mimeType") {
						t.Fatalf("any query acquired type filter: %s", query)
					}
				}
				calls++
				mimeType := FolderMIME
				if itemType != "folder" {
					mimeType = "text/plain"
				}
				return jsonResponse(http.StatusOK, fmt.Sprintf(`{"files":[{"id":"item-%d","mimeType":%q}]}`, calls, mimeType)), nil
			})})
			item, err := client.ResolveItemPath(context.Background(), tt.path, "root", "", tt.itemType)
			if err != nil {
				t.Fatal(err)
			}
			if calls != len(tt.names) || item.Path != "/"+strings.Join(tt.names, "/") {
				t.Fatalf("calls = %d, path = %q; want %d, %q", calls, item.Path, len(tt.names), "/"+strings.Join(tt.names, "/"))
			}
		})
	}
}

func TestResolveItemPathEmptyAndRootBehavior(t *testing.T) {
	for _, path := range []string{"", "/", "///"} {
		for _, itemType := range []string{"folder", "any", "file"} {
			t.Run(path+"/"+itemType, func(t *testing.T) {
				client := NewClient(testConfig(), &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					t.Fatal("root path reached Drive")
					return nil, nil
				})})
				item, err := client.ResolveItemPath(context.Background(), path, "root-id", "root-key", itemType)
				if itemType == "file" {
					if !errors.Is(err, ErrNotFound) {
						t.Fatalf("error = %v, want file at root not found", err)
					}
					return
				}
				if err != nil || item.Path != "/" || item.File.ID != "root-id" || item.File.ResourceKey != "root-key" {
					t.Fatalf("item = %+v, error = %v", item, err)
				}
			})
		}
	}
}

func TestExportFormatForRejectsBlankInput(t *testing.T) {
	for _, value := range []string{"", " ", "\t\r\n", "\u00a0"} {
		t.Run(value, func(t *testing.T) {
			_, err := ExportFormatFor(value)
			if !errors.Is(err, ErrUnsupportedExport) || !strings.Contains(err.Error(), "export format or MIME type is required") {
				t.Fatalf("error = %v, want required export format", err)
			}
		})
	}
}
