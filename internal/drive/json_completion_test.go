package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestJSONResponsesRequireCompleteBodies(t *testing.T) {
	const canary = "SYNTHETIC_JSON_SECRET"
	for _, operation := range []string{"metadata", "list", "path", "token"} {
		for _, variant := range []string{"trailing garbage", "second value", "truncated content length", "unfinished chunks", "malformed trailers", "whitespace and unknown fields"} {
			t.Run(operation+"/"+variant, func(t *testing.T) {
				prefix, valid := `{"id":"prefix-id","unknown":true}`, `{"id":"valid-id"}`
				if operation == "list" || operation == "path" {
					prefix = `{"files":[{"id":"prefix-id","mimeType":"text/plain"}],"unknown":true}`
					valid = `{"files":[{"id":"valid-id","mimeType":"text/plain"}]}`
				} else if operation == "token" {
					prefix = `{"access_token":"prefix-id","expires_in":3600,"unknown":true}`
					valid = `{"access_token":"valid-id","expires_in":3600}`
				}
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if _, err := io.Copy(io.Discard, req.Body); err != nil {
						t.Error(err)
						return
					}
					if calls.Add(1) > 1 {
						fmt.Fprint(w, valid)
						return
					}
					conn, rw, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					fmt.Fprint(rw, "HTTP/1.1 200 OK\r\nConnection: close\r\n")
					switch variant {
					case "truncated content length":
						fmt.Fprintf(rw, "Content-Length: %d\r\n\r\n%s", len(prefix)+1, prefix)
					case "unfinished chunks":
						fmt.Fprintf(rw, "Transfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n", len(prefix), prefix)
					case "malformed trailers":
						fmt.Fprintf(rw, "Transfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\n%s\r\n\r\n", len(prefix), prefix, canary)
					default:
						body := prefix + " \r\n\t"
						if variant == "trailing garbage" {
							body = prefix + canary
						} else if variant == "second value" {
							body = prefix + `{"secret":"` + canary + `"}`
						}
						fmt.Fprintf(rw, "Content-Length: %d\r\n\r\n%s", len(body), body)
					}
					if err := rw.Flush(); err != nil {
						t.Error(err)
					}
				}))
				defer upstream.Close()
				transport := &http.Transport{}
				defer transport.CloseIdleConnections()
				cfg := testConfig()
				if operation == "token" {
					cfg.Token.AccessToken = "old-token"
					cfg.Token.RefreshToken = "fixture-refresh"
					cfg.Token.Expiry = time.Now().Add(-time.Hour)
				}
				client := NewClient(cfg, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					req = req.Clone(req.Context())
					req.URL.Scheme, req.URL.Host = "http", strings.TrimPrefix(upstream.URL, "http://")
					return transport.RoundTrip(req)
				})})
				request := func() (string, error) {
					switch operation {
					case "metadata":
						file, err := client.GetFile(context.Background(), "requested", "")
						if err != nil {
							return "", err
						}
						return file.ID, nil
					case "list":
						list, err := client.ListChildren(context.Background(), ListOptions{FolderID: "root"})
						if err != nil {
							return "", err
						}
						if len(list.Files) != 1 {
							t.Fatalf("files = %+v", list.Files)
						}
						return list.Files[0].ID, nil
					case "path":
						item, err := client.ResolveItemPath(context.Background(), "/Entry", "root", "", "file")
						if err != nil {
							return "", err
						}
						return item.File.ID, nil
					default:
						return client.accessToken(context.Background(), "")
					}
				}
				first, err := request()
				want, wantCalls := "valid-id", int32(2)
				if variant == "whitespace and unknown fields" {
					want, wantCalls = "prefix-id", 1
					if err != nil || first != want {
						t.Errorf("valid response = %q, %v", first, err)
					}
				} else {
					if err == nil || first != "" || strings.Contains(err.Error(), canary) {
						t.Errorf("incomplete response = %q, %v; want safe error", first, err)
					}
					if (variant == "truncated content length" || variant == "unfinished chunks") && !errors.Is(err, io.ErrUnexpectedEOF) {
						t.Errorf("error = %v; want unexpected EOF", err)
					}
					if operation == "token" && client.token != cfg.Token {
						t.Error("incomplete response replaced OAuth token")
					}
				}
				for range 2 {
					value, err := request()
					if err != nil || value != want {
						t.Errorf("later response = %q, %v; want %q", value, err, want)
					}
				}
				if calls.Load() != wantCalls {
					t.Errorf("requests = %d; want %d before cache reuse", calls.Load(), wantCalls)
				}
			})
		}
	}
}

func TestJSONResponseLateReadErrorsPreserveCause(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		t.Run(fmt.Sprintf("refresh=%t", refresh), func(t *testing.T) {
			cfg := testConfig()
			body := `{"id":"complete"}`
			readErr := &net.OpError{Op: "SYNTHETIC_READ_SECRET", Err: context.Canceled}
			if refresh {
				cfg.Token.AccessToken = ""
				cfg.Token.RefreshToken = "fixture-refresh"
				body = `{"access_token":"complete","expires_in":3600}`
			}
			client := NewClient(cfg, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				response := jsonResponse(http.StatusOK, "")
				response.Body = io.NopCloser(io.MultiReader(strings.NewReader(body), failingReader{readErr}))
				return response, nil
			})})
			_, err := client.GetFile(context.Background(), "requested", "")
			var original *net.OpError
			if !errors.Is(err, context.Canceled) || !errors.As(err, &original) || original != readErr || strings.Contains(err.Error(), "SYNTHETIC_READ_SECRET") || client.token != cfg.Token {
				t.Fatalf("error = %v, token changed = %t; want cancellation without state change", err, client.token != cfg.Token)
			}
		})
	}
}
