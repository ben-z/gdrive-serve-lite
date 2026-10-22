package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestInterruptedDriveStreamsAbortResponse(t *testing.T) {
	for _, target := range []string{"/file/file-1/video.mp4", "/export/doc-1/Notes?format=pdf"} {
		t.Run(target, func(t *testing.T) {
			body := &interruptedBody{reader: strings.NewReader(strings.Repeat("partial", 8192))}
			app := testServer(func(req *http.Request) (*http.Response, error) {
				if req.URL.Query().Get("fields") != "" {
					return jsonResponse(http.StatusOK, `{"id":"doc-1","name":"Notes","mimeType":"application/vnd.google-apps.document"}`), nil
				}
				res := textResponse(http.StatusOK, "")
				res.Body = body
				return res, nil
			})
			srv := httptest.NewServer(app.Handler())
			defer srv.Close()
			res, err := srv.Client().Get(srv.URL + target)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if _, err := io.ReadAll(res.Body); err == nil {
				t.Fatal("interrupted Drive stream appeared complete to the client")
			}
			if !body.closed.Load() {
				t.Fatal("Drive response body was not closed")
			}
		})
	}
}

type interruptedBody struct {
	reader *strings.Reader
	closed atomic.Bool
}

func (b *interruptedBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if err == io.EOF {
		err = errors.New("upstream connection interrupted")
	}
	return n, err
}

func (b *interruptedBody) Close() error {
	b.closed.Store(true)
	return nil
}
