package drive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"gdrive-serve-lite/internal/config"
)

const FolderMIME = "application/vnd.google-apps.folder"

const (
	DefaultMetadataCacheTTL = 5 * time.Minute
	DefaultListCacheTTL     = 30 * time.Second
)

// fileFields is the Drive metadata field set requested by every read in this
// package, so all routes see the same file shape.
const fileFields = "id,name,mimeType,size,modifiedTime,md5Checksum,webViewLink,webContentLink,resourceKey,exportLinks,shortcutDetails"

var ErrNotFound = errors.New("not found")

// BadRequestError reports an invalid caller-supplied argument.
type BadRequestError struct{ msg string }

func (e *BadRequestError) Error() string { return e.msg }

type AmbiguousPathError struct {
	Segment string
	Parent  string
	Count   int
}

func (e *AmbiguousPathError) Error() string {
	return fmt.Sprintf("path segment %q is ambiguous under parent %q (%d matches)", e.Segment, e.Parent, e.Count)
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
	URL             string            `json:"url,omitempty"`
}

type ShortcutDetails struct {
	TargetID          string `json:"targetId,omitempty"`
	TargetMIMEType    string `json:"targetMimeType,omitempty"`
	TargetResourceKey string `json:"targetResourceKey,omitempty"`
}

// EffectiveMIMEType returns the MIME type the file acts as, following
// shortcut targets.
func (f File) EffectiveMIMEType() string {
	if f.ShortcutDetails != nil && f.ShortcutDetails.TargetMIMEType != "" {
		return f.ShortcutDetails.TargetMIMEType
	}
	return f.MIMEType
}

// IsFolder reports whether the file behaves as a folder; shortcuts to
// folders count as folders.
func (f File) IsFolder() bool {
	return f.EffectiveMIMEType() == FolderMIME
}

// TargetIDAndKey returns the file ID and resource key to use when reading
// the file, following shortcut targets.
func (f File) TargetIDAndKey() (id, resourceKey string) {
	if f.ShortcutDetails != nil && f.ShortcutDetails.TargetID != "" {
		return f.ShortcutDetails.TargetID, f.ShortcutDetails.TargetResourceKey
	}
	return f.ID, f.ResourceKey
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

type Client struct {
	clientID     string
	clientSecret string
	driveID      string
	httpClient   *http.Client

	mu    sync.Mutex
	token config.OAuthToken

	files   *ttlCache[File]
	lookups *ttlCache[[]File]
	lists   *ttlCache[FileList]
}

func NewClient(cfg config.Config, httpClient *http.Client) *Client {
	return &Client{
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		driveID:      cfg.DriveID,
		httpClient:   httpClient,
		token:        cfg.Token,
		files:        newTTLCache(DefaultMetadataCacheTTL, cloneFile),
		lookups:      newTTLCache(DefaultMetadataCacheTTL, cloneFiles),
		lists:        newTTLCache(DefaultListCacheTTL, cloneFileList),
	}
}

// NewHTTPClient returns an http.Client that passes Drive responses through
// raw: no transparent decompression, with a bound on response header waits.
func NewHTTPClient(responseHeaderTimeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	transport.DisableCompression = true
	return &http.Client{Transport: transport}
}

// SetMetadataCacheTTL sets the TTL for file metadata and path-segment
// lookups and drops both caches; zero or less disables them.
func (c *Client) SetMetadataCacheTTL(ttl time.Duration) {
	c.files.reset(ttl)
	c.lookups.reset(ttl)
}

// SetListCacheTTL sets the TTL for exact-query list responses and drops the
// cache; zero or less disables it.
func (c *Client) SetListCacheTTL(ttl time.Duration) {
	c.lists.reset(ttl)
}

func (c *Client) ListChildren(ctx context.Context, opts ListOptions) (*FileList, error) {
	if opts.FolderID == "" {
		return nil, errors.New("folder ID is required")
	}
	listType, err := normalizeListType(opts.Type)
	if err != nil {
		return nil, err
	}
	opts.Type = listType
	pageSize := normalizePageSize(opts.PageSize)
	limit := normalizeLimit(opts.Limit, pageSize)
	cacheKey := strings.Join([]string{
		opts.FolderID,
		opts.ResourceKey,
		opts.Type,
		opts.PageToken,
		opts.OrderBy,
		strconv.Itoa(pageSize),
		strconv.Itoa(limit),
	}, "\x00")
	if cached, ok := c.lists.get(cacheKey); ok {
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
		page, err := c.listChildrenPage(ctx, opts, nextToken, min(pageSize, limit-len(combined.Files)))
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
	c.lists.put(cacheKey, combined)
	return &combined, nil
}

func (c *Client) listChildrenPage(ctx context.Context, opts ListOptions, pageToken string, pageSize int) (*FileList, error) {
	values := c.baseListValues()
	values.Set("q", "trashed=false and "+quote(opts.FolderID)+" in parents"+typeFilter(opts.Type))
	values.Set("fields", "nextPageToken,incompleteSearch,files("+fileFields+")")
	values.Set("pageSize", strconv.Itoa(pageSize))
	if pageToken != "" {
		values.Set("pageToken", pageToken)
	}
	if opts.OrderBy != "" {
		values.Set("orderBy", opts.OrderBy)
	}

	var out FileList
	if err := c.getJSON(ctx, "https://www.googleapis.com/drive/v3/files?"+values.Encode(), opts.FolderID, opts.ResourceKey, &out); err != nil {
		return nil, err
	}
	annotateListExports(&out)
	return &out, nil
}

// ResolveItemPath resolves a slash-separated path of URL-escaped segments,
// one Drive name lookup per segment, starting at the given root folder.
func (c *Client) ResolveItemPath(ctx context.Context, p, rootFolderID, rootResourceKey, itemType string) (*ResolvedItem, error) {
	itemType, err := normalizeItemType(itemType)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.Trim(strings.TrimSpace(p), "/"), "/")
	parent, parentResourceKey := rootFolderID, rootResourceKey
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
			label := leafType
			if label == "any" {
				label = "item"
			}
			return nil, fmt.Errorf("%w: %s %q under %q", ErrNotFound, label, segment, parent)
		case 1:
			resolved = matches[0]
			resolvedParent = parent
			parent, parentResourceKey = matches[0].TargetIDAndKey()
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

func (c *Client) GetFile(ctx context.Context, id, resourceKey string) (*File, error) {
	if id == "" {
		return nil, errors.New("file ID is required")
	}
	cacheKey := id + "\x00" + resourceKey
	if cached, ok := c.files.get(cacheKey); ok {
		return &cached, nil
	}
	values := c.baseGetValues()
	values.Set("fields", fileFields)
	var out File
	if err := c.getJSON(ctx, "https://www.googleapis.com/drive/v3/files/"+url.PathEscape(id)+"?"+values.Encode(), id, resourceKey, &out); err != nil {
		return nil, err
	}
	annotateFileExports(&out)
	if out.ID != "" {
		c.files.put(cacheKey, out)
	}
	return &out, nil
}

// mediaRequestHeaders are the caller headers forwarded to Drive media
// reads, so range and conditional requests stay single upstream requests.
var mediaRequestHeaders = []string{
	"Range",
	"If-Match",
	"If-None-Match",
	"If-Modified-Since",
	"If-Unmodified-Since",
	"If-Range",
}

// Download streams file content via alt=media, forwarding the
// media-relevant request headers and the Drive response as-is.
func (c *Client) Download(ctx context.Context, method, id string, headers http.Header, resourceKey string) (*http.Response, error) {
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

// Export streams a Google Workspace file converted to the given MIME type,
// returning the Drive response as-is.
func (c *Client) Export(ctx context.Context, id, mimeType, resourceKey string) (*http.Response, error) {
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
	return c.doRaw(req)
}

func (c *Client) findItemsByName(ctx context.Context, parentID, parentResourceKey, name, itemType string) ([]File, error) {
	cacheKey := parentID + "\x00" + parentResourceKey + "\x00" + itemType + "\x00" + name
	if cached, ok := c.lookups.get(cacheKey); ok {
		return cached, nil
	}
	values := c.baseListValues()
	values.Set("q", "trashed=false and "+quote(parentID)+" in parents and name = "+quote(name)+typeFilter(itemType))
	values.Set("fields", "files("+fileFields+")")
	values.Set("pageSize", "2")
	var out FileList
	if err := c.getJSON(ctx, "https://www.googleapis.com/drive/v3/files?"+values.Encode(), parentID, parentResourceKey, &out); err != nil {
		return nil, err
	}
	annotateListExports(&out)
	files := filterResolvedItems(out.Files, itemType)
	if len(files) == 1 {
		c.lookups.put(cacheKey, files)
	}
	return files, nil
}

func (c *Client) baseListValues() url.Values {
	values := url.Values{}
	values.Set("supportsAllDrives", "true")
	values.Set("includeItemsFromAllDrives", "true")
	values.Set("spaces", "drive")
	if c.driveID != "" {
		values.Set("corpora", "drive")
		values.Set("driveId", c.driveID)
	}
	return values
}

func (c *Client) baseGetValues() url.Values {
	values := url.Values{}
	values.Set("supportsAllDrives", "true")
	return values
}

func (c *Client) getJSON(ctx context.Context, requestURL, id, resourceKey string, out any) error {
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
	authed := req.Clone(req.Context())
	authed.Header.Set("Authorization", "Bearer "+token)

	res, err := c.httpClient.Do(authed)
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
		authed = req.Clone(req.Context())
		authed.Header.Set("Authorization", "Bearer "+token)
		res, err = c.httpClient.Do(authed)
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
	form.Set("client_id", c.clientID)
	if c.clientSecret != "" {
		form.Set("client_secret", c.clientSecret)
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
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(res.Body).Decode(&refreshed); err != nil {
		return "", fmt.Errorf("decode token refresh: %w", err)
	}
	if refreshed.AccessToken == "" {
		return "", errors.New("token refresh returned empty access token")
	}
	c.token.AccessToken = refreshed.AccessToken
	if refreshed.ExpiresIn > 0 {
		c.token.Expiry = time.Now().Add(time.Duration(refreshed.ExpiresIn) * time.Second)
	}
	return c.token.AccessToken, nil
}

// ttlCache is a small expiring cache. Values are cloned on the way in and
// out so callers can mutate results without corrupting cached entries. A
// TTL of zero or less disables the cache.
type ttlCache[T any] struct {
	clone func(T) T

	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]ttlEntry[T]
}

type ttlEntry[T any] struct {
	value     T
	expiresAt time.Time
}

func newTTLCache[T any](ttl time.Duration, clone func(T) T) *ttlCache[T] {
	return &ttlCache[T]{clone: clone, ttl: ttl, entries: map[string]ttlEntry[T]{}}
}

func (c *ttlCache[T]) get(key string) (value T, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if c.ttl <= 0 || !ok {
		var zero T
		return zero, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.entries, key)
		var zero T
		return zero, false
	}
	return c.clone(entry.value), true
}

func (c *ttlCache[T]) put(key string, value T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ttl <= 0 {
		return
	}
	c.entries[key] = ttlEntry[T]{value: c.clone(value), expiresAt: time.Now().Add(c.ttl)}
}

// reset sets a new TTL and drops every entry.
func (c *ttlCache[T]) reset(ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttl = ttl
	c.entries = map[string]ttlEntry[T]{}
}

func cloneFileList(list FileList) FileList {
	list.Files = cloneFiles(list.Files)
	return list
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

// quote escapes a string for use in a Drive query expression.
func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}

// normalizeItemType maps the accepted type aliases to "any", "file", or
// "folder".
func normalizeItemType(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "any", "all":
		return "any", nil
	case "file", "files":
		return "file", nil
	case "folder", "folders", "dir", "dirs", "directory", "directories":
		return "folder", nil
	default:
		return "", &BadRequestError{msg: fmt.Sprintf("invalid item type %q", s)}
	}
}

// normalizeListType accepts the same aliases as normalizeItemType except an
// explicit "any", which list requests have always rejected.
func normalizeListType(s string) (string, error) {
	t, err := normalizeItemType(s)
	if err != nil || strings.EqualFold(strings.TrimSpace(s), "any") {
		return "", &BadRequestError{msg: fmt.Sprintf("invalid list type %q", s)}
	}
	return t, nil
}

var (
	folderLikeQuery    = "(mimeType = " + quote(FolderMIME) + " or (mimeType = " + quote(shortcutMIME) + " and shortcutDetails.targetMimeType = " + quote(FolderMIME) + "))"
	nonFolderLikeQuery = "(mimeType != " + quote(FolderMIME) + " and (mimeType != " + quote(shortcutMIME) + " or shortcutDetails.targetMimeType != " + quote(FolderMIME) + "))"
)

// typeFilter returns the Drive query clause for a canonical item type,
// classifying shortcuts by their target type.
func typeFilter(itemType string) string {
	switch itemType {
	case "file":
		return " and " + nonFolderLikeQuery
	case "folder":
		return " and " + folderLikeQuery
	default:
		return ""
	}
}

// filterResolvedItems re-checks the type filter on lookup results, since
// the Drive query classifies shortcuts only by targetMimeType.
func filterResolvedItems(files []File, itemType string) []File {
	if itemType == "any" {
		return files
	}
	filtered := files[:0]
	for _, file := range files {
		if (itemType == "folder") == file.IsFolder() {
			filtered = append(filtered, file)
		}
	}
	return filtered
}

func normalizePageSize(n int) int {
	if n <= 0 || n > 1000 {
		return 1000
	}
	return n
}

func normalizeLimit(limit, pageSize int) int {
	if limit <= 0 {
		return pageSize
	}
	return min(limit, 10000)
}

func copySelectedHeaders(dst, src http.Header, keys ...string) {
	for _, key := range keys {
		if values := src.Values(key); len(values) > 0 {
			dst.Del(key)
			for _, value := range values {
				dst.Add(key, value)
			}
		}
	}
}

func setResourceKeyHeader(req *http.Request, id, resourceKey string) {
	if id == "" || resourceKey == "" {
		return
	}
	req.Header.Set("X-Goog-Drive-Resource-Keys", id+"/"+resourceKey)
}
