package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
)

// MaxThumbnailBytes bounds the raster image buffered before an image response is sent.
const MaxThumbnailBytes = 2 << 20

type Thumbnail struct {
	Body     []byte
	MIMEType string
}

// GetThumbnail requests a fresh private link only when the thumbnail is read.
// Signed links never enter File values or the metadata/list caches.
func (c *Client) GetThumbnail(ctx context.Context, id, resourceKey string) (*Thumbnail, error) {
	if id == "" {
		return nil, errors.New("file ID is required")
	}
	values := c.baseGetValues()
	values.Set("fields", "id,hasThumbnail,thumbnailLink")
	// Neither phase may redirect OAuth or resource-key headers elsewhere.
	client := *c.httpClient
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	metadataRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/drive/v3/files/"+url.PathEscape(id)+"?"+values.Encode(), nil)
	if err != nil {
		return nil, errors.New("invalid thumbnail metadata request")
	}
	setResourceKeyHeader(metadataRequest, id, resourceKey)
	metadataResponse, err := c.doRawWithClient(metadataRequest, &client)
	if err != nil {
		return nil, thumbnailRequestError(err)
	}
	defer metadataResponse.Body.Close()
	if metadataResponse.StatusCode != http.StatusOK {
		return nil, thumbnailHTTPError(metadataResponse.StatusCode)
	}
	var metadata struct {
		ID            string `json:"id"`
		HasThumbnail  bool   `json:"hasThumbnail"`
		ThumbnailLink string `json:"thumbnailLink"`
	}
	if err := readJSON(metadataResponse.Body, &metadata); err != nil {
		return nil, errors.New("Drive thumbnail metadata could not be read")
	}
	if metadata.ID != id {
		return nil, errors.New("Drive thumbnail metadata does not match the requested file")
	}
	if !metadata.HasThumbnail || metadata.ThumbnailLink == "" {
		return nil, fmt.Errorf("%w: no readable native thumbnail", ErrNotFound)
	}
	link, err := url.Parse(metadata.ThumbnailLink)
	if err != nil || link.Scheme != "https" || link.Host != "lh3.googleusercontent.com" || link.User != nil || link.Fragment != "" || link.Opaque != "" {
		return nil, errors.New("Drive returned an unsupported thumbnail host")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.String(), nil)
	if err != nil {
		return nil, errors.New("invalid Drive thumbnail request")
	}
	// Do not forward any caller credentials or request headers to the image host.
	res, err := c.doRawWithClient(req, &client)
	if err != nil {
		return nil, thumbnailRequestError(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, thumbnailHTTPError(res.StatusCode)
	}
	contentType, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || !thumbnailMIME(contentType) {
		return nil, errors.New("Drive thumbnail has an unsupported image type")
	}
	if res.ContentLength > MaxThumbnailBytes {
		return nil, errors.New("Drive thumbnail exceeds the image size limit")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxThumbnailBytes+1))
	if err != nil {
		return nil, errors.New("Drive thumbnail image could not be read")
	}
	if len(body) > MaxThumbnailBytes {
		return nil, errors.New("Drive thumbnail exceeds the image size limit")
	}
	if http.DetectContentType(body) != contentType {
		return nil, errors.New("Drive thumbnail image does not match its content type")
	}
	return &Thumbnail{Body: body, MIMEType: contentType}, nil
}

func thumbnailHTTPError(status int) error {
	if status >= 400 && status <= 599 {
		return &APIError{StatusCode: status, Status: http.StatusText(status)}
	}
	return errors.New("Drive thumbnail request returned an unexpected status")
}

func thumbnailMIME(contentType string) bool {
	switch contentType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

// Transport errors and upstream bodies may contain signed URLs or credentials.
func thumbnailRequestError(err error) error {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return &APIError{StatusCode: apiErr.StatusCode, Status: http.StatusText(apiErr.StatusCode)}
	}
	return errors.New("Drive thumbnail request failed")
}
