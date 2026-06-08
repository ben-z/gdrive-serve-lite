package server

import (
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
}

func New(client *drive.Client, rootFolderID string) *Server {
	return &Server{drive: client, rootFolderID: rootFolderID}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/api/list", s.handleList)
	mux.HandleFunc("/api/resolve", s.handleResolve)
	mux.HandleFunc("/file/", s.handleFile)
	mux.HandleFunc("/browse/", s.handleBrowse)
	mux.HandleFunc("/browse", s.handleBrowse)
	mux.HandleFunc("/", s.handleRoot)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/browse/", http.StatusFound)
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	p := r.URL.Query().Get("path")
	id, err := s.drive.ResolvePath(r.Context(), p, s.rootFolderID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"path":     p,
		"folderId": id,
	})
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
		PageToken: r.URL.Query().Get("pageToken"),
		OrderBy:   r.URL.Query().Get("orderBy"),
		Type:      r.URL.Query().Get("type"),
	}
	list, err := s.drive.ListChildren(r.Context(), opts)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
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

	meta, err := s.drive.GetFile(r.Context(), fileID)
	if err != nil {
		writeError(w, err)
		return
	}
	if meta.MIMEType == drive.FolderMIME {
		http.Error(w, "folders cannot be downloaded", http.StatusBadRequest)
		return
	}
	if strings.HasPrefix(meta.MIMEType, "application/vnd.google-apps.") {
		http.Error(w, "Google Workspace document export is not implemented yet", http.StatusNotImplemented)
		return
	}

	res, err := s.drive.Download(r.Context(), r.Method, fileID, r.Header.Get("Range"))
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
	if w.Header().Get("Content-Disposition") == "" {
		w.Header().Set("Content-Disposition", contentDisposition(meta.Name))
	}
	w.WriteHeader(res.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, res.Body)
	}
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
	return "", errors.New("folderId or path is required")
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
	var ambiguous *drive.AmbiguousPathError
	if errors.As(err, &ambiguous) {
		status = http.StatusConflict
	}
	if strings.Contains(err.Error(), "folderId or path is required") || strings.Contains(err.Error(), "invalid list type") {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
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
