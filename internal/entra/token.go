// Package entra gets access tokens from Microsoft Entra ID (Azure AD) with the
// OAuth 2.0 client credentials flow: an app registration's client id and secret
// are exchanged for a short-lived bearer token for one API.
//
// It is deliberately small (plain net/http, no Azure SDK) and careful with the
// secret: it is only ever put in the request body, and error messages carry the
// HTTP status and Entra's error code, never the request or response body.
package entra

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"exmon/internal/secret"
)

// DefaultAuthority is the public-cloud Entra endpoint.
const DefaultAuthority = "https://login.microsoftonline.com"

// refreshEarly gets a new token this long before the current one expires, so a
// token never runs out in the middle of a send.
const refreshEarly = 5 * time.Minute

// ClientCredentials gets and caches a token for one scope. The zero value isn't
// usable; set TenantID, ClientID, Secret and Scope. It is safe to use from several
// goroutines.
type ClientCredentials struct {
	TenantID  string
	ClientID  string
	Secret    secret.Secret
	Scope     string       // e.g. "https://monitor.azure.com//.default"
	Authority string       // "" = DefaultAuthority; tests point it at a fake server
	HTTP      *http.Client // nil = a client with a 30s timeout

	mu      sync.Mutex // guards the fields below
	token   string
	expires time.Time
	now     func() time.Time // nil = time.Now; tests replace it
}

// Token returns a valid access token, fetching a new one if needed.
func (c *ClientCredentials) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.clock().Before(c.expires.Add(-refreshEarly)) {
		return c.token, nil
	}
	tok, life, err := c.fetch(ctx)
	if err != nil {
		return "", err
	}
	c.token, c.expires = tok, c.clock().Add(life)
	return tok, nil
}

// Invalidate drops the cached token, for example after the API answered 401.
func (c *ClientCredentials) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
}

func (c *ClientCredentials) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// tokenResponse is the part of Entra's answer we use. ErrorCodes holds AADSTS
// numbers such as 7000215 (invalid client secret).
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Error       string `json:"error"`
	ErrorCodes  []int  `json:"error_codes"`
}

func (c *ClientCredentials) fetch(ctx context.Context) (string, time.Duration, error) {
	authority := c.Authority
	if authority == "" {
		authority = DefaultAuthority
	}
	endpoint := strings.TrimRight(authority, "/") + "/" + url.PathEscape(c.TenantID) + "/oauth2/v2.0/token"
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.ClientID},
		"client_secret": {c.Secret.Reveal()},
		"scope":         {c.Scope},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := c.HTTP
	if client == nil {
		// The form holds the secret, and Go would post it again to wherever a
		// 307/308 redirect points.
		client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: RefuseRedirects}
	}
	resp, err := client.Do(req)
	if err != nil {
		// A url.Error repeats the URL, which holds no secret (the secret is in the body).
		return "", 0, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	var tr tokenResponse
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = json.Unmarshal(body, &tr) // a non-JSON body just leaves tr empty

	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		code := ""
		if len(tr.ErrorCodes) > 0 {
			code = fmt.Sprintf(" (AADSTS%d)", tr.ErrorCodes[0])
		}
		return "", 0, fmt.Errorf("token request: HTTP %d %s%s", resp.StatusCode, tr.Error, code)
	}
	life := time.Duration(tr.ExpiresIn) * time.Second
	if life <= 0 {
		life = 10 * time.Minute // Entra always sends expires_in; this is just a safe fallback
	}
	return tr.AccessToken, life, nil
}

// RefuseRedirects is an http.Client CheckRedirect for a client that talks to one
// fixed service that never redirects (Entra ID, the Logs Ingestion API). Go's
// default follows up to 10 redirects to any host, and on a 307 or 308 it sends
// the request body there again: for a token request, the body holds the client
// secret.
func RefuseRedirects(req *http.Request, _ []*http.Request) error {
	return fmt.Errorf("redirect to %s refused", req.URL.Host)
}
