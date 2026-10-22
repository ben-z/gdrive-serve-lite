package drive

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenRefreshRejectsNon200WithoutFollowingRedirects(t *testing.T) {
	const accessToken = "SYNTHETIC_RETURNED_ACCESS"
	const locationSecret = "SYNTHETIC_LOCATION_SECRET"
	const reasonSecret = "SYNTHETIC_REASON_SECRET"
	var sinkRequests, redirectChecks atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sinkRequests.Add(1)
		if r.URL.Path == "/media" {
			io.WriteString(w, "fixture media")
			return
		}
		io.WriteString(w, `{"access_token":"`+accessToken+`","expires_in":3600}`)
	}))
	defer sink.Close()

	cfg := testConfig()
	cfg.ClientSecret = "synthetic-client-secret"
	cfg.Token.RefreshToken = "synthetic-refresh-token"
	cfg.Token.Expiry = time.Now().Add(-time.Hour)
	var status atomic.Int32
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			if r.Header.Get("Authorization") != "Bearer "+accessToken {
				t.Errorf("media authorization = %q", r.Header.Get("Authorization"))
			}
			if cookie, err := r.Cookie("fixture"); err != nil || cookie.Value != "drive-cookie" {
				t.Errorf("shared media cookie = %v, %v", cookie, err)
			}
			http.Redirect(w, r, sink.URL+"/media", http.StatusFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Method != http.MethodPost || r.Form.Get("client_secret") != cfg.ClientSecret || r.Form.Get("refresh_token") != cfg.Token.RefreshToken {
			t.Error("issuer did not receive the configured OAuth refresh POST")
		}
		if cookie, err := r.Cookie("fixture"); err != nil || cookie.Value != "oauth-cookie" {
			t.Errorf("refresh cookie = %v, %v", cookie, err)
		}
		code := int(status.Load())
		if code == http.StatusMultipleChoices {
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			fmt.Fprintf(rw, "HTTP/1.1 300 %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", reasonSecret)
			rw.Flush()
			return
		}
		w.Header().Set("Location", sink.URL+"/capture?secret="+locationSecret)
		w.WriteHeader(code)
		if code >= 400 {
			io.WriteString(w, `{"error":"invalid_grant"}`)
		} else if code != http.StatusNoContent && code != http.StatusNotModified {
			io.WriteString(w, `{"access_token":"`+accessToken+`","expires_in":3600}`)
		}
	}))
	defer issuer.Close()
	issuerURL, err := url.Parse(issuer.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	for host, cookie := range map[string]string{"oauth2.googleapis.com": "oauth-cookie", "www.googleapis.com": "drive-cookie"} {
		jar.SetCookies(&url.URL{Scheme: "https", Host: host}, []*http.Cookie{{Name: "fixture", Value: cookie}})
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	defer base.CloseIdleConnections()
	shared := &http.Client{
		Jar: jar, Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { redirectChecks.Add(1); return nil },
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "oauth2.googleapis.com" || r.URL.Host == "www.googleapis.com" {
				r = r.Clone(r.Context())
				r.URL.Scheme, r.URL.Host = issuerURL.Scheme, issuerURL.Host
			}
			return base.RoundTrip(r)
		}),
	}
	for _, code := range []int{201, 202, 204, 206, 299, 300, 301, 302, 303, 304, 307, 308, 400, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			status.Store(int32(code))
			client := NewClient(cfg, shared)
			_, err := client.accessToken(context.Background(), "")
			if err == nil || !strings.Contains(err.Error(), fmt.Sprint(code)) {
				t.Fatalf("error = %v, want rejected HTTP %d", err, code)
			}
			if strings.Contains(err.Error(), accessToken) || strings.Contains(err.Error(), locationSecret) || strings.Contains(err.Error(), reasonSecret) {
				t.Fatalf("unexpected response exposed credentials: %v", err)
			}
			if client.token != cfg.Token || sinkRequests.Load() != 0 || redirectChecks.Load() != 0 {
				t.Fatalf("failed refresh changed token or followed redirect: token=%+v sink=%d callback=%d", client.token, sinkRequests.Load(), redirectChecks.Load())
			}
		})
	}
	status.Store(http.StatusOK)
	client := NewClient(cfg, shared)
	if token, err := client.accessToken(context.Background(), ""); err != nil || token != accessToken {
		t.Fatalf("HTTP 200 refresh = %q, %v", token, err)
	}
	resp, err := client.Download(context.Background(), "fixture", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "fixture media" || sinkRequests.Load() != 1 || redirectChecks.Load() != 1 {
		t.Fatalf("media redirect: body=%q err=%v sink=%d callback=%d", body, err, sinkRequests.Load(), redirectChecks.Load())
	}
	if shared.Jar != jar || shared.Timeout != 3*time.Second {
		t.Fatal("refresh changed the shared HTTP client")
	}
}
