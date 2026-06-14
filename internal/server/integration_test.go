package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"gdrive-serve-lite/internal/config"
	"gdrive-serve-lite/internal/drive"
)

// This is an end-to-end integration test: the real server.Handler and the
// real drive.Client run against a stateful HTTP server that speaks the
// Google Drive v3 wire protocol, with traffic crossing real TCP sockets in
// both directions (client -> server.Handler -> drive.Client -> Drive
// backend). It is the closest autonomous approximation of a live Drive: it
// exercises listing, multi-page pagination via real `next` links, path
// resolution, browse, blob download with HTTP Range, conditional 304s,
// Google Workspace export, export-formats, and OAuth refresh-on-401 —
// without any mocked RoundTripper.

const (
	folderMIME   = "application/vnd.google-apps.folder"
	documentMIME = "application/vnd.google-apps.document"
)

type driveNode struct {
	id, name, mime, size, modified, content, resourceKey string
	parent                                               string
	exportLinks                                          map[string]string
}

// fakeDrive is an in-memory Google Drive v3 backend.
type fakeDrive struct {
	nodes     []driveNode
	bigParent string // folder whose children are generated on demand
	bigCount  int
	tokenHits int
	listHits  int
	mediaHits int
	parentRe  *regexp.Regexp
	nameRe    *regexp.Regexp
}

func newFakeDrive() *fakeDrive {
	mod := "2026-06-08T12:34:56.000Z"
	return &fakeDrive{
		bigParent: "vid-folder",
		bigCount:  2500,
		parentRe:  regexp.MustCompile(`'([^']*)' in parents`),
		nameRe:    regexp.MustCompile(`name = '([^']*)'`),
		nodes: []driveNode{
			{id: "vid-folder", name: "Videos", mime: folderMIME, parent: "root-folder", modified: mod},
			{id: "docs-folder", name: "Docs", mime: folderMIME, parent: "root-folder", modified: mod, resourceKey: "docs-key"},
			{id: "file-readme", name: "readme.txt", mime: "text/plain", size: "12", parent: "root-folder", modified: mod, content: "hello drive\n"},
			{id: "doc-notes", name: "Notes", mime: documentMIME, parent: "docs-folder", modified: mod,
				exportLinks: map[string]string{
					"application/pdf": "https://example.invalid/pdf",
					"application/vnd.openxmlformats-officedocument.wordprocessingml.document": "https://example.invalid/docx",
				}},
		},
	}
}

func (d *fakeDrive) node(id string) (driveNode, bool) {
	for _, n := range d.nodes {
		if n.id == id {
			return n, true
		}
	}
	if id != "" && strings.HasPrefix(id, "clip-") {
		i, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(id, "clip-"), ".mp4"))
		if err == nil && i >= 0 && i < d.bigCount {
			return d.generatedClip(i), true
		}
	}
	return driveNode{}, false
}

func (d *fakeDrive) generatedClip(i int) driveNode {
	return driveNode{
		id:       fmt.Sprintf("clip-%04d.mp4", i),
		name:     fmt.Sprintf("clip-%04d.mp4", i),
		mime:     "video/mp4",
		size:     "1048576",
		parent:   d.bigParent,
		modified: "2026-06-08T12:34:56.000Z",
		content:  fmt.Sprintf("bytes-of-clip-%04d", i),
	}
}

func (d *fakeDrive) children(parent string) []driveNode {
	var out []driveNode
	for _, n := range d.nodes {
		if n.parent == parent {
			out = append(out, n)
		}
	}
	if parent == d.bigParent {
		for i := range d.bigCount {
			out = append(out, d.generatedClip(i))
		}
	}
	return out
}

func (d *fakeDrive) marshalFile(n driveNode) map[string]any {
	m := map[string]any{"id": n.id, "name": n.name, "mimeType": n.mime}
	if n.size != "" {
		m["size"] = n.size
	}
	if n.modified != "" {
		m["modifiedTime"] = n.modified
	}
	if n.resourceKey != "" {
		m["resourceKey"] = n.resourceKey
	}
	if len(n.exportLinks) > 0 {
		m["exportLinks"] = n.exportLinks
	}
	return m
}

func (d *fakeDrive) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		d.tokenHits++
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "refreshed-token", "expires_in": 3600})
	})

	mux.HandleFunc("/drive/v3/files/", func(w http.ResponseWriter, r *http.Request) {
		// Require a bearer token; a stale one triggers the refresh path.
		if auth := r.Header.Get("Authorization"); auth == "Bearer stale-token" {
			http.Error(w, `{"error":"invalid"}`, http.StatusUnauthorized)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/drive/v3/files/")
		id, isExport := strings.CutSuffix(rest, "/export")
		n, ok := d.node(id)
		if !ok {
			http.Error(w, `{"error":{"message":"File not found"}}`, http.StatusNotFound)
			return
		}
		if isExport {
			w.Header().Set("Content-Type", r.URL.Query().Get("mimeType"))
			w.Header().Set("Content-Disposition", "attachment; filename=export.bin")
			_, _ = io.WriteString(w, "%PDF-1.4 exported "+n.id)
			return
		}
		if r.URL.Query().Get("alt") == "media" {
			d.mediaHits++
			modTime, _ := time.Parse(time.RFC3339, n.modified)
			w.Header().Set("Content-Type", n.mime)
			// ServeContent supplies Accept-Ranges, Content-Range, 206/416,
			// and honors If-Modified-Since/If-Range/Range like Drive does.
			http.ServeContent(w, r, n.name, modTime, bytes.NewReader([]byte(n.content)))
			return
		}
		writeJSON(w, http.StatusOK, d.marshalFile(n))
	})

	mux.HandleFunc("/drive/v3/files", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer stale-token" {
			http.Error(w, `{"error":"invalid"}`, http.StatusUnauthorized)
			return
		}
		d.listHits++
		q := r.URL.Query().Get("q")
		parent := ""
		if m := d.parentRe.FindStringSubmatch(q); m != nil {
			parent = m[1]
		}
		kids := d.children(parent)

		if m := d.nameRe.FindStringSubmatch(q); m != nil {
			var filtered []driveNode
			for _, n := range kids {
				if n.name == m[1] {
					filtered = append(filtered, n)
				}
			}
			kids = filtered
		}
		wantFolders := strings.Contains(q, "mimeType = '"+folderMIME+"'")
		wantFiles := strings.Contains(q, "mimeType != '"+folderMIME+"'")
		if wantFolders || wantFiles {
			var filtered []driveNode
			for _, n := range kids {
				isFolder := n.mime == folderMIME
				if isFolder == wantFolders {
					filtered = append(filtered, n)
				}
			}
			kids = filtered
		}
		sort.Slice(kids, func(i, j int) bool { return kids[i].name < kids[j].name })

		pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
		if pageSize <= 0 {
			pageSize = 1000
		}
		start := 0
		if tok := r.URL.Query().Get("pageToken"); tok != "" {
			start, _ = strconv.Atoi(tok)
		}
		end := min(start+pageSize, len(kids))
		files := make([]map[string]any, 0, end-start)
		for _, n := range kids[start:end] {
			files = append(files, d.marshalFile(n))
		}
		resp := map[string]any{"files": files}
		if end < len(kids) {
			resp["nextPageToken"] = strconv.Itoa(end)
		}
		writeJSON(w, http.StatusOK, resp)
	})

	return mux
}

// redirectTransport rewrites every request host to the fake Drive backend,
// so the real drive.Client talks to it over real TCP without any code
// change to the tool.
type redirectTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (rt redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = rt.target.Scheme
	clone.URL.Host = rt.target.Host
	clone.Host = rt.target.Host
	return rt.base.RoundTrip(clone)
}

// liveStack wires a real server.Handler -> real drive.Client -> fake Drive,
// with the front door also on a real socket. Returns the public base URL
// and the backend for assertions.
func liveStack(t *testing.T, token config.OAuthToken) (string, *fakeDrive) {
	t.Helper()
	backend := newFakeDrive()
	driveSrv := httptest.NewServer(backend.handler(t))
	t.Cleanup(driveSrv.Close)
	target, err := url.Parse(driveSrv.URL)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{ClientID: "client", ClientSecret: "secret", RootFolderID: "root-folder", Token: token}
	client := drive.NewClient(cfg, &http.Client{Transport: redirectTransport{target: target, base: http.DefaultTransport}})
	client.SetListCacheTTL(0) // exercise the backend on every call
	app := New(client, Options{RootFolderID: "root-folder"})

	front := httptest.NewServer(app.Handler())
	t.Cleanup(front.Close)
	return front.URL, backend
}

func freshToken() config.OAuthToken {
	return config.OAuthToken{AccessToken: "good-token", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}
}

func getJSON(t *testing.T, url string) (*http.Response, ListResponse) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var lr ListResponse
	if strings.Contains(res.Header.Get("Content-Type"), "json") {
		if err := json.Unmarshal(body, &lr); err != nil {
			t.Fatalf("decode %s: %v (body=%s)", url, err, body[:min(len(body), 200)])
		}
	}
	return res, lr
}

func TestIntegrationListAndPaginateOverRealSockets(t *testing.T) {
	base, backend := liveStack(t, freshToken())

	// Root listing returns the two folders and the blob and the doc.
	res, root := getJSON(t, base+"/api/list?type=all&orderBy=name&pageSize=1000&limit=1000")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("root status = %d", res.StatusCode)
	}
	names := map[string]bool{}
	for _, f := range root.Files {
		names[f.Name] = true
	}
	// Notes lives under Docs, not at root.
	for _, want := range []string{"Videos", "Docs", "readme.txt"} {
		if !names[want] {
			t.Fatalf("root listing missing %q: %+v", want, root.Files)
		}
	}
	if names["Notes"] {
		t.Fatalf("Notes should not appear at root: %+v", root.Files)
	}

	// Walk all 2500 generated clips in the Videos folder by following the
	// real `next` links the server emits — multi-request pagination end to
	// end. limit=1000 caps each response, so this takes 3 requests.
	seen := map[string]bool{}
	next := base + "/api/list?folderId=vid-folder&type=files&orderBy=name&pageSize=1000&limit=1000"
	pages := 0
	for next != "" {
		res, page := getJSON(t, next)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("page %d status = %d", pages, res.StatusCode)
		}
		for _, f := range page.Files {
			if seen[f.ID] {
				t.Fatalf("duplicate %q", f.ID)
			}
			seen[f.ID] = true
		}
		pages++
		if page.Next == "" {
			if len(page.Files) != 500 {
				t.Fatalf("last page had %d files, want 500", len(page.Files))
			}
			break
		}
		next = base + page.Next
		if !strings.Contains(page.Next, "pageToken=") {
			t.Fatalf("next link missing pageToken: %q", page.Next)
		}
	}
	if pages != 3 || len(seen) != 2500 {
		t.Fatalf("pages=%d seen=%d, want 3/2500", pages, len(seen))
	}
	if backend.listHits < 4 {
		t.Fatalf("backend listHits=%d, expected at least 4 real list calls", backend.listHits)
	}
}

func TestIntegrationResolveBrowseAndTypeFilters(t *testing.T) {
	base, _ := liveStack(t, freshToken())

	// Path resolution through nested folders.
	res, err := http.Get(base + "/api/resolve?path=/Docs/Notes&type=file")
	if err != nil {
		t.Fatal(err)
	}
	var rr ResolveResponse
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if err := json.Unmarshal(body, &rr); err != nil {
		t.Fatal(err)
	}
	if rr.ID != "doc-notes" || rr.IsDir || rr.ParentID != "docs-folder" {
		t.Fatalf("resolve = %+v", rr)
	}

	// type=folders excludes the blob and doc; type=files excludes folders.
	_, folders := getJSON(t, base+"/api/list?type=folders&orderBy=name&pageSize=1000&limit=1000")
	if len(folders.Files) != 2 {
		t.Fatalf("folders = %d, want 2 (Docs, Videos): %+v", len(folders.Files), folders.Files)
	}
	_, files := getJSON(t, base+"/api/list?type=files&orderBy=name&pageSize=1000&limit=1000")
	for _, f := range files.Files {
		if f.MIMEType == folderMIME {
			t.Fatalf("type=files returned a folder: %+v", f)
		}
	}

	// Browse HTML lists the folder and links its children.
	res, err = http.Get(base + "/browse/Docs?orderBy=name&pageSize=1000&limit=1000")
	if err != nil {
		t.Fatal(err)
	}
	html, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(res.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("browse Content-Type = %q", res.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(html), "/export/doc-notes/Notes.docx") {
		t.Fatalf("browse HTML missing export link:\n%s", html)
	}
}

func TestIntegrationBlobRangeDownloadAndConditional(t *testing.T) {
	base, backend := liveStack(t, freshToken())

	// Full blob download.
	res, err := http.Get(base + "/file/file-readme/readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || string(body) != "hello drive\n" {
		t.Fatalf("download status=%d body=%q", res.StatusCode, body)
	}
	if !strings.Contains(res.Header.Get("Content-Disposition"), "readme.txt") {
		t.Fatalf("Content-Disposition = %q", res.Header.Get("Content-Disposition"))
	}

	// Real HTTP Range request, forwarded to the backend and served as 206.
	req, _ := http.NewRequest(http.MethodGet, base+"/file/file-readme/readme.txt", nil)
	req.Header.Set("Range", "bytes=0-4")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusPartialContent || string(body) != "hello" {
		t.Fatalf("range status=%d body=%q", res.StatusCode, body)
	}
	if res.Header.Get("Content-Range") == "" {
		t.Fatal("missing Content-Range on 206")
	}

	// HEAD uses metadata only (no media fetch) and advertises ranges.
	mediaBefore := backend.mediaHits
	req, _ = http.NewRequest(http.MethodHead, base+"/file/file-readme/readme.txt", nil)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("HEAD status=%d accept-ranges=%q", res.StatusCode, res.Header.Get("Accept-Ranges"))
	}
	if backend.mediaHits != mediaBefore {
		t.Fatal("HEAD should not fetch media bytes")
	}

	// Conditional GET: If-Modified-Since at the file's mtime -> 304.
	lastMod := res.Header.Get("Last-Modified")
	if lastMod == "" {
		t.Fatal("HEAD missing Last-Modified")
	}
	req, _ = http.NewRequest(http.MethodGet, base+"/file/file-readme/readme.txt", nil)
	req.Header.Set("If-Modified-Since", lastMod)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional GET status=%d, want 304", res.StatusCode)
	}
}

func TestIntegrationExportAndFormats(t *testing.T) {
	base, _ := liveStack(t, freshToken())

	// export-formats reports the doc's available formats and default.
	res, err := http.Get(base + "/api/export-formats?id=doc-notes")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), `"defaultExport"`) ||
		!strings.Contains(string(body), `"docx"`) ||
		!strings.Contains(string(body), `"pdf"`) {
		t.Fatalf("export-formats body = %s", body)
	}

	// Export to PDF streams the converted bytes with the export disposition.
	res, err = http.Get(base + "/export/doc-notes/Notes?format=pdf")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(string(body), "%PDF") {
		t.Fatalf("export status=%d body=%q", res.StatusCode, body[:min(len(body), 40)])
	}

	// Export rejects Range locally without hitting the backend.
	req, _ := http.NewRequest(http.MethodGet, base+"/export/doc-notes/Notes?format=pdf", nil)
	req.Header.Set("Range", "bytes=0-9")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("export+Range status=%d, want 400", res.StatusCode)
	}
}

func TestIntegrationOAuthRefreshOnUnauthorized(t *testing.T) {
	// Start with a stale access token; the backend 401s it, the client
	// refreshes against /token over a real socket and retries.
	stale := config.OAuthToken{AccessToken: "stale-token", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}
	base, backend := liveStack(t, stale)

	res, lr := getJSON(t, base+"/api/list?folderId=docs-folder&type=files&orderBy=name&pageSize=1000&limit=1000")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if len(lr.Files) != 1 || lr.Files[0].ID != "doc-notes" {
		t.Fatalf("files = %+v", lr.Files)
	}
	if backend.tokenHits == 0 {
		t.Fatal("expected a token refresh against the real /token endpoint")
	}
}
