package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"gdrive-serve-lite/internal/drive"
)

type Server struct {
	drive *drive.Client
	root  folderRef
	auth  *basicAuth
}

type basicAuth struct {
	username string
	password string
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

type folderRef struct {
	ID          string
	ResourceKey string
}

func New(client *drive.Client, rootFolderID string) *Server {
	return NewWithRootResourceKey(client, rootFolderID, "")
}

func NewWithRootResourceKey(client *drive.Client, rootFolderID string, rootResourceKey string) *Server {
	return &Server{drive: client, root: folderRef{ID: rootFolderID, ResourceKey: rootResourceKey}}
}

func (s *Server) SetBasicAuth(username, password string) {
	if username == "" && password == "" {
		s.auth = nil
		return
	}
	s.auth = &basicAuth{username: username, password: password}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/api/file", s.handleFileMetadata)
	mux.HandleFunc("/api/list", s.handleList)
	mux.HandleFunc("/api/resolve", s.handleResolve)
	mux.HandleFunc("/api/export-formats", s.handleExportFormats)
	mux.HandleFunc("/export/", s.handleExport)
	mux.HandleFunc("/file/", s.handleFile)
	mux.HandleFunc("/path/", s.handlePathFile)
	mux.HandleFunc("/browse/", s.handleBrowse)
	mux.HandleFunc("/browse", s.handleBrowse)
	mux.HandleFunc("/", s.handleRoot)
	if s.auth == nil {
		return mux
	}
	return s.authMiddleware(mux)
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

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	p := compatiblePath(r)
	if p == "/" {
		s.serveFolderPath(w, r, p, s.root)
		return
	}
	item, err := s.resolveItemPath(r, p, "any")
	if err != nil {
		writeError(w, err)
		return
	}
	if isFolderLike(item.File) {
		id, resourceKey := readableFileIDAndKey(item.File)
		s.serveFolderPath(w, r, item.Path, folderRef{ID: id, ResourceKey: resourceKey})
		return
	}
	s.serveResolvedFile(w, r, item.File, item.File.Name)
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	p := r.URL.Query().Get("path")
	itemType := r.URL.Query().Get("type")
	if itemType == "" {
		itemType = "folder"
	}
	item, err := s.resolveItemPath(r, p, itemType)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resolveResponse(item))
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	folder, err := s.folderFromRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	opts := drive.ListOptions{
		FolderID:    folder.ID,
		ResourceKey: folder.ResourceKey,
		PageSize:    intParam(r, "pageSize", 1000),
		Limit:       intParam(r, "limit", 0),
		PageToken:   r.URL.Query().Get("pageToken"),
		OrderBy:     r.URL.Query().Get("orderBy"),
		Type:        r.URL.Query().Get("type"),
	}
	list, err := s.drive.ListChildren(r.Context(), opts)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse(r, folder, list))
}

func (s *Server) handleFileMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, errors.New("id is required"))
		return
	}
	file, err := s.drive.GetFileWithResourceKey(r.Context(), id, r.URL.Query().Get("resourceKey"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, file)
}

func (s *Server) handleExportFormats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, errors.New("id is required"))
		return
	}
	file, err := s.drive.GetFileWithResourceKey(r.Context(), id, r.URL.Query().Get("resourceKey"))
	if err != nil {
		writeError(w, err)
		return
	}
	defaultExport, hasDefaultExport := defaultExportForResponse(*file)
	var defaultExportValue *drive.ExportFormat
	if hasDefaultExport {
		defaultExportValue = &defaultExport
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":            file.ID,
		"name":          file.Name,
		"mimeType":      file.MIMEType,
		"defaultExport": defaultExportValue,
		"formats":       exportFormatsForResponse(*file),
	})
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/export/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	rawID, _, _ := strings.Cut(rest, "/")
	fileID, err := url.PathUnescape(rawID)
	if err != nil || fileID == "" {
		http.Error(w, "invalid file ID", http.StatusBadRequest)
		return
	}
	_, rawName, _ := strings.Cut(rest, "/")
	name, _ := url.PathUnescape(rawName)

	file, err := s.drive.GetFileWithResourceKey(r.Context(), fileID, r.URL.Query().Get("resourceKey"))
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
		writeExportHead(w, r, *file, format, name)
		return
	}
	s.streamExport(w, r, *file, format, name, r.URL.Query().Get("resourceKey"))
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/file/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	rawID, _, _ := strings.Cut(rest, "/")
	fileID, err := url.PathUnescape(rawID)
	if err != nil || fileID == "" {
		http.Error(w, "invalid file ID", http.StatusBadRequest)
		return
	}
	_, rawName, _ := strings.Cut(rest, "/")
	name, _ := url.PathUnescape(rawName)

	if r.Method == http.MethodHead {
		s.handleFileHead(w, r, fileID, name)
		return
	}

	res, err := s.drive.DownloadWithResourceKeyAndHeaders(r.Context(), r.Method, fileID, r.Header, r.URL.Query().Get("resourceKey"))
	if err != nil {
		writeError(w, err)
		return
	}
	defer res.Body.Close()

	copyReadResponseHeaders(w.Header(), res.Header)
	if isReadSuccess(res.StatusCode) && name != "" && w.Header().Get("Content-Disposition") == "" {
		w.Header().Set("Content-Disposition", contentDisposition(name))
	}
	w.WriteHeader(res.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, res.Body)
	}
}

func (s *Server) handlePathFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rest := pathRest(r, "/path/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	item, err := s.resolveItemPath(r, rest, "file")
	if err != nil {
		writeError(w, err)
		return
	}
	s.serveResolvedFile(w, r, item.File, item.File.Name)
}

func (s *Server) serveResolvedFile(w http.ResponseWriter, r *http.Request, file drive.File, name string) {
	id, resourceKey := readableFileIDAndKey(file)
	if r.Method == http.MethodHead {
		if isWorkspaceReadable(file) || exportRequested(r) {
			format, err := exportFormatFromRequest(r, exportSourceFile(file))
			if err != nil {
				writeError(w, err)
				return
			}
			writeExportHead(w, r, file, format, name)
			return
		}
		writeFileHead(w, r, file, name)
		return
	}
	if isWorkspaceReadable(file) || exportRequested(r) {
		format, err := exportFormatFromRequest(r, exportSourceFile(file))
		if err != nil {
			writeError(w, err)
			return
		}
		s.streamExport(w, r, file, format, name, "")
		return
	}
	res, err := s.drive.DownloadWithResourceKeyAndHeaders(r.Context(), r.Method, id, r.Header, resourceKey)
	if err != nil {
		writeError(w, err)
		return
	}
	defer res.Body.Close()

	copyReadResponseHeaders(w.Header(), res.Header)
	if isReadSuccess(res.StatusCode) && name != "" && w.Header().Get("Content-Disposition") == "" {
		w.Header().Set("Content-Disposition", contentDisposition(name))
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
}

func (s *Server) serveFolderPath(w http.ResponseWriter, r *http.Request, p string, folder folderRef) {
	if r.Method == http.MethodHead {
		if wantsJSON(r) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	list, err := s.drive.ListChildren(r.Context(), drive.ListOptions{
		FolderID:    folder.ID,
		ResourceKey: folder.ResourceKey,
		PageSize:    intParam(r, "pageSize", 1000),
		Limit:       intParam(r, "limit", 0),
		PageToken:   r.URL.Query().Get("pageToken"),
		OrderBy:     r.URL.Query().Get("orderBy"),
		Type:        r.URL.Query().Get("type"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, listResponseWithPath(r, p, folder, list))
		return
	}
	s.renderBrowse(w, r, p, folder, list)
}

func (s *Server) handleFileHead(w http.ResponseWriter, r *http.Request, fileID string, name string) {
	meta, err := s.drive.GetFileWithResourceKey(r.Context(), fileID, r.URL.Query().Get("resourceKey"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeFileHead(w, r, *meta, name)
}

func (s *Server) streamExport(w http.ResponseWriter, r *http.Request, file drive.File, format drive.ExportFormat, name string, resourceKeyOverride string) {
	if r.Header.Get("Range") != "" {
		writeError(w, errors.New("Range is not supported for Drive exports"))
		return
	}
	id, resourceKey := readableFileIDAndKey(file)
	if resourceKeyOverride != "" && id == file.ID {
		resourceKey = resourceKeyOverride
	}
	res, err := s.drive.ExportWithResourceKey(r.Context(), id, format.MIMEType, resourceKey)
	if err != nil {
		writeError(w, err)
		return
	}
	defer res.Body.Close()

	copyReadResponseHeaders(w.Header(), res.Header)
	if isReadSuccess(res.StatusCode) && w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", format.MIMEType)
	}
	if name == "" {
		name = file.Name
	}
	if isReadSuccess(res.StatusCode) && name != "" && w.Header().Get("Content-Disposition") == "" {
		w.Header().Set("Content-Disposition", contentDisposition(exportName(name, format)))
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	folder, err := s.folderFromRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	p := browsePath(r)
	list, err := s.drive.ListChildren(r.Context(), drive.ListOptions{
		FolderID:    folder.ID,
		ResourceKey: folder.ResourceKey,
		PageSize:    intParam(r, "pageSize", 1000),
		Limit:       intParam(r, "limit", 0),
		PageToken:   r.URL.Query().Get("pageToken"),
		OrderBy:     r.URL.Query().Get("orderBy"),
		Type:        r.URL.Query().Get("type"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, listResponseWithPath(r, p, folder, list))
		return
	}
	s.renderBrowse(w, r, p, folder, list)
}

func (s *Server) folderFromRequest(r *http.Request) (folderRef, error) {
	if id := r.URL.Query().Get("folderId"); id != "" {
		return folderRef{ID: id, ResourceKey: r.URL.Query().Get("resourceKey")}, nil
	}
	if p := r.URL.Query().Get("path"); p != "" {
		item, err := s.resolveItemPath(r, p, "folder")
		if err != nil {
			return folderRef{}, err
		}
		id, resourceKey := readableFileIDAndKey(item.File)
		return folderRef{ID: id, ResourceKey: resourceKey}, nil
	}
	if strings.HasPrefix(r.URL.Path, "/browse") {
		item, err := s.resolveItemPath(r, browsePath(r), "folder")
		if err != nil {
			return folderRef{}, err
		}
		id, resourceKey := readableFileIDAndKey(item.File)
		return folderRef{ID: id, ResourceKey: resourceKey}, nil
	}
	return s.root, nil
}

func (s *Server) resolveItemPath(r *http.Request, p string, itemType string) (*drive.ResolvedItem, error) {
	return s.drive.ResolveItemPathWithResourceKey(r.Context(), p, s.root.ID, s.root.ResourceKey, itemType)
}

func (s *Server) renderBrowse(w http.ResponseWriter, r *http.Request, p string, folder folderRef, list *drive.FileList) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><meta charset=\"utf-8\"><title>%s</title>", html.EscapeString(p))
	fmt.Fprint(w, "<style>body{font:14px system-ui,sans-serif;margin:24px;max-width:1100px}table{border-collapse:collapse;width:100%}td,th{border-bottom:1px solid #ddd;padding:6px 8px;text-align:left}a{text-decoration:none}code{background:#f5f5f5;padding:2px 4px}</style>")
	fmt.Fprintf(w, "<h1>%s</h1><p><code>%s</code></p>", html.EscapeString(p), html.EscapeString(folder.ID))
	fmt.Fprint(w, "<table><thead><tr><th>Name</th><th>Type</th><th>Size</th><th>Modified</th></tr></thead><tbody>")
	if p != "/" {
		parent := path.Dir(strings.TrimRight(p, "/"))
		if parent == "." {
			parent = "/"
		}
		fmt.Fprintf(w, "<tr><td><a href=\"%s\">..</a></td><td>folder</td><td></td><td></td></tr>", html.EscapeString("/browse"+urlPath(parent)))
	}
	for _, f := range list.Files {
		name := html.EscapeString(f.Name)
		if isFolderLike(f) {
			childPath := path.Join(p, f.Name)
			childID, childResourceKey := readableFileIDAndKey(f)
			link := withResourceKey("/browse"+urlPath(childPath)+"?folderId="+url.QueryEscape(childID), childResourceKey)
			fmt.Fprintf(w, "<tr><td><a href=\"%s\">%s/</a></td><td>folder</td><td></td><td>%s</td></tr>", html.EscapeString(link), name, html.EscapeString(f.ModifiedTime))
			continue
		}
		fileID, resourceKey := readableFileIDAndKey(f)
		link := "/file/" + url.PathEscape(fileID) + "/" + url.PathEscape(f.Name)
		if format, ok := defaultExportForResponse(f); ok {
			link = "/export/" + url.PathEscape(fileID) + "/" + url.PathEscape(exportName(f.Name, format))
		}
		link = withResourceKey(link, resourceKey)
		fmt.Fprintf(w, "<tr><td><a href=\"%s\">%s</a></td><td>%s</td><td>%s</td><td>%s</td></tr>", html.EscapeString(link), name, html.EscapeString(f.MIMEType), html.EscapeString(f.Size), html.EscapeString(f.ModifiedTime))
	}
	fmt.Fprint(w, "</tbody></table>")
	if list.NextPageToken != "" {
		q := r.URL.Query()
		q.Set("pageToken", list.NextPageToken)
		q.Set("folderId", folder.ID)
		if folder.ResourceKey != "" {
			q.Set("resourceKey", folder.ResourceKey)
		}
		next := r.URL.Path + "?" + q.Encode()
		fmt.Fprintf(w, "<p><a href=\"%s\">Next page</a></p>", html.EscapeString(next))
	}
}

func browsePath(r *http.Request) string {
	p := strings.TrimPrefix(r.URL.Path, "/browse")
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}

func urlPath(p string) string {
	if p == "/" || p == "." {
		return "/"
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "/" + strings.Join(parts, "/")
}

func pathRest(r *http.Request, prefix string) string {
	p := r.URL.EscapedPath()
	if p == "" {
		p = r.URL.Path
	}
	return strings.TrimPrefix(p, prefix)
}

func compatiblePath(r *http.Request) string {
	p := r.URL.EscapedPath()
	if p == "" {
		p = r.URL.Path
	}
	if p == "" || p == "/" {
		return "/"
	}
	return path.Clean("/" + strings.TrimPrefix(p, "/"))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, drive.ErrNotFound) {
		status = http.StatusNotFound
	}
	if errors.Is(err, drive.ErrUnsupportedExport) {
		status = http.StatusBadRequest
	}
	var ambiguous *drive.AmbiguousPathError
	if errors.As(err, &ambiguous) {
		status = http.StatusConflict
	}
	if strings.Contains(err.Error(), "folderId or path is required") ||
		strings.Contains(err.Error(), "invalid list type") ||
		strings.Contains(err.Error(), "invalid item type") ||
		strings.Contains(err.Error(), "Range is not supported") {
		status = http.StatusBadRequest
	}
	if strings.Contains(err.Error(), "id is required") {
		status = http.StatusBadRequest
	}
	var apiErr *drive.APIError
	if errors.As(err, &apiErr) {
		status = apiErr.StatusCode
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func listResponse(r *http.Request, folder folderRef, list *drive.FileList) ListResponse {
	resp := ListResponse{
		FolderID:         folder.ID,
		ResourceKey:      folder.ResourceKey,
		Count:            len(list.Files),
		Files:            list.Files,
		NextPageToken:    list.NextPageToken,
		IncompleteSearch: list.IncompleteSearch,
	}
	if list.NextPageToken != "" {
		q := r.URL.Query()
		q.Set("folderId", folder.ID)
		if folder.ResourceKey != "" {
			q.Set("resourceKey", folder.ResourceKey)
		}
		q.Set("pageToken", list.NextPageToken)
		resp.Next = r.URL.Path + "?" + q.Encode()
	}
	return resp
}

func listResponseWithPath(r *http.Request, p string, folder folderRef, list *drive.FileList) ListResponse {
	resp := listResponse(r, folder, list)
	resp.Path = p
	if list.NextPageToken != "" {
		q := r.URL.Query()
		if folder.ResourceKey != "" && q.Get("folderId") != "" {
			q.Set("resourceKey", folder.ResourceKey)
		}
		q.Set("pageToken", list.NextPageToken)
		resp.Next = r.URL.Path + "?" + q.Encode()
	}
	return resp
}

func resolveResponse(item *drive.ResolvedItem) ResolveResponse {
	isDir := isFolderLike(item.File)
	id, resourceKey := readableFileIDAndKey(item.File)
	resp := ResolveResponse{
		Path:        item.Path,
		ID:          item.File.ID,
		ResourceKey: resourceKey,
		ParentID:    item.ParentID,
		IsDir:       isDir,
		File:        item.File,
	}
	if isDir {
		resp.FolderID = id
	}
	return resp
}

func writeFileHead(w http.ResponseWriter, r *http.Request, meta drive.File, name string) {
	if writeNotModifiedHead(w, r, meta.ModifiedTime) {
		return
	}
	if meta.MIMEType != "" {
		w.Header().Set("Content-Type", meta.MIMEType)
	}
	if meta.Size != "" {
		w.Header().Set("Content-Length", meta.Size)
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
	if writeNotModifiedHead(w, r, file.ModifiedTime) {
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

func writeNotModifiedHead(w http.ResponseWriter, r *http.Request, modifiedTime string) bool {
	modified, ok := driveModifiedHTTPTime(modifiedTime)
	if !ok {
		return false
	}
	w.Header().Set("Last-Modified", modified.Format(http.TimeFormat))
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
	return r.URL.Query().Get("format") != "" || r.URL.Query().Get("mimeType") != ""
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
		return drive.ExportFormatFor(raw)
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

func isFolderLike(file drive.File) bool {
	return effectiveMIMEType(file) == drive.FolderMIME
}

func isWorkspaceReadable(file drive.File) bool {
	return drive.IsWorkspaceFile(exportSourceFile(file))
}

func exportSourceFile(file drive.File) drive.File {
	if file.ShortcutDetails != nil && file.ShortcutDetails.TargetMIMEType != "" {
		file.MIMEType = file.ShortcutDetails.TargetMIMEType
		file.ExportLinks = nil
		file.ExportFormats = nil
		file.DefaultExport = nil
	}
	return file
}

func effectiveMIMEType(file drive.File) string {
	if file.ShortcutDetails != nil && file.ShortcutDetails.TargetMIMEType != "" {
		return file.ShortcutDetails.TargetMIMEType
	}
	return file.MIMEType
}

func defaultExportForResponse(file drive.File) (drive.ExportFormat, bool) {
	return drive.DefaultExportFormat(exportSourceFile(file))
}

func exportFormatsForResponse(file drive.File) []drive.ExportFormat {
	return drive.SupportedExportFormats(exportSourceFile(file))
}

func readableFileIDAndKey(file drive.File) (string, string) {
	if file.ShortcutDetails != nil && file.ShortcutDetails.TargetID != "" {
		return file.ShortcutDetails.TargetID, file.ShortcutDetails.TargetResourceKey
	}
	return file.ID, file.ResourceKey
}

func intParam(r *http.Request, name string, fallback int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

func wantsJSON(r *http.Request) bool {
	if strings.EqualFold(r.URL.Query().Get("format"), "json") ||
		strings.EqualFold(r.URL.Query().Get("alt"), "json") ||
		strings.EqualFold(r.URL.Query().Get("json"), "1") {
		return true
	}
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		mediaType := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") {
			return true
		}
	}
	return false
}

func withResourceKey(link string, resourceKey string) string {
	if resourceKey == "" {
		return link
	}
	separator := "?"
	if strings.Contains(link, "?") {
		separator = "&"
	}
	return link + separator + "resourceKey=" + url.QueryEscape(resourceKey)
}

func copyHeader(dst http.Header, src http.Header, key string) {
	if value := src.Get(key); value != "" {
		dst.Set(key, value)
	}
}

func copyReadResponseHeaders(dst http.Header, src http.Header) {
	for _, key := range readResponseHeaders {
		copyHeader(dst, src, key)
	}
}

func isReadSuccess(status int) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices
}

func contentDisposition(name string) string {
	if name == "" {
		return "inline"
	}
	return "inline; filename*=UTF-8''" + url.PathEscape(name)
}
