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

	"gdrive-serve-lite/internal/drive"
)

type Server struct {
	drive        *drive.Client
	rootFolderID string
	auth         *basicAuth
}

type basicAuth struct {
	username string
	password string
}

type ListResponse struct {
	FolderID         string       `json:"folderId"`
	Count            int          `json:"count"`
	Files            []drive.File `json:"files"`
	NextPageToken    string       `json:"nextPageToken,omitempty"`
	Next             string       `json:"next,omitempty"`
	IncompleteSearch bool         `json:"incompleteSearch,omitempty"`
}

type ResolveResponse struct {
	Path     string     `json:"path"`
	ID       string     `json:"id"`
	FolderID string     `json:"folderId,omitempty"`
	ParentID string     `json:"parentId,omitempty"`
	IsDir    bool       `json:"isDir"`
	File     drive.File `json:"file"`
}

func New(client *drive.Client, rootFolderID string) *Server {
	return &Server{drive: client, rootFolderID: rootFolderID}
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
		s.serveFolderPath(w, r, p, s.rootFolderID)
		return
	}
	item, err := s.drive.ResolveItemPath(r.Context(), p, s.rootFolderID, "any")
	if err != nil {
		writeError(w, err)
		return
	}
	if item.File.MIMEType == drive.FolderMIME {
		s.serveFolderPath(w, r, item.Path, item.File.ID)
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
	item, err := s.drive.ResolveItemPath(r.Context(), p, s.rootFolderID, itemType)
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
	folderID, err := s.folderIDFromRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	opts := drive.ListOptions{
		FolderID:  folderID,
		PageSize:  intParam(r, "pageSize", 1000),
		Limit:     intParam(r, "limit", 0),
		PageToken: r.URL.Query().Get("pageToken"),
		OrderBy:   r.URL.Query().Get("orderBy"),
		Type:      r.URL.Query().Get("type"),
	}
	list, err := s.drive.ListChildren(r.Context(), opts)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse(r, folderID, list))
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
	writeJSON(w, http.StatusOK, map[string]any{
		"id":            file.ID,
		"name":          file.Name,
		"mimeType":      file.MIMEType,
		"defaultExport": file.DefaultExport,
		"formats":       file.ExportFormats,
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
	format, err := exportFormatFromRequest(r, *file)
	if err != nil {
		writeError(w, err)
		return
	}
	if r.Method == http.MethodHead {
		writeExportHead(w, *file, format, name)
		return
	}
	s.streamExport(w, r, *file, format, name)
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

	res, err := s.drive.DownloadWithResourceKey(r.Context(), r.Method, fileID, r.Header.Get("Range"), r.URL.Query().Get("resourceKey"))
	if err != nil {
		writeError(w, err)
		return
	}
	defer res.Body.Close()

	copyHeader(w.Header(), res.Header, "Content-Type")
	copyHeader(w.Header(), res.Header, "Content-Length")
	copyHeader(w.Header(), res.Header, "Content-Range")
	copyHeader(w.Header(), res.Header, "Accept-Ranges")
	copyHeader(w.Header(), res.Header, "ETag")
	copyHeader(w.Header(), res.Header, "Last-Modified")
	if name != "" && w.Header().Get("Content-Disposition") == "" {
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
	item, err := s.drive.ResolveItemPath(r.Context(), rest, s.rootFolderID, "file")
	if err != nil {
		writeError(w, err)
		return
	}
	s.serveResolvedFile(w, r, item.File, item.File.Name)
}

func (s *Server) serveResolvedFile(w http.ResponseWriter, r *http.Request, file drive.File, name string) {
	id, resourceKey := readableFileIDAndKey(file)
	if r.Method == http.MethodHead {
		if drive.IsWorkspaceFile(file) || exportRequested(r) {
			format, err := exportFormatFromRequest(r, file)
			if err != nil {
				writeError(w, err)
				return
			}
			writeExportHead(w, file, format, name)
			return
		}
		writeFileHead(w, file, name)
		return
	}
	if drive.IsWorkspaceFile(file) || exportRequested(r) {
		format, err := exportFormatFromRequest(r, file)
		if err != nil {
			writeError(w, err)
			return
		}
		s.streamExport(w, r, file, format, name)
		return
	}
	res, err := s.drive.DownloadWithResourceKey(r.Context(), r.Method, id, r.Header.Get("Range"), resourceKey)
	if err != nil {
		writeError(w, err)
		return
	}
	defer res.Body.Close()

	copyHeader(w.Header(), res.Header, "Content-Type")
	copyHeader(w.Header(), res.Header, "Content-Length")
	copyHeader(w.Header(), res.Header, "Content-Range")
	copyHeader(w.Header(), res.Header, "Accept-Ranges")
	copyHeader(w.Header(), res.Header, "ETag")
	copyHeader(w.Header(), res.Header, "Last-Modified")
	if name != "" && w.Header().Get("Content-Disposition") == "" {
		w.Header().Set("Content-Disposition", contentDisposition(name))
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
}

func (s *Server) serveFolderPath(w http.ResponseWriter, r *http.Request, p string, folderID string) {
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		return
	}
	list, err := s.drive.ListChildren(r.Context(), drive.ListOptions{
		FolderID:  folderID,
		PageSize:  intParam(r, "pageSize", 1000),
		Limit:     intParam(r, "limit", 0),
		PageToken: r.URL.Query().Get("pageToken"),
		OrderBy:   r.URL.Query().Get("orderBy"),
		Type:      r.URL.Query().Get("type"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	s.renderBrowse(w, r, p, folderID, list)
}

func (s *Server) handleFileHead(w http.ResponseWriter, r *http.Request, fileID string, name string) {
	meta, err := s.drive.GetFileWithResourceKey(r.Context(), fileID, r.URL.Query().Get("resourceKey"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeFileHead(w, *meta, name)
}

func (s *Server) streamExport(w http.ResponseWriter, r *http.Request, file drive.File, format drive.ExportFormat, name string) {
	if r.Header.Get("Range") != "" {
		writeError(w, errors.New("Range is not supported for Drive exports"))
		return
	}
	id, resourceKey := readableFileIDAndKey(file)
	res, err := s.drive.ExportWithResourceKey(r.Context(), id, format.MIMEType, resourceKey)
	if err != nil {
		writeError(w, err)
		return
	}
	defer res.Body.Close()

	copyHeader(w.Header(), res.Header, "Content-Type")
	copyHeader(w.Header(), res.Header, "Content-Length")
	copyHeader(w.Header(), res.Header, "ETag")
	copyHeader(w.Header(), res.Header, "Last-Modified")
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", format.MIMEType)
	}
	if name == "" {
		name = file.Name
	}
	if name != "" && w.Header().Get("Content-Disposition") == "" {
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
	folderID, err := s.folderIDFromRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	p := browsePath(r)
	list, err := s.drive.ListChildren(r.Context(), drive.ListOptions{
		FolderID:  folderID,
		PageSize:  intParam(r, "pageSize", 1000),
		Limit:     intParam(r, "limit", 0),
		PageToken: r.URL.Query().Get("pageToken"),
		OrderBy:   r.URL.Query().Get("orderBy"),
		Type:      r.URL.Query().Get("type"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	s.renderBrowse(w, r, p, folderID, list)
}

func (s *Server) folderIDFromRequest(r *http.Request) (string, error) {
	if id := r.URL.Query().Get("folderId"); id != "" {
		return id, nil
	}
	if p := r.URL.Query().Get("path"); p != "" {
		return s.drive.ResolvePath(r.Context(), p, s.rootFolderID)
	}
	if strings.HasPrefix(r.URL.Path, "/browse") {
		return s.drive.ResolvePath(r.Context(), browsePath(r), s.rootFolderID)
	}
	return s.rootFolderID, nil
}

func (s *Server) renderBrowse(w http.ResponseWriter, r *http.Request, p string, folderID string, list *drive.FileList) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><meta charset=\"utf-8\"><title>%s</title>", html.EscapeString(p))
	fmt.Fprint(w, "<style>body{font:14px system-ui,sans-serif;margin:24px;max-width:1100px}table{border-collapse:collapse;width:100%}td,th{border-bottom:1px solid #ddd;padding:6px 8px;text-align:left}a{text-decoration:none}code{background:#f5f5f5;padding:2px 4px}</style>")
	fmt.Fprintf(w, "<h1>%s</h1><p><code>%s</code></p>", html.EscapeString(p), html.EscapeString(folderID))
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
		if f.MIMEType == drive.FolderMIME {
			childPath := path.Join(p, f.Name)
			link := "/browse" + urlPath(childPath) + "?folderId=" + url.QueryEscape(f.ID)
			fmt.Fprintf(w, "<tr><td><a href=\"%s\">%s/</a></td><td>folder</td><td></td><td>%s</td></tr>", html.EscapeString(link), name, html.EscapeString(f.ModifiedTime))
			continue
		}
		link := "/file/" + url.PathEscape(f.ID) + "/" + url.PathEscape(f.Name)
		if f.DefaultExport != nil {
			link = "/export/" + url.PathEscape(f.ID) + "/" + url.PathEscape(exportName(f.Name, *f.DefaultExport))
		}
		fmt.Fprintf(w, "<tr><td><a href=\"%s\">%s</a></td><td>%s</td><td>%s</td><td>%s</td></tr>", html.EscapeString(link), name, html.EscapeString(f.MIMEType), html.EscapeString(f.Size), html.EscapeString(f.ModifiedTime))
	}
	fmt.Fprint(w, "</tbody></table>")
	if list.NextPageToken != "" {
		q := r.URL.Query()
		q.Set("pageToken", list.NextPageToken)
		q.Set("folderId", folderID)
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

func listResponse(r *http.Request, folderID string, list *drive.FileList) ListResponse {
	resp := ListResponse{
		FolderID:         folderID,
		Count:            len(list.Files),
		Files:            list.Files,
		NextPageToken:    list.NextPageToken,
		IncompleteSearch: list.IncompleteSearch,
	}
	if list.NextPageToken != "" {
		q := r.URL.Query()
		q.Set("folderId", folderID)
		q.Set("pageToken", list.NextPageToken)
		resp.Next = r.URL.Path + "?" + q.Encode()
	}
	return resp
}

func resolveResponse(item *drive.ResolvedItem) ResolveResponse {
	isDir := item.File.MIMEType == drive.FolderMIME
	resp := ResolveResponse{
		Path:     item.Path,
		ID:       item.File.ID,
		ParentID: item.ParentID,
		IsDir:    isDir,
		File:     item.File,
	}
	if isDir {
		resp.FolderID = item.File.ID
	}
	return resp
}

func writeFileHead(w http.ResponseWriter, meta drive.File, name string) {
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

func writeExportHead(w http.ResponseWriter, file drive.File, format drive.ExportFormat, name string) {
	w.Header().Set("Content-Type", format.MIMEType)
	if name == "" {
		name = file.Name
	}
	if name != "" {
		w.Header().Set("Content-Disposition", contentDisposition(exportName(name, format)))
	}
	w.WriteHeader(http.StatusOK)
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

func copyHeader(dst http.Header, src http.Header, key string) {
	if value := src.Get(key); value != "" {
		dst.Set(key, value)
	}
}

func contentDisposition(name string) string {
	if name == "" {
		return "inline"
	}
	return "inline; filename*=UTF-8''" + url.PathEscape(name)
}
