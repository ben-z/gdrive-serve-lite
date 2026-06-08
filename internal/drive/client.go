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
	cfg        config.Config
	token      config.OAuthToken
	httpClient *http.Client
	mu         sync.Mutex
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
	ShortcutDetails *ShortcutDetails  `json:"shortcutDetails,omitempty"`
}

type ShortcutDetails struct {
	TargetID       string `json:"targetId,omitempty"`
	TargetMIMEType string `json:"targetMimeType,omitempty"`
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
	FolderID  string
	PageSize  int
	Limit     int
	PageToken string
	OrderBy   string
	Type      string
}

func NewClient(cfg config.Config) *Client {
	return NewClientWithHTTPClient(cfg, http.DefaultClient)
}

func NewClientWithHTTPClient(cfg config.Config, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		cfg:        cfg,
		token:      cfg.Token,
		httpClient: httpClient,
	}
}

func (c *Client) ListChildren(ctx context.Context, opts ListOptions) (*FileList, error) {
	if opts.FolderID == "" {
		return nil, errors.New("folder ID is required")
	}
	pageSize := normalizePageSize(opts.PageSize)
	limit := normalizeLimit(opts.Limit, pageSize)

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
	return &combined, nil
}

func (c *Client) listChildrenPage(ctx context.Context, opts ListOptions, pageToken string, pageSize int) (*FileList, error) {
	q := "trashed=false and " + quote(opts.FolderID) + " in parents"
	switch opts.Type {
	case "", "all":
	case "files":
		q += " and mimeType != " + quote(FolderMIME)
	case "dirs", "folders", "directories":
		q += " and mimeType = " + quote(FolderMIME)
	default:
		return nil, fmt.Errorf("invalid list type %q", opts.Type)
	}

	values := c.baseListValues()
	values.Set("q", q)
	values.Set("fields", "nextPageToken,incompleteSearch,files(id,name,mimeType,size,modifiedTime,md5Checksum,webViewLink,webContentLink,resourceKey,shortcutDetails)")
	values.Set("pageSize", fmt.Sprintf("%d", pageSize))
	if pageToken != "" {
		values.Set("pageToken", pageToken)
	}
	if opts.OrderBy != "" {
		values.Set("orderBy", opts.OrderBy)
	}

	var out FileList
	if err := c.getJSON(ctx, "https://www.googleapis.com/drive/v3/files?"+values.Encode(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ResolvePath(ctx context.Context, p string, rootFolderID string) (string, error) {
	item, err := c.ResolveItemPath(ctx, p, rootFolderID, "folder")
	if err != nil {
		return "", err
	}
	return item.File.ID, nil
}

func (c *Client) ResolveItemPath(ctx context.Context, p string, rootFolderID string, itemType string) (*ResolvedItem, error) {
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
				ID:       rootFolderID,
				Name:     "/",
				MIMEType: FolderMIME,
			},
		}, nil
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	parent := rootFolderID
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
		matches, err := c.findItemsByName(ctx, parent, segment, leafType)
		if err != nil {
			return nil, err
		}
		switch len(matches) {
		case 0:
			return nil, fmt.Errorf("%w: %s %q under %q", ErrNotFound, typeLabel(leafType), segment, parent)
		case 1:
			resolved = matches[0]
			resolvedParent = parent
			parent = matches[0].ID
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
				ID:       rootFolderID,
				Name:     "/",
				MIMEType: FolderMIME,
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
	if id == "" {
		return nil, errors.New("file ID is required")
	}
	values := c.baseGetValues()
	values.Set("fields", "id,name,mimeType,size,modifiedTime,md5Checksum,webViewLink,webContentLink,resourceKey,exportLinks,shortcutDetails")
	var out File
	err := c.getJSON(ctx, "https://www.googleapis.com/drive/v3/files/"+url.PathEscape(id)+"?"+values.Encode(), &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Download(ctx context.Context, method string, id string, rangeHeader string) (*http.Response, error) {
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
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	return c.do(req)
}

func (c *Client) findItemsByName(ctx context.Context, parentID, name string, itemType string) ([]File, error) {
	itemType, err := normalizeItemType(itemType)
	if err != nil {
		return nil, err
	}
	q := "trashed=false and " + quote(parentID) + " in parents and name = " + quote(name)
	switch itemType {
	case "any":
	case "file":
		q += " and mimeType != " + quote(FolderMIME)
	case "folder":
		q += " and mimeType = " + quote(FolderMIME)
	}
	values := c.baseListValues()
	values.Set("q", q)
	values.Set("fields", "files(id,name,mimeType,size,modifiedTime,md5Checksum,webViewLink,webContentLink,resourceKey,shortcutDetails)")
	values.Set("pageSize", "2")
	var out FileList
	if err := c.getJSON(ctx, "https://www.googleapis.com/drive/v3/files?"+values.Encode(), &out); err != nil {
		return nil, err
	}
	return out.Files, nil
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
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

func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
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
