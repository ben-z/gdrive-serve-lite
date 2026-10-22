package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"gdrive-serve-lite/internal/drive"
)

// Options configures a Server. The zero value serves the configured root
// with no local auth, no CORS, and no path prefix.
type Options struct {
	RootFolderID    string
	RootResourceKey string
	Username        string
	Password        string
	AllowOrigin     string
	BaseURL         string
}

type Server struct {
	drive       *drive.Client
	root        folderRef
	auth        *basicAuth
	allowOrigin string
	basePath    string
}

type basicAuth struct {
	username string
	password string
}

type folderRef struct {
	ID          string
	ResourceKey string
}

// folderPath carries a folder's display path alongside its URL-escaped
// form; escaped segments are the unit of path resolution throughout.
type folderPath struct {
	Display string
	Escaped string
}

func New(client *drive.Client, opts Options) *Server {
	s := &Server{
		drive:       client,
		root:        folderRef{ID: opts.RootFolderID, ResourceKey: opts.RootResourceKey},
		allowOrigin: strings.TrimSpace(opts.AllowOrigin),
		basePath:    normalizeBasePath(opts.BaseURL),
	}
	if opts.Username != "" || opts.Password != "" {
		s.auth = &basicAuth{username: opts.Username, password: opts.Password}
	}
	return s
}

var readResponseHeaders = []string{
	"Content-Type",
	"Content-Length",
	"Content-Range",
	"Accept-Ranges",
	"ETag",
	"Last-Modified",
	"Content-Disposition",
	"Cache-Control",
	"Expires",
	"Content-Encoding",
	"Content-Language",
	"Content-MD5",
	"X-Goog-Hash",
}

type ListResponse struct {
	Path             string       `json:"path,omitempty"`
	FolderID         string       `json:"folderId"`
	ResourceKey      string       `json:"resourceKey,omitempty"`
	Count            int          `json:"count"`
	Files            []drive.File `json:"files"`
	NextPageToken    string       `json:"nextPageToken,omitempty"`
	Next             string       `json:"next,omitempty"`
	IncompleteSearch bool         `json:"incompleteSearch,omitempty"`
}

type ResolveResponse struct {
	Path        string     `json:"path"`
	ID          string     `json:"id"`
	FolderID    string     `json:"folderId,omitempty"`
	ResourceKey string     `json:"resourceKey,omitempty"`
	ParentID    string     `json:"parentId,omitempty"`
	IsDir       bool       `json:"isDir"`
	File        drive.File `json:"file"`
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/api/file", s.handleFileMetadata)
	mux.HandleFunc("/api/list", s.handleList)
	mux.HandleFunc("/api/resolve", s.handleResolve)
	mux.HandleFunc("/api/export-formats", s.handleExportFormats)
	// GET patterns also match HEAD. Requests with other methods fall
	// through to the method-less "/" pattern, where handleRoot writes the
	// empty-bodied 405 these routes have always returned.
	mux.HandleFunc("GET /export/", s.handleExport)
	mux.HandleFunc("GET /file/", s.handleFile)
	mux.HandleFunc("GET /path/", s.handlePathFile)
	mux.HandleFunc("GET /browse/", s.handleBrowse)
	mux.HandleFunc("GET /browse", s.handleBrowse)
	mux.HandleFunc("/", s.handleRoot)
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := url.ParseQuery(r.URL.RawQuery); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid URL query"})
			return
		}
		mux.ServeHTTP(w, r)
	})
	if s.basePath != "" {
		handler = basePathMiddleware(s.basePath, handler)
	}
	if s.auth != nil {
		handler = s.authMiddleware(handler)
	}
	if s.allowOrigin != "" {
		handler = s.corsMiddleware(handler)
	}
	return handler
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(username), []byte(s.auth.username)) != 1 ||
			subtle.ConstantTimeCompare([]byte(password), []byte(s.auth.password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="gdrive-serve-lite"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		origin := r.Header.Get("Origin")
		if origin == "" || (s.allowOrigin != "*" && origin != s.allowOrigin) {
			next.ServeHTTP(w, r)
			return
		}
		headers := w.Header()
		headers.Set("Access-Control-Allow-Origin", s.allowOrigin)
		headers.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		headers.Set("Access-Control-Allow-Headers", "Authorization, Range, If-Match, If-None-Match, If-Modified-Since, If-Unmodified-Since, If-Range, Content-Type")
		headers.Set("Access-Control-Expose-Headers", strings.Join(readResponseHeaders, ", "))
		if s.allowOrigin != "*" {
			headers.Set("Access-Control-Allow-Credentials", "true")
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}
	p := cleanEscapedPath(escapedPath(r))
	if p == "/" {
		s.serveFolderPath(w, r, folderPath{Display: "/", Escaped: "/"}, s.root)
		return
	}
	item, err := s.resolveItemPath(r, p, "any")
	if err != nil {
		writeError(w, err)
		return
	}
	if item.File.IsFolder() {
		id, resourceKey := item.File.TargetIDAndKey()
		s.serveFolderPath(w, r, folderPath{Display: item.Path, Escaped: p}, folderRef{ID: id, ResourceKey: resourceKey})
		return
	}
	s.serveResolvedFile(w, r, item.File)
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	itemType := r.URL.Query().Get("type")
	if itemType == "" {
		itemType = "folder"
	}
	item, err := s.resolveItemPath(r, r.URL.Query().Get("path"), itemType)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resolveResponse(item))
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	opts, err := listOptions(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	folder, err := s.folderFromRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	opts.FolderID, opts.ResourceKey = folder.ID, folder.ResourceKey
	list, err := s.drive.ListChildren(r.Context(), opts)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.listResponse(r, "", folder, list))
}

// fileFromQuery fetches the file identified by the id and resourceKey query
// parameters; it writes the response itself when it returns false.
func (s *Server) fileFromQuery(w http.ResponseWriter, r *http.Request) (*drive.File, bool) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return nil, false
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		badRequest(w, "id is required")
		return nil, false
	}
	file, err := s.drive.GetFile(r.Context(), id, r.URL.Query().Get("resourceKey"))
	if err != nil {
		writeError(w, err)
		return nil, false
	}
	return file, true
}

func (s *Server) handleFileMetadata(w http.ResponseWriter, r *http.Request) {
	file, ok := s.fileFromQuery(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, file)
}

func (s *Server) handleExportFormats(w http.ResponseWriter, r *http.Request) {
	file, ok := s.fileFromQuery(w, r)
	if !ok {
		return
	}
	var defaultExport *drive.ExportFormat
	if format, ok := defaultExportForResponse(*file); ok {
		defaultExport = &format
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":            file.ID,
		"name":          file.Name,
		"mimeType":      file.MIMEType,
		"defaultExport": defaultExport,
		"formats":       exportFormatsForResponse(*file),
	})
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodHead && r.Header.Get("Range") != "" {
		badRequest(w, "Range is not supported for Drive exports")
		return
	}
	fileID, name, ok, err := idAndNameFromEscapedPath(r, "/export/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "invalid file ID", http.StatusBadRequest)
		return
	}

	file, err := s.drive.GetFile(r.Context(), fileID, r.URL.Query().Get("resourceKey"))
	if err != nil {
		writeError(w, err)
		return
	}
	format, err := exportFormatFromRequest(r, exportSourceFile(*file))
	if err != nil {
		writeError(w, err)
		return
	}
	if r.Method == http.MethodHead {
		meta, err := s.headSourceFile(r, *file)
		if err != nil {
			writeError(w, err)
			return
		}
		writeExportHead(w, r, meta, format, name)
		return
	}
	s.streamExport(w, r, *file, format, name, r.URL.Query().Get("resourceKey"))
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	fileID, name, ok, err := idAndNameFromEscapedPath(r, "/file/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "invalid file ID", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodHead {
		meta, err := s.drive.GetFile(r.Context(), fileID, r.URL.Query().Get("resourceKey"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeFileHead(w, r, *meta, name)
		return
	}
	s.proxyDownload(w, r, fileID, r.URL.Query().Get("resourceKey"), name)
}

func (s *Server) handlePathFile(w http.ResponseWriter, r *http.Request) {
	rest := stripRawPrefix(escapedPath(r), "/path/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	item, err := s.resolveItemPath(r, rest, "file")
	if err != nil {
		writeError(w, err)
		return
	}
	s.serveResolvedFile(w, r, item.File)
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	folder, err := s.folderFromRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	p := browsePath(r)
	s.serveFolderPath(w, r, folderPath{Display: displayPathFromEscaped(p), Escaped: p}, folder)
}

// serveResolvedFile streams a path-resolved file: Google Workspace files
// (and explicit format/mimeType requests) export, blobs download.
func (s *Server) serveResolvedFile(w http.ResponseWriter, r *http.Request, file drive.File) {
	if isWorkspaceReadable(file) || exportRequested(r) {
		// Reject ranges before format validation so a bad format never
		// masks the more actionable Range error.
		if r.Method != http.MethodHead && r.Header.Get("Range") != "" {
			badRequest(w, "Range is not supported for Drive exports")
			return
		}
		format, err := exportFormatFromRequest(r, exportSourceFile(file))
		if err != nil {
			writeError(w, err)
			return
		}
		if r.Method == http.MethodHead {
			meta, err := s.headSourceFile(r, file)
			if err != nil {
				writeError(w, err)
				return
			}
			writeExportHead(w, r, meta, format, file.Name)
			return
		}
		s.streamExport(w, r, file, format, file.Name, "")
		return
	}
	if r.Method == http.MethodHead {
		meta, err := s.headSourceFile(r, file)
		if err != nil {
			writeError(w, err)
			return
		}
		writeFileHead(w, r, meta, file.Name)
		return
	}
	id, resourceKey := file.TargetIDAndKey()
	s.proxyDownload(w, r, id, resourceKey, file.Name)
}

// HEAD describes a shortcut's target while retaining the alias download name.
func (s *Server) headSourceFile(r *http.Request, file drive.File) (drive.File, error) {
	id, resourceKey := file.TargetIDAndKey()
	if id == file.ID {
		return file, nil
	}
	target, err := s.drive.GetFile(r.Context(), id, resourceKey)
	if err != nil {
		return drive.File{}, err
	}
	target.Name = file.Name
	return *target, nil
}

func (s *Server) serveFolderPath(w http.ResponseWriter, r *http.Request, p folderPath, folder folderRef) {
	w.Header().Add("Vary", "Accept")
	jsonResponse, err := wantsJSON(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	if r.Method == http.MethodHead {
		contentType := "text/html; charset=utf-8"
		if jsonResponse {
			contentType = "application/json; charset=utf-8"
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		return
	}
	opts, err := listOptions(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	opts.FolderID, opts.ResourceKey = folder.ID, folder.ResourceKey
	list, err := s.drive.ListChildren(r.Context(), opts)
	if err != nil {
		writeError(w, err)
		return
	}
	if jsonResponse {
		writeJSON(w, http.StatusOK, s.listResponse(r, p.Display, folder, list))
		return
	}
	s.renderBrowse(w, r, p, folder, list)
}

// proxyDownload forwards a blob read to Drive and passes the response
// through, adding a Content-Disposition for the requested name when Drive
// does not supply one.
func (s *Server) proxyDownload(w http.ResponseWriter, r *http.Request, id, resourceKey, name string) {
	res, err := s.drive.Download(r.Context(), id, r.Header, resourceKey)
	if err != nil {
		writeError(w, err)
		return
	}
	streamResponse(w, res, "", name)
}

func (s *Server) streamExport(w http.ResponseWriter, r *http.Request, file drive.File, format drive.ExportFormat, name string, resourceKeyOverride string) {
	id, resourceKey := file.TargetIDAndKey()
	if resourceKeyOverride != "" && id == file.ID {
		resourceKey = resourceKeyOverride
	}
	res, err := s.drive.Export(r.Context(), id, format.MIMEType, resourceKey)
	if err != nil {
		writeError(w, err)
		return
	}
	if name == "" {
		name = file.Name
	}
	streamResponse(w, res, format.MIMEType, exportName(name, format))
}

func streamResponse(w http.ResponseWriter, res *http.Response, contentType, name string) {
	defer res.Body.Close()
	copyReadResponseHeaders(w.Header(), res.Header)
	if isReadSuccess(res.StatusCode) {
		if contentType != "" && w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", contentType)
		}
		if name != "" && w.Header().Get("Content-Disposition") == "" {
			w.Header().Set("Content-Disposition", contentDisposition(name))
		}
	}
	w.WriteHeader(res.StatusCode)
	if _, err := io.Copy(w, res.Body); err != nil {
		log.Printf("stream Drive response: %v", err)
		panic(http.ErrAbortHandler)
	}
}

func (s *Server) folderFromRequest(r *http.Request) (folderRef, error) {
	if id := r.URL.Query().Get("folderId"); id != "" {
		return folderRef{ID: id, ResourceKey: r.URL.Query().Get("resourceKey")}, nil
	}
	p := r.URL.Query().Get("path")
	if p == "" && strings.HasPrefix(r.URL.Path, "/browse") {
		p = browsePath(r)
	}
	if p == "" {
		return s.root, nil
	}
	item, err := s.resolveItemPath(r, p, "folder")
	if err != nil {
		return folderRef{}, err
	}
	id, resourceKey := item.File.TargetIDAndKey()
	return folderRef{ID: id, ResourceKey: resourceKey}, nil
}

func (s *Server) resolveItemPath(r *http.Request, p, itemType string) (*drive.ResolvedItem, error) {
	return s.drive.ResolveItemPath(r.Context(), p, s.root.ID, s.root.ResourceKey, itemType)
}

func listOptions(r *http.Request) (drive.ListOptions, error) {
	query := r.URL.Query()
	pageSize, err := intParam(query, "pageSize")
	if err != nil {
		return drive.ListOptions{}, err
	}
	limit, err := intParam(query, "limit")
	if err != nil {
		return drive.ListOptions{}, err
	}
	// Preserve the input: the canonical default "any" is not an accepted list type.
	if _, err := drive.NormalizeListType(query.Get("type")); err != nil {
		return drive.ListOptions{}, err
	}
	return drive.ListOptions{
		PageSize:  pageSize,
		Limit:     limit,
		PageToken: query.Get("pageToken"),
		OrderBy:   query.Get("orderBy"),
		Type:      query.Get("type"),
	}, nil
}

func (s *Server) renderBrowse(w http.ResponseWriter, r *http.Request, p folderPath, folder folderRef, list *drive.FileList) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><meta charset=\"utf-8\"><title>%s</title>", html.EscapeString(p.Display))
	fmt.Fprint(w, "<style>body{font:14px system-ui,sans-serif;margin:24px;max-width:1100px}table{border-collapse:collapse;width:100%}td,th{border-bottom:1px solid #ddd;padding:6px 8px;text-align:left}a{text-decoration:none}code{background:#f5f5f5;padding:2px 4px}</style>")
	fmt.Fprintf(w, "<h1>%s</h1><p><code>%s</code></p>", html.EscapeString(p.Display), html.EscapeString(folder.ID))
	fmt.Fprint(w, "<table><thead><tr><th>Name</th><th>Type</th><th>Size</th><th>Modified</th></tr></thead><tbody>")
	if p.Escaped != "/" {
		parent := path.Dir(strings.TrimRight(p.Escaped, "/"))
		if parent == "." {
			parent = "/"
		}
		fmt.Fprintf(w, "<tr><td><a href=\"%s\">..</a></td><td>folder</td><td></td><td></td></tr>", html.EscapeString(s.link("/browse"+parent)))
	}
	for _, f := range list.Files {
		name := html.EscapeString(f.Name)
		if f.IsFolder() {
			var link string
			if f.Name == "." || f.Name == ".." {
				link = s.entryURL(f)
			} else {
				childID, childResourceKey := f.TargetIDAndKey()
				link = withResourceKey(s.link("/browse"+urlEscapedPathAppend(p.Escaped, f.Name)+"?folderId="+url.QueryEscape(childID)), childResourceKey)
			}
			fmt.Fprintf(w, "<tr><td><a href=\"%s\">%s/</a></td><td>folder</td><td></td><td>%s</td></tr>", html.EscapeString(link), name, html.EscapeString(f.ModifiedTime))
			continue
		}
		fmt.Fprintf(w, "<tr><td><a href=\"%s\">%s</a></td><td>%s</td><td>%s</td><td>%s</td></tr>", html.EscapeString(s.fileLink(f)), name, html.EscapeString(f.MIMEType), html.EscapeString(f.Size), html.EscapeString(f.ModifiedTime))
	}
	fmt.Fprint(w, "</tbody></table>")
	if list.NextPageToken != "" {
		q := r.URL.Query()
		q.Del("resourceKey")
		q.Set("pageToken", list.NextPageToken)
		q.Set("folderId", folder.ID)
		if folder.ResourceKey != "" {
			q.Set("resourceKey", folder.ResourceKey)
		}
		next := s.link("/browse" + p.Escaped + "?" + q.Encode())
		fmt.Fprintf(w, "<p><a href=\"%s\">Next page</a></p>", html.EscapeString(next))
	}
}

func browsePath(r *http.Request) string {
	return cleanEscapedPath(stripRawPrefix(escapedPath(r), "/browse"))
}

func displayPathFromEscaped(p string) string {
	p = cleanEscapedPath(p)
	if p == "/" {
		return "/"
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i := range parts {
		if unescaped, err := url.PathUnescape(parts[i]); err == nil {
			parts[i] = unescaped
		}
	}
	return "/" + strings.Join(parts, "/")
}

func urlEscapedPathAppend(base, segment string) string {
	base = cleanEscapedPath(base)
	if base == "/" {
		return "/" + url.PathEscape(segment)
	}
	return strings.TrimRight(base, "/") + "/" + url.PathEscape(segment)
}

func cleanEscapedPath(p string) string {
	if p == "" || p == "." {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}

// escapedPath returns the path with percent-escapes intact, so escaped
// slashes in Drive names survive routing.
func escapedPath(r *http.Request) string {
	return r.URL.EscapedPath()
}

func basePathMiddleware(basePath string, next http.Handler) http.Handler {
	escapedBase := (&url.URL{Path: basePath}).EscapedPath()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != basePath && !strings.HasPrefix(r.URL.Path, basePath+"/") {
			http.NotFound(w, r)
			return
		}
		stripped := r.Clone(r.Context())
		strippedPath := strings.TrimPrefix(r.URL.Path, basePath)
		if strippedPath == "" {
			strippedPath = "/"
		}
		stripped.URL.Path = strippedPath
		stripped.URL.RawPath = ""
		if r.URL.RawPath != "" && r.URL.Path != basePath {
			// Normalize the matched separator so the stripped URL stays absolute.
			stripped.URL.RawPath = "/" + stripRawPrefix(r.URL.EscapedPath(), basePath+"/")
		}
		_, hadContentType := w.Header()["Content-Type"]
		next.ServeHTTP(&basePathResponseWriter{
			ResponseWriter:  w,
			request:         r,
			escapedBasePath: escapedBase,
			hadContentType:  hadContentType,
		}, stripped)
	})
}

// ServeMux computes canonical redirects after the request prefix is stripped.
// Re-emit them with the base path so both Location and the HTML link stay local.
type basePathResponseWriter struct {
	http.ResponseWriter
	request         *http.Request
	escapedBasePath string
	hadContentType  bool
	redirected      bool
}

func (w *basePathResponseWriter) WriteHeader(status int) {
	location := w.Header().Get("Location")
	if status >= 300 && status < 400 && strings.HasPrefix(location, "/") {
		w.redirected = true
		if !w.hadContentType {
			w.Header().Del("Content-Type")
		}
		http.Redirect(w.ResponseWriter, w.request, w.escapedBasePath+location, status)
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *basePathResponseWriter) Write(p []byte) (int, error) {
	if w.redirected {
		// The adjusted redirect already includes its response body.
		return len(p), nil
	}
	return w.ResponseWriter.Write(p)
}

func (w *basePathResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	if w.redirected {
		return io.Copy(io.Discard, r)
	}
	return io.Copy(w.ResponseWriter, r)
}

func (w *basePathResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// stripRawPrefix strips a prefix already matched against the decoded path.
// Each percent escape represents one byte, regardless of its spelling.
func stripRawPrefix(rawPath, prefix string) string {
	for range len(prefix) {
		width := 1
		if rawPath[0] == '%' {
			width = 3
		}
		rawPath = rawPath[width:]
	}
	return rawPath
}

func normalizeBasePath(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" || baseURL == "/" {
		return ""
	}
	if parsed, err := url.Parse(baseURL); err == nil && parsed.Path != "" {
		baseURL = parsed.Path
	}
	if !strings.HasPrefix(baseURL, "/") {
		baseURL = "/" + baseURL
	}
	baseURL = path.Clean(baseURL)
	if baseURL == "/" || baseURL == "." {
		return ""
	}
	return strings.TrimRight(baseURL, "/")
}

// link prefixes a server-generated link with the configured base path.
func (s *Server) link(target string) string {
	if s.basePath == "" {
		return target
	}
	if target != "" && !strings.HasPrefix(target, "/") {
		target = "/" + target
	}
	return (&url.URL{Path: s.basePath}).EscapedPath() + target
}

func idAndNameFromEscapedPath(r *http.Request, prefix string) (id, name string, ok bool, err error) {
	rest := stripRawPrefix(escapedPath(r), prefix)
	if rest == "" {
		return "", "", false, nil
	}
	rawID, rawName, _ := strings.Cut(rest, "/")
	id, err = url.PathUnescape(rawID)
	if err != nil || id == "" {
		return "", "", true, errors.New("invalid file ID")
	}
	name, err = url.PathUnescape(rawName)
	if err != nil {
		name = ""
	}
	if rawName == "" {
		name = r.URL.Query().Get("name")
	}
	return id, name, true, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	w.WriteHeader(http.StatusMethodNotAllowed)
}

func badRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var badReq *drive.BadRequestError
	var ambiguous *drive.AmbiguousPathError
	var apiErr *drive.APIError
	switch {
	case errors.Is(err, drive.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, drive.ErrUnsupportedExport), errors.As(err, &badReq):
		status = http.StatusBadRequest
	case errors.As(err, &ambiguous):
		status = http.StatusConflict
	case errors.As(err, &apiErr):
		status = apiErr.StatusCode
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// listResponse builds the JSON listing. For path-addressed requests (p
// non-empty) the next link keeps the request URL shape as-is; for
// ID-addressed requests it pins folderId so pagination stays on the
// resolved folder.
func (s *Server) listResponse(r *http.Request, p string, folder folderRef, list *drive.FileList) ListResponse {
	resp := ListResponse{
		Path:             p,
		FolderID:         folder.ID,
		ResourceKey:      folder.ResourceKey,
		Count:            len(list.Files),
		Files:            s.listFiles(list.Files, includeEntryURLs(r)),
		NextPageToken:    list.NextPageToken,
		IncompleteSearch: list.IncompleteSearch,
	}
	if list.NextPageToken != "" {
		q := r.URL.Query()
		q.Del("resourceKey")
		if p == "" {
			q.Set("folderId", folder.ID)
		}
		if folder.ResourceKey != "" && q.Get("folderId") != "" {
			q.Set("resourceKey", folder.ResourceKey)
		}
		q.Set("pageToken", list.NextPageToken)
		resp.Next = s.link(escapedPath(r) + "?" + q.Encode())
	}
	return resp
}

func (s *Server) listFiles(files []drive.File, includeURLs bool) []drive.File {
	if len(files) == 0 {
		return nil
	}
	if !includeURLs {
		return files
	}
	withURLs := make([]drive.File, len(files))
	for i, file := range files {
		withURLs[i] = file
		withURLs[i].URL = s.entryURL(file)
	}
	return withURLs
}

func (s *Server) entryURL(file drive.File) string {
	id, resourceKey := file.TargetIDAndKey()
	if id == "" {
		return ""
	}
	if file.IsFolder() {
		return withResourceKey(s.link("/browse?folderId="+url.QueryEscape(id)), resourceKey)
	}
	return s.fileLink(file)
}

// fileLink returns the read link for a non-folder entry: the export route
// when the file has a default export format, the blob route otherwise.
func (s *Server) fileLink(file drive.File) string {
	id, resourceKey := file.TargetIDAndKey()
	route := "/file/"
	if _, ok := defaultExportForResponse(file); ok {
		route = "/export/"
	}
	link := s.link(route + url.PathEscape(id))
	if file.Name == "." || file.Name == ".." {
		// Browsers normalize even percent-encoded dot segments in URL paths.
		link += "?name=" + url.QueryEscape(file.Name)
	} else {
		link += "/" + url.PathEscape(file.Name)
	}
	return withResourceKey(link, resourceKey)
}

func includeEntryURLs(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("links"))) {
	case "1", "true", "yes", "urls", "url":
		return true
	default:
		return false
	}
}

func resolveResponse(item *drive.ResolvedItem) ResolveResponse {
	id, resourceKey := item.File.TargetIDAndKey()
	resp := ResolveResponse{
		Path:        item.Path,
		ID:          item.File.ID,
		ResourceKey: resourceKey,
		ParentID:    item.ParentID,
		IsDir:       item.File.IsFolder(),
		File:        item.File,
	}
	if resp.IsDir {
		resp.FolderID = id
	}
	return resp
}

func writeFileHead(w http.ResponseWriter, r *http.Request, meta drive.File, name string) {
	if meta.Size != "" {
		if _, err := strconv.ParseUint(meta.Size, 10, 63); err != nil {
			writeError(w, errors.New("invalid file size in Drive metadata"))
			return
		}
	}
	if writeConditionalHead(w, r, meta.ModifiedTime) {
		return
	}
	if meta.MIMEType != "" {
		w.Header().Set("Content-Type", meta.MIMEType)
	}
	if meta.Size != "" {
		w.Header().Set("Content-Length", meta.Size)
	}
	if supportsRangeHead(meta) {
		w.Header().Set("Accept-Ranges", "bytes")
	}
	if name == "" {
		name = meta.Name
	}
	if name != "" {
		w.Header().Set("Content-Disposition", contentDisposition(name))
	}
	w.WriteHeader(http.StatusOK)
}

func writeExportHead(w http.ResponseWriter, r *http.Request, file drive.File, format drive.ExportFormat, name string) {
	if writeConditionalHead(w, r, file.ModifiedTime) {
		return
	}
	w.Header().Set("Content-Type", format.MIMEType)
	if name == "" {
		name = file.Name
	}
	if name != "" {
		w.Header().Set("Content-Disposition", contentDisposition(exportName(name, format)))
	}
	w.WriteHeader(http.StatusOK)
}

func supportsRangeHead(file drive.File) bool {
	return file.Size != "" && !isWorkspaceReadable(file) && !file.IsFolder()
}

// writeConditionalHead evaluates date preconditions in HTTP precedence order
// and sets Last-Modified when the Drive modification time is known.
func writeConditionalHead(w http.ResponseWriter, r *http.Request, modifiedTime string) bool {
	modified, ok := driveModifiedHTTPTime(modifiedTime)
	if !ok {
		return false
	}
	w.Header().Set("Last-Modified", modified.Format(http.TimeFormat))
	if raw := r.Header.Get("If-Unmodified-Since"); raw != "" && r.Header.Get("If-Match") == "" {
		if since, err := http.ParseTime(raw); err == nil && modified.After(since) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return true
		}
	}
	if r.Header.Get("If-None-Match") != "" {
		return false
	}
	since, err := http.ParseTime(r.Header.Get("If-Modified-Since"))
	if err != nil {
		return false
	}
	if modified.After(since) {
		return false
	}
	w.WriteHeader(http.StatusNotModified)
	return true
}

func driveModifiedHTTPTime(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	modified, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return modified.UTC().Truncate(time.Second), true
}

func exportRequested(r *http.Request) bool {
	query := r.URL.Query()
	return query.Get("format") != "" || query.Get("mimeType") != ""
}

func exportFormatFromRequest(r *http.Request, file drive.File) (drive.ExportFormat, error) {
	if !drive.IsWorkspaceFile(file) {
		return drive.ExportFormat{}, fmt.Errorf("%w: %q is not a Google Workspace file", drive.ErrUnsupportedExport, file.Name)
	}
	raw := r.URL.Query().Get("mimeType")
	if raw == "" {
		raw = r.URL.Query().Get("format")
	}
	if raw != "" {
		format, err := drive.ExportFormatFor(raw)
		if err != nil {
			return drive.ExportFormat{}, err
		}
		if !drive.SupportsExportFormat(file, format) {
			return drive.ExportFormat{}, fmt.Errorf("%w: MIME type %q is not supported for %q", drive.ErrUnsupportedExport, format.MIMEType, file.Name)
		}
		return format, nil
	}
	format, ok := drive.DefaultExportFormat(file)
	if !ok {
		return drive.ExportFormat{}, fmt.Errorf("%w: no default export format for MIME type %q", drive.ErrUnsupportedExport, file.MIMEType)
	}
	return format, nil
}

func exportName(name string, format drive.ExportFormat) string {
	if name == "" || format.Extension == "" {
		return name
	}
	suffix := "." + strings.ToLower(format.Extension)
	if strings.HasSuffix(strings.ToLower(name), suffix) {
		return name
	}
	return name + "." + format.Extension
}

func isWorkspaceReadable(file drive.File) bool {
	return drive.IsWorkspaceFile(exportSourceFile(file))
}

// exportSourceFile rewrites a shortcut as its target for export purposes,
// dropping export annotations that describe the shortcut itself.
func exportSourceFile(file drive.File) drive.File {
	if file.ShortcutDetails != nil && file.ShortcutDetails.TargetMIMEType != "" {
		file.MIMEType = file.ShortcutDetails.TargetMIMEType
		file.ExportLinks = nil
		file.ExportFormats = nil
		file.DefaultExport = nil
	}
	return file
}

func defaultExportForResponse(file drive.File) (drive.ExportFormat, bool) {
	return drive.DefaultExportFormat(exportSourceFile(file))
}

func exportFormatsForResponse(file drive.File) []drive.ExportFormat {
	return drive.SupportedExportFormats(exportSourceFile(file))
}

func intParam(query url.Values, name string) (int, error) {
	raw := query.Get(name)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return n, nil
}

func wantsJSON(r *http.Request) (bool, error) {
	query := r.URL.Query()
	if strings.EqualFold(query.Get("format"), "json") ||
		strings.EqualFold(query.Get("alt"), "json") ||
		strings.EqualFold(query.Get("json"), "1") {
		return true, nil
	}
	ranges, err := splitAcceptRanges(strings.Join(r.Header.Values("Accept"), ","))
	if err != nil {
		return false, err
	}
	wantsJSON := false
	for _, part := range ranges {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		mediaType, params, err := mime.ParseMediaType(part)
		if err != nil {
			return false, fmt.Errorf("invalid Accept media range %q: %w", part, err)
		}
		typeName, subtype, ok := strings.Cut(mediaType, "/")
		if !ok || (typeName == "*" && subtype != "*") {
			return false, fmt.Errorf("invalid Accept media range %q", part)
		}
		if raw, ok := params["q"]; ok {
			nonzero, err := nonzeroAcceptQuality(raw)
			if err != nil {
				return false, err
			}
			if !nonzero {
				continue
			}
		}
		if mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") {
			wantsJSON = true
		}
	}
	return wantsJSON, nil
}

func splitAcceptRanges(header string) ([]string, error) {
	var ranges []string
	start := 0
	quoted, escaped := false, false
	for i := 0; i < len(header); i++ {
		if escaped {
			escaped = false
			continue
		}
		switch header[i] {
		case '\\':
			escaped = quoted
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				ranges = append(ranges, header[start:i])
				start = i + 1
			}
		}
	}
	if quoted {
		return nil, errors.New("invalid Accept header: unterminated quoted string")
	}
	return append(ranges, header[start:]), nil
}

func nonzeroAcceptQuality(raw string) (bool, error) {
	const invalid = "invalid Accept quality %q: expected 0 to 1 with at most three decimal places"
	if len(raw) == 0 || len(raw) > 5 || (raw[0] != '0' && raw[0] != '1') || (len(raw) > 1 && raw[1] != '.') {
		return false, fmt.Errorf(invalid, raw)
	}
	nonzero := raw[0] == '1'
	for i := 2; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' || (raw[0] == '1' && raw[i] != '0') {
			return false, fmt.Errorf(invalid, raw)
		}
		nonzero = nonzero || raw[i] != '0'
	}
	return nonzero, nil
}

func withResourceKey(link, resourceKey string) string {
	if resourceKey == "" {
		return link
	}
	separator := "?"
	if strings.Contains(link, "?") {
		separator = "&"
	}
	return link + separator + "resourceKey=" + url.QueryEscape(resourceKey)
}

func copyReadResponseHeaders(dst, src http.Header) {
	for _, key := range readResponseHeaders {
		if values := src.Values(key); len(values) > 0 {
			dst.Del(key)
			for _, value := range values {
				dst.Add(key, value)
			}
		}
	}
}

func isReadSuccess(status int) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices
}

func contentDisposition(name string) string {
	if name == "" {
		return "inline"
	}
	// Extended filename parameters require percent-encoded spaces, not query '+'.
	return "inline; filename*=UTF-8''" + strings.ReplaceAll(url.QueryEscape(name), "+", "%20")
}
