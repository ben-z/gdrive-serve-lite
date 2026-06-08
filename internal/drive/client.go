package drive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gdrive-serve-lite/internal/config"
)

const FolderMIME = "application/vnd.google-apps.folder"

const DefaultMetadataCacheTTL = 5 * time.Minute
const DefaultListCacheTTL = 30 * time.Second

var mediaRequestHeaders = []string{
	"Range",
	"If-Match",
	"If-None-Match",
	"If-Modified-Since",
	"If-Unmodified-Since",
	"If-Range",
}

var ErrNotFound = errors.New("not found")

type AmbiguousPathError struct {
	Segment string
	Parent  string
	Count   int
}

func (e *AmbiguousPathError) Error() string {
	return fmt.Sprintf("path segment %q is ambiguous under parent %q (%d matches)", e.Segment, e.Parent, e.Count)
}

type Client struct {
	cfg              config.Config
	token            config.OAuthToken
	httpClient       *http.Client
	mu               sync.Mutex
	metadataCacheTTL time.Duration
	listCacheTTL     time.Duration
	cacheMu          sync.RWMutex
	fileCache        map[string]cachedFile
	lookupCache      map[string]cachedFiles
	listCache        map[string]cachedList
}

type cachedFile struct {
	file      File
	expiresAt time.Time
}

type cachedFiles struct {
	files     []File
	expiresAt time.Time
}

type cachedList struct {
	list      FileList
	expiresAt time.Time
}

type APIError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return "Drive API " + e.Status
	}
	return "Drive API " + e.Status + ": " + e.Body
}

type File struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	MIMEType        string            `json:"mimeType"`
	Size            string            `json:"size,omitempty"`
	ModifiedTime    string            `json:"modifiedTime,omitempty"`
	MD5Checksum     string            `json:"md5Checksum,omitempty"`
	WebViewLink     string            `json:"webViewLink,omitempty"`
	WebContentLink  string            `json:"webContentLink,omitempty"`
	ResourceKey     string            `json:"resourceKey,omitempty"`
	ExportLinks     map[string]string `json:"exportLinks,omitempty"`
	ExportFormats   []ExportFormat    `json:"exportFormats,omitempty"`
	DefaultExport   *ExportFormat     `json:"defaultExport,omitempty"`
	ShortcutDetails *ShortcutDetails  `json:"shortcutDetails,omitempty"`
}

type ShortcutDetails struct {
	TargetID          string `json:"targetId,omitempty"`
	TargetMIMEType    string `json:"targetMimeType,omitempty"`
	TargetResourceKey string `json:"targetResourceKey,omitempty"`
}

type ResolvedItem struct {
	Path     string `json:"path"`
	ParentID string `json:"parentId,omitempty"`
	File     File   `json:"file"`
}

type FileList struct {
	Files            []File `json:"files"`
	NextPageToken    string `json:"nextPageToken,omitempty"`
	IncompleteSearch bool   `json:"incompleteSearch,omitempty"`
}

type ListOptions struct {
	FolderID    string
	ResourceKey string
	PageSize    int
	Limit       int
	PageToken   string
	OrderBy     string
	Type        string
}

func NewClient(cfg config.Config) *Client {
	return NewClientWithHTTPClient(cfg, http.DefaultClient)
}

func NewClientWithHTTPClient(cfg config.Config, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		cfg:              cfg,
		token:            cfg.Token,
		httpClient:       httpClient,
		metadataCacheTTL: DefaultMetadataCacheTTL,
		listCacheTTL:     DefaultListCacheTTL,
		fileCache:        map[string]cachedFile{},
		lookupCache:      map[string]cachedFiles{},
		listCache:        map[string]cachedList{},
	}
}

func (c *Client) SetMetadataCacheTTL(ttl time.Duration) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	c.metadataCacheTTL = ttl
	c.fileCache = map[string]cachedFile{}
	c.lookupCache = map[string]cachedFiles{}
}

func (c *Client) ClearMetadataCache() {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	c.fileCache = map[string]cachedFile{}
	c.lookupCache = map[string]cachedFiles{}
}

func (c *Client) SetListCacheTTL(ttl time.Duration) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	c.listCacheTTL = ttl
	c.listCache = map[string]cachedList{}
}

func (c *Client) ClearListCache() {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	c.listCache = map[string]cachedList{}
}

func (c *Client) ListChildren(ctx context.Context, opts ListOptions) (*FileList, error) {
	if opts.FolderID == "" {
		return nil, errors.New("folder ID is required")
	}
	pageSize := normalizePageSize(opts.PageSize)
	limit := normalizeLimit(opts.Limit, pageSize)
	listType, err := normalizeListType(opts.Type)
	if err != nil {
		return nil, err
	}
	opts.Type = listType
	cacheKey := listCacheKey(opts, pageSize, limit)
	if cached, ok := c.cachedList(cacheKey); ok {
		return &cached, nil
	}

	var combined FileList
	nextToken := opts.PageToken
	seenTokens := map[string]struct{}{}
	for pageRequests := 0; len(combined.Files) < limit; pageRequests++ {
		if pageRequests >= 10000 {
			return nil, errors.New("too many Drive list pages without reaching limit")
		}
		if nextToken != "" {
			if _, seen := seenTokens[nextToken]; seen {
				return nil, fmt.Errorf("Drive returned repeated page token %q", nextToken)
			}
			seenTokens[nextToken] = struct{}{}
		}
		remaining := limit - len(combined.Files)
		thisPageSize := pageSize
		if remaining < thisPageSize {
			thisPageSize = remaining
		}
		page, err := c.listChildrenPage(ctx, opts, nextToken, thisPageSize)
		if err != nil {
			return nil, err
		}
		combined.Files = append(combined.Files, page.Files...)
		combined.IncompleteSearch = combined.IncompleteSearch || page.IncompleteSearch
		combined.NextPageToken = page.NextPageToken
		if page.NextPageToken == "" {
			break
		}
		nextToken = page.NextPageToken
	}
	c.putCachedList(cacheKey, combined)
	return &combined, nil
}

func (c *Client) listChildrenPage(ctx context.Context, opts ListOptions, pageToken string, pageSize int) (*FileList, error) {
	q := "trashed=false and " + quote(opts.FolderID) + " in parents"
	switch opts.Type {
	case "all":
	case "files":
		q += " and " + nonFolderLikeQuery()
	case "folders":
		q += " and " + folderLikeQuery()
	default:
		return nil, fmt.Errorf("invalid list type %q", opts.Type)
	}

	values := c.baseListValues()
	values.Set("q", q)
	values.Set("fields", "nextPageToken,incompleteSearch,files(id,name,mimeType,size,modifiedTime,md5Checksum,webViewLink,webContentLink,resourceKey,exportLinks,shortcutDetails)")
	values.Set("pageSize", fmt.Sprintf("%d", pageSize))
	if pageToken != "" {
		values.Set("pageToken", pageToken)
	}
	if opts.OrderBy != "" {
		values.Set("orderBy", opts.OrderBy)
	}

	var out FileList
	if err := c.getJSONWithResourceKey(ctx, "https://www.googleapis.com/drive/v3/files?"+values.Encode(), opts.FolderID, opts.ResourceKey, &out); err != nil {
		return nil, err
	}
	annotateListExports(&out)
	return &out, nil
}

func (c *Client) ResolvePath(ctx context.Context, p string, rootFolderID string) (string, error) {
	return c.ResolvePathWithResourceKey(ctx, p, rootFolderID, "", "folder")
}

func (c *Client) ResolvePathWithResourceKey(ctx context.Context, p string, rootFolderID string, rootResourceKey string, itemType string) (string, error) {
	item, err := c.ResolveItemPathWithResourceKey(ctx, p, rootFolderID, rootResourceKey, itemType)
	if err != nil {
		return "", err
	}
	id, _ := ReadableFileIDAndResourceKey(item.File)
	return id, nil
}

func (c *Client) ResolveItemPath(ctx context.Context, p string, rootFolderID string, itemType string) (*ResolvedItem, error) {
	return c.ResolveItemPathWithResourceKey(ctx, p, rootFolderID, "", itemType)
}

func (c *Client) ResolveItemPathWithResourceKey(ctx context.Context, p string, rootFolderID string, rootResourceKey string, itemType string) (*ResolvedItem, error) {
	itemType, err := normalizeItemType(itemType)
	if err != nil {
		return nil, err
	}
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		if itemType == "file" {
			return nil, fmt.Errorf("%w: file at root path", ErrNotFound)
		}
		return &ResolvedItem{
			Path: "/",
			File: File{
				ID:          rootFolderID,
				Name:        "/",
				MIMEType:    FolderMIME,
				ResourceKey: rootResourceKey,
			},
		}, nil
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	parent := rootFolderID
	parentResourceKey := rootResourceKey
	resolvedParent := ""
	cleanParts := make([]string, 0, len(parts))
	var resolved File
	for i, raw := range parts {
		segment, err := url.PathUnescape(raw)
		if err != nil {
			return nil, err
		}
		if segment == "" {
			continue
		}
		cleanParts = append(cleanParts, segment)
		leafType := "folder"
		if i == len(parts)-1 {
			leafType = itemType
		}
		matches, err := c.findItemsByName(ctx, parent, parentResourceKey, segment, leafType)
		if err != nil {
			return nil, err
		}
		switch len(matches) {
		case 0:
			return nil, fmt.Errorf("%w: %s %q under %q", ErrNotFound, typeLabel(leafType), segment, parent)
		case 1:
			resolved = matches[0]
			resolvedParent = parent
			parent, parentResourceKey = ReadableFileIDAndResourceKey(matches[0])
		default:
			return nil, &AmbiguousPathError{Segment: segment, Parent: parent, Count: len(matches)}
		}
	}
	if len(cleanParts) == 0 {
		if itemType == "file" {
			return nil, fmt.Errorf("%w: file at root path", ErrNotFound)
		}
		return &ResolvedItem{
			Path: "/",
			File: File{
				ID:          rootFolderID,
				Name:        "/",
				MIMEType:    FolderMIME,
				ResourceKey: rootResourceKey,
			},
		}, nil
	}
	return &ResolvedItem{
		Path:     "/" + strings.Join(cleanParts, "/"),
		ParentID: resolvedParent,
		File:     resolved,
	}, nil
}

func (c *Client) GetFile(ctx context.Context, id string) (*File, error) {
	return c.GetFileWithResourceKey(ctx, id, "")
}

func (c *Client) GetFileWithResourceKey(ctx context.Context, id string, resourceKey string) (*File, error) {
	if id == "" {
		return nil, errors.New("file ID is required")
	}
	if cached, ok := c.cachedFile(fileCacheKey(id, resourceKey)); ok {
		return &cached, nil
	}
	values := c.baseGetValues()
	values.Set("fields", "id,name,mimeType,size,modifiedTime,md5Checksum,webViewLink,webContentLink,resourceKey,exportLinks,shortcutDetails")
	var out File
	err := c.getJSONWithResourceKey(ctx, "https://www.googleapis.com/drive/v3/files/"+url.PathEscape(id)+"?"+values.Encode(), id, resourceKey, &out)
	if err != nil {
		return nil, err
	}
	annotateFileExports(&out)
	c.putCachedFile(fileCacheKey(id, resourceKey), out)
	return &out, nil
}

func (c *Client) Download(ctx context.Context, method string, id string, rangeHeader string) (*http.Response, error) {
	return c.DownloadWithResourceKey(ctx, method, id, rangeHeader, "")
}

func (c *Client) DownloadWithResourceKey(ctx context.Context, method string, id string, rangeHeader string, resourceKey string) (*http.Response, error) {
	headers := http.Header{}
	if rangeHeader != "" {
		headers.Set("Range", rangeHeader)
	}
	return c.DownloadWithResourceKeyAndHeaders(ctx, method, id, headers, resourceKey)
}

func (c *Client) DownloadWithResourceKeyAndHeaders(ctx context.Context, method string, id string, headers http.Header, resourceKey string) (*http.Response, error) {
	if id == "" {
		return nil, errors.New("file ID is required")
	}
	if method == "" {
		method = http.MethodGet
	}
	values := c.baseGetValues()
	values.Set("alt", "media")
	req, err := http.NewRequestWithContext(ctx, method, "https://www.googleapis.com/drive/v3/files/"+url.PathEscape(id)+"?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	copySelectedHeaders(req.Header, headers, mediaRequestHeaders...)
	setResourceKeyHeader(req, id, resourceKey)
	return c.doRaw(req)
}

func (c *Client) Export(ctx context.Context, id string, mimeType string) (*http.Response, error) {
	return c.ExportWithResourceKey(ctx, id, mimeType, "")
}

func (c *Client) ExportWithResourceKey(ctx context.Context, id string, mimeType string, resourceKey string) (*http.Response, error) {
	if id == "" {
		return nil, errors.New("file ID is required")
	}
	if mimeType == "" {
		return nil, errors.New("export MIME type is required")
	}
	values := url.Values{}
	values.Set("mimeType", mimeType)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/drive/v3/files/"+url.PathEscape(id)+"/export?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	setResourceKeyHeader(req, id, resourceKey)
	return c.do(req)
}

func (c *Client) findItemsByName(ctx context.Context, parentID, parentResourceKey, name string, itemType string) ([]File, error) {
	itemType, err := normalizeItemType(itemType)
	if err != nil {
		return nil, err
	}
	cacheKey := lookupCacheKey(parentID, parentResourceKey, name, itemType)
	if cached, ok := c.cachedLookup(cacheKey); ok {
		return cached, nil
	}
	q := "trashed=false and " + quote(parentID) + " in parents and name = " + quote(name)
	switch itemType {
	case "any":
	case "file":
		q += " and " + nonFolderLikeQuery()
	case "folder":
		q += " and " + folderLikeQuery()
	}
	values := c.baseListValues()
	values.Set("q", q)
	values.Set("fields", "files(id,name,mimeType,size,modifiedTime,md5Checksum,webViewLink,webContentLink,resourceKey,exportLinks,shortcutDetails)")
	values.Set("pageSize", "2")
	var out FileList
	if err := c.getJSONWithResourceKey(ctx, "https://www.googleapis.com/drive/v3/files?"+values.Encode(), parentID, parentResourceKey, &out); err != nil {
		return nil, err
	}
	annotateListExports(&out)
	files := filterResolvedItems(out.Files, itemType)
	if len(files) == 1 {
		c.putCachedLookup(cacheKey, files)
	}
	return files, nil
}

func (c *Client) baseListValues() url.Values {
	values := url.Values{}
	values.Set("supportsAllDrives", "true")
	values.Set("includeItemsFromAllDrives", "true")
	values.Set("spaces", "drive")
	if c.cfg.DriveID != "" {
		values.Set("corpora", "drive")
		values.Set("driveId", c.cfg.DriveID)
	}
	return values
}

func (c *Client) baseGetValues() url.Values {
	values := url.Values{}
	values.Set("supportsAllDrives", "true")
	return values
}

func (c *Client) getJSON(ctx context.Context, requestURL string, out any) error {
	return c.getJSONWithResourceKey(ctx, requestURL, "", "", out)
}

func (c *Client) getJSONWithResourceKey(ctx context.Context, requestURL string, id string, resourceKey string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	setResourceKeyHeader(req, id, resourceKey)
	res, err := c.do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("decode Drive response: %w", err)
	}
	return nil
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	res, err := c.doRaw(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		defer res.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64*1024))
		return nil, &APIError{
			StatusCode: res.StatusCode,
			Status:     res.Status,
			Body:       strings.TrimSpace(string(body)),
		}
	}
	return res, nil
}

func (c *Client) doRaw(req *http.Request) (*http.Response, error) {
	token, err := c.accessToken(req.Context(), false)
	if err != nil {
		return nil, err
	}
	req = cloneRequest(req)
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusUnauthorized && c.canRefresh() {
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		token, err = c.accessToken(req.Context(), true)
		if err != nil {
			return nil, err
		}
		req = cloneRequest(req)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err = c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
	}
	return res, nil
}

func (c *Client) canRefresh() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token.RefreshToken != ""
}

func (c *Client) accessToken(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !force && c.token.AccessToken != "" && (c.token.Expiry.IsZero() || time.Until(c.token.Expiry) > time.Minute) {
		return c.token.AccessToken, nil
	}
	if c.token.RefreshToken == "" {
		if c.token.AccessToken != "" {
			return c.token.AccessToken, nil
		}
		return "", errors.New("missing access token and refresh token")
	}

	form := url.Values{}
	form.Set("client_id", c.cfg.ClientID)
	if c.cfg.ClientSecret != "" {
		form.Set("client_secret", c.cfg.ClientSecret)
	}
	form.Set("refresh_token", c.token.RefreshToken)
	form.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64*1024))
		return "", fmt.Errorf("refresh token failed: %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	var refreshed struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(res.Body).Decode(&refreshed); err != nil {
		return "", fmt.Errorf("decode token refresh: %w", err)
	}
	if refreshed.AccessToken == "" {
		return "", errors.New("token refresh returned empty access token")
	}
	c.token.AccessToken = refreshed.AccessToken
	c.token.TokenType = refreshed.TokenType
	if c.token.TokenType == "" {
		c.token.TokenType = "Bearer"
	}
	if refreshed.ExpiresIn > 0 {
		c.token.Expiry = time.Now().Add(time.Duration(refreshed.ExpiresIn) * time.Second)
	}
	return c.token.AccessToken, nil
}

func (c *Client) cachedFile(key string) (File, bool) {
	c.cacheMu.RLock()
	ttl := c.metadataCacheTTL
	entry, ok := c.fileCache[key]
	c.cacheMu.RUnlock()
	if ttl <= 0 || !ok {
		return File{}, false
	}
	if time.Now().After(entry.expiresAt) {
		c.cacheMu.Lock()
		if current, ok := c.fileCache[key]; ok && time.Now().After(current.expiresAt) {
			delete(c.fileCache, key)
		}
		c.cacheMu.Unlock()
		return File{}, false
	}
	return cloneFile(entry.file), true
}

func (c *Client) putCachedFile(key string, file File) {
	if file.ID == "" {
		return
	}
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if c.metadataCacheTTL <= 0 {
		return
	}
	c.fileCache[key] = cachedFile{
		file:      cloneFile(file),
		expiresAt: time.Now().Add(c.metadataCacheTTL),
	}
}

func (c *Client) cachedLookup(key string) ([]File, bool) {
	c.cacheMu.RLock()
	ttl := c.metadataCacheTTL
	entry, ok := c.lookupCache[key]
	c.cacheMu.RUnlock()
	if ttl <= 0 || !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		c.cacheMu.Lock()
		if current, ok := c.lookupCache[key]; ok && time.Now().After(current.expiresAt) {
			delete(c.lookupCache, key)
		}
		c.cacheMu.Unlock()
		return nil, false
	}
	return cloneFiles(entry.files), true
}

func (c *Client) putCachedLookup(key string, files []File) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if c.metadataCacheTTL <= 0 {
		return
	}
	c.lookupCache[key] = cachedFiles{
		files:     cloneFiles(files),
		expiresAt: time.Now().Add(c.metadataCacheTTL),
	}
}

func (c *Client) cachedList(key string) (FileList, bool) {
	c.cacheMu.RLock()
	ttl := c.listCacheTTL
	entry, ok := c.listCache[key]
	c.cacheMu.RUnlock()
	if ttl <= 0 || !ok {
		return FileList{}, false
	}
	if time.Now().After(entry.expiresAt) {
		c.cacheMu.Lock()
		if current, ok := c.listCache[key]; ok && time.Now().After(current.expiresAt) {
			delete(c.listCache, key)
		}
		c.cacheMu.Unlock()
		return FileList{}, false
	}
	return cloneFileList(entry.list), true
}

func (c *Client) putCachedList(key string, list FileList) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if c.listCacheTTL <= 0 {
		return
	}
	c.listCache[key] = cachedList{
		list:      cloneFileList(list),
		expiresAt: time.Now().Add(c.listCacheTTL),
	}
}

func lookupCacheKey(parentID, parentResourceKey, name, itemType string) string {
	return parentID + "\x00" + parentResourceKey + "\x00" + itemType + "\x00" + name
}

func fileCacheKey(id, resourceKey string) string {
	return id + "\x00" + resourceKey
}

func listCacheKey(opts ListOptions, pageSize int, limit int) string {
	return strings.Join([]string{
		opts.FolderID,
		opts.ResourceKey,
		opts.Type,
		opts.PageToken,
		opts.OrderBy,
		fmt.Sprintf("%d", pageSize),
		fmt.Sprintf("%d", limit),
	}, "\x00")
}

func cloneFileList(list FileList) FileList {
	return FileList{
		Files:            cloneFiles(list.Files),
		NextPageToken:    list.NextPageToken,
		IncompleteSearch: list.IncompleteSearch,
	}
}

func cloneFiles(files []File) []File {
	if len(files) == 0 {
		return nil
	}
	cloned := make([]File, len(files))
	for i := range files {
		cloned[i] = cloneFile(files[i])
	}
	return cloned
}

func cloneFile(file File) File {
	if file.ExportLinks != nil {
		links := make(map[string]string, len(file.ExportLinks))
		for key, value := range file.ExportLinks {
			links[key] = value
		}
		file.ExportLinks = links
	}
	if file.ExportFormats != nil {
		file.ExportFormats = append([]ExportFormat(nil), file.ExportFormats...)
	}
	if file.DefaultExport != nil {
		format := *file.DefaultExport
		file.DefaultExport = &format
	}
	if file.ShortcutDetails != nil {
		details := *file.ShortcutDetails
		file.ShortcutDetails = &details
	}
	return file
}

func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}

func normalizeListType(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "all":
		return "all", nil
	case "file", "files":
		return "files", nil
	case "folder", "folders", "dir", "dirs", "directory", "directories":
		return "folders", nil
	default:
		return "", fmt.Errorf("invalid list type %q", s)
	}
}

func normalizeItemType(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "any", "all":
		return "any", nil
	case "file", "files":
		return "file", nil
	case "folder", "folders", "dir", "dirs", "directory", "directories":
		return "folder", nil
	default:
		return "", fmt.Errorf("invalid item type %q", s)
	}
}

func typeLabel(itemType string) string {
	switch itemType {
	case "file":
		return "file"
	case "folder":
		return "folder"
	default:
		return "item"
	}
}

func filterResolvedItems(files []File, itemType string) []File {
	if itemType == "any" {
		return files
	}
	filtered := files[:0]
	for _, file := range files {
		isFolder := effectiveMIMEType(file) == FolderMIME
		if (itemType == "folder" && isFolder) || (itemType == "file" && !isFolder) {
			filtered = append(filtered, file)
		}
	}
	return filtered
}

func folderLikeQuery() string {
	return "(mimeType = " + quote(FolderMIME) + " or (mimeType = " + quote(ShortcutMIME) + " and shortcutDetails.targetMimeType = " + quote(FolderMIME) + "))"
}

func nonFolderLikeQuery() string {
	return "(mimeType != " + quote(FolderMIME) + " and (mimeType != " + quote(ShortcutMIME) + " or shortcutDetails.targetMimeType != " + quote(FolderMIME) + "))"
}

func effectiveMIMEType(file File) string {
	if file.ShortcutDetails != nil && file.ShortcutDetails.TargetMIMEType != "" {
		return file.ShortcutDetails.TargetMIMEType
	}
	return file.MIMEType
}

func normalizePageSize(n int) int {
	if n <= 0 {
		return 1000
	}
	if n > 1000 {
		return 1000
	}
	return n
}

func normalizeLimit(limit int, pageSize int) int {
	if limit <= 0 {
		return pageSize
	}
	if limit > 10000 {
		return 10000
	}
	return limit
}

func cloneRequest(req *http.Request) *http.Request {
	cloned := req.Clone(req.Context())
	cloned.Header = req.Header.Clone()
	return cloned
}

func copySelectedHeaders(dst http.Header, src http.Header, keys ...string) {
	for _, key := range keys {
		if value := src.Values(key); len(value) > 0 {
			dst.Del(key)
			for _, v := range value {
				dst.Add(key, v)
			}
		}
	}
}

func ReadableFileIDAndResourceKey(file File) (string, string) {
	if file.ShortcutDetails != nil && file.ShortcutDetails.TargetID != "" {
		return file.ShortcutDetails.TargetID, file.ShortcutDetails.TargetResourceKey
	}
	return file.ID, file.ResourceKey
}

func setResourceKeyHeader(req *http.Request, id string, resourceKey string) {
	if id == "" || resourceKey == "" {
		return
	}
	req.Header.Set("X-Goog-Drive-Resource-Keys", id+"/"+resourceKey)
}
