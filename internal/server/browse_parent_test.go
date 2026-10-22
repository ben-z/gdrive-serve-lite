package server

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"gdrive-serve-lite/internal/drive"
)

const browseParentChildName = "Child /?&#"

func TestBrowseGeneratedIDLinksDoNotGuessParent(t *testing.T) {
	for _, tt := range []struct {
		name, base, child, id, key string
	}{
		{"folder", "", browseParentChildName, "child", "child-key"},
		{"shortcut with base path", "/my base", "Alias & shortcut", "target", "target-key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			front := browseParentFixture(t, tt.base)
			prefix := (&url.URL{Path: tt.base}).EscapedPath()
			res, err := http.Get(front.URL + prefix + "/api/list?links=1")
			if err != nil {
				t.Fatal(err)
			}
			var listing ListResponse
			err = json.NewDecoder(res.Body).Decode(&listing)
			res.Body.Close()
			if err != nil || res.StatusCode != http.StatusOK {
				t.Fatalf("listing status = %d, files = %d, error = %v", res.StatusCode, len(listing.Files), err)
			}
			var selectedURL string
			for _, file := range listing.Files {
				if file.ID == "selected" {
					selectedURL = file.URL
				}
			}
			if selectedURL == "" {
				t.Fatal("missing selected folder URL")
			}
			links := browseParentPage(t, front.URL+selectedURL, "selected")
			childLink := links[tt.child+"/"]
			parsed, err := url.Parse(childLink)
			if err != nil || childLink == "" || parsed.Query().Get("folderId") != tt.id || parsed.Query().Get("resourceKey") != tt.key {
				t.Fatalf("child link = %q, error = %v", childLink, err)
			}
			links = browseParentPage(t, front.URL+childLink, tt.id)
			if parent := links[".."]; parent != "" {
				t.Fatalf("ID-selected child advertises guessed parent %q", parent)
			}
		})
	}
}

func TestBrowseParentLinksRespectFolderSelectors(t *testing.T) {
	const base = "/my base"
	childPath := "/Selected/" + url.PathEscape(browseParentChildName)
	for _, tt := range []struct {
		name, target, folder, parent, parentFolder string
	}{
		{"folder ID", "/browse/Decoration?folderId=child&resourceKey=child-key", "child", "", ""},
		{"escaped browse letters", "/br%6fwse/Decoration?folderId=child&resourceKey=child-key", "child", "", ""},
		{"query path", "/browse/Decoration?path=" + url.QueryEscape(childPath), "child", "", ""},
		{"ID takes precedence", "/browse/Decoration?folderId=selected&resourceKey=selected-key&path=" + url.QueryEscape(childPath), "selected", "", ""},
		{"URL path", "/browse" + childPath, "child", "/browse/Selected", "selected"},
		{"empty selectors", "/browse" + childPath + "?folderId=&path=", "child", "/browse/Selected", "selected"},
		{"root-relative ignores selectors", childPath + "?folderId=ignored&path=ignored", "child", "/browse/Selected", "selected"},
		{"browser prefix", "/browser/" + url.PathEscape(browseParentChildName) + "?folderId=ignored&path=ignored", "browser-child", "/browse/browser", "browser"},
		{"escaped slash in root name", "/browse%2FDecoration?folderId=ignored&path=ignored", "slash-folder", "/browse/", "root-folder"},
		{"root", "/browse/", "root-folder", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			front := browseParentFixture(t, base)
			prefix := (&url.URL{Path: base}).EscapedPath()
			links := browseParentPage(t, front.URL+prefix+tt.target, tt.folder)
			want := tt.parent
			if want != "" {
				want = prefix + want
			}
			if got := links[".."]; got != want {
				t.Fatalf("parent = %q, want %q", got, want)
			}
			if want != "" {
				browseParentPage(t, front.URL+want, tt.parentFolder)
			}
		})
	}
}

func browseParentFixture(t *testing.T, base string) *httptest.Server {
	t.Helper()
	children := map[string][]drive.File{
		"root-folder": {
			{ID: "selected", Name: "Selected", MIMEType: drive.FolderMIME, ResourceKey: "selected-key"},
			{ID: "browser", Name: "browser", MIMEType: drive.FolderMIME, ResourceKey: "browser-key"},
			{ID: "slash-folder", Name: "browse/Decoration", MIMEType: drive.FolderMIME, ResourceKey: "slash-key"},
		},
		"selected": {
			{ID: "child", Name: browseParentChildName, MIMEType: drive.FolderMIME, ResourceKey: "child-key"},
			{ID: "alias", Name: "Alias & shortcut", MIMEType: "application/vnd.google-apps.shortcut", ShortcutDetails: &drive.ShortcutDetails{TargetID: "target", TargetMIMEType: drive.FolderMIME, TargetResourceKey: "target-key"}},
		},
		"child":         nil,
		"target":        nil,
		"browser":       {{ID: "browser-child", Name: browseParentChildName, MIMEType: drive.FolderMIME, ResourceKey: "browser-child-key"}},
		"browser-child": nil,
		"slash-folder":  nil,
	}
	keys := map[string]string{"root-folder": "root-key", "selected": "selected-key", "child": "child-key", "target": "target-key", "browser": "browser-key", "browser-child": "browser-child-key", "slash-folder": "slash-key"}
	app := testServer(func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query().Get("q")
		var files []drive.File
		for id, entries := range children {
			if !strings.Contains(query, "'"+id+"' in parents") {
				continue
			}
			if got, want := req.Header.Get("X-Goog-Drive-Resource-Keys"), id+"/"+keys[id]; got != want {
				t.Errorf("resource key = %q, want %q", got, want)
			}
			files = entries
			break
		}
		if strings.Contains(query, "name = ") {
			var matches []drive.File
			for _, file := range files {
				if strings.Contains(query, "name = '"+file.Name+"'") {
					matches = append(matches, file)
				}
			}
			files = matches
		}
		body, err := json.Marshal(drive.FileList{Files: files})
		if err != nil {
			return nil, err
		}
		return jsonResponse(http.StatusOK, string(body)), nil
	}, Options{BaseURL: base, RootResourceKey: "root-key"})
	front := httptest.NewServer(app.Handler())
	t.Cleanup(front.Close)
	return front
}

func browseParentPage(t *testing.T, target, folder string) map[string]string {
	t.Helper()
	res, err := http.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || res.StatusCode != http.StatusOK || !strings.Contains(string(body), "<code>"+folder+"</code>") {
		t.Fatalf("GET %s = %d, body = %s, error = %v; want folder %q", target, res.StatusCode, body, err, folder)
	}
	links := make(map[string]string)
	for _, match := range regexp.MustCompile(`<a href="([^"]*)">([^<]*)</a>`).FindAllStringSubmatch(string(body), -1) {
		links[html.UnescapeString(match[2])] = html.UnescapeString(match[1])
	}
	return links
}
