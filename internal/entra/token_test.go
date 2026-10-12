package entra

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenCachedAndRefreshed(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.URL.Path != "/tenant-1/oauth2/v2.0/token" || r.Form.Get("client_secret") != "s3cret" ||
			r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("scope") != "https://monitor.azure.com//.default" {
			t.Errorf("bad request %s %v", r.URL.Path, r.Form)
		}
		n := calls.Add(1)
		fmt.Fprintf(w, `{"access_token":"tok%d","expires_in":3600}`, n)
	}))
	defer srv.Close()

	now := time.Now()
	c := &ClientCredentials{TenantID: "tenant-1", ClientID: "app", Secret: "s3cret",
		Scope: "https://monitor.azure.com//.default", Authority: srv.URL, now: func() time.Time { return now }}
	ctx := context.Background()

	if tok, _ := c.Token(ctx); tok != "tok1" {
		t.Fatalf("first token %q", tok)
	}
	if tok, _ := c.Token(ctx); tok != "tok1" || calls.Load() != 1 {
		t.Errorf("second call should use the cache, got %q after %d calls", tok, calls.Load())
	}
	now = now.Add(56 * time.Minute) // within 5 minutes of expiry
	if tok, _ := c.Token(ctx); tok != "tok2" {
		t.Errorf("near expiry should refresh, got %q", tok)
	}
	c.Invalidate()
	if tok, _ := c.Token(ctx); tok != "tok3" {
		t.Errorf("after Invalidate should refresh, got %q", tok)
	}
}

func TestTokenErrorHasNoSecretOrBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"invalid_client","error_description":"AADSTS7000215: Invalid client secret provided. Trace ID: secret-trace","error_codes":[7000215]}`)
	}))
	defer srv.Close()
	c := &ClientCredentials{TenantID: "t", ClientID: "app", Secret: "s3cret", Scope: "x", Authority: srv.URL}
	_, err := c.Token(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "HTTP 401 invalid_client (AADSTS7000215)") {
		t.Errorf("error should name status and code: %s", msg)
	}
	if strings.Contains(msg, "s3cret") || strings.Contains(msg, "secret-trace") {
		t.Errorf("error leaks the secret or body: %s", msg)
	}
}

// redirectPair returns a server that answers every request with a 307 to a
// second server, and a counter of requests that reached the second one.
func redirectPair(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var reached atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
	}))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	return redirector, &reached
}

// The form with the secret must never be posted to a redirect's target.
func TestTokenRefusesRedirect(t *testing.T) {
	srv, reached := redirectPair(t)
	c := &ClientCredentials{TenantID: "t", ClientID: "app", Secret: "s3cret", Scope: "x", Authority: srv.URL}
	if _, err := c.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("got %v, want the redirect refused", err)
	}
	if reached.Load() != 0 {
		t.Errorf("%d token requests reached the redirect's target", reached.Load())
	}
}
