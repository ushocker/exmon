// Package azuremonitor is the "azureMonitor" remote type: it sends events to a
// Log Analytics workspace through the Azure Monitor Logs Ingestion API.
//
// How it fits together in Azure (docs/azure-monitor.md lists what it needs):
//
//	exmon ──HTTPS──► DCR logs-ingestion endpoint ──► data collection rule ──► ExmonEvents_CL table
//	        bearer token from Entra (client credentials of a "write-only" app whose
//	        only role is Monitoring Metrics Publisher on that one rule)
//
// Each call is a gzipped JSON array of flat rows. A call may carry at most 1 MB,
// measured both before and after compression, so batches are split into chunks.
package azuremonitor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"exmon/internal/entra"
	"exmon/internal/event"
	"exmon/internal/remote"
	"exmon/internal/secret"
	"exmon/internal/version"
)

// TypeName is the remote "type" this package provides.
const TypeName = "azureMonitor"

const (
	apiVersion     = "2023-01-01"
	defaultScope   = "https://monitor.azure.com//.default" // the double slash is how Azure Monitor's scope is written
	maxBodyBytes   = 990_000                               // under the 1 MB per-call limit, with margin
	maxDetailBytes = 32 << 10                              // longer details are cut, so one event can't fill a call
)

// Settings is the remote's "settings" object.
type Settings struct {
	Endpoint       string        `json:"endpoint"`                 // the DCR's logs ingestion endpoint, https://<...>.ingest.monitor.azure.com
	DCRImmutableID string        `json:"dcrImmutableId"`           // dcr-<32 hex>
	StreamName     string        `json:"streamName"`               // Custom-<name>, as declared in the DCR
	TenantID       string        `json:"tenantId"`                 // Entra tenant (directory) id
	ClientID       string        `json:"clientId"`                 // the write-only app's application (client) id
	ClientSecret   secret.Secret `json:"clientSecret"`             // the app's secret; never logged
	TimeoutSeconds int           `json:"timeoutSeconds,omitempty"` // per HTTP call; default 30
	Authority      string        `json:"authority,omitempty"`      // Entra endpoint; "" = public cloud
	Scope          string        `json:"scope,omitempty"`          // token scope; "" = Azure Monitor public cloud
}

var (
	guidRE   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	dcrRE    = regexp.MustCompile(`^dcr-[0-9a-f]{32}$`)
	streamRE = regexp.MustCompile(`^Custom-[A-Za-z0-9_]{1,100}$`)
)

func decode(raw json.RawMessage) (Settings, error) {
	var s Settings
	if err := remote.DecodeSettings(raw, &s); err != nil {
		return s, err
	}
	if s.TimeoutSeconds == 0 {
		s.TimeoutSeconds = 30
	}
	if s.Scope == "" {
		s.Scope = defaultScope
	}
	return s, s.validate()
}

func (s Settings) validate() error {
	if err := httpsURL("endpoint", s.Endpoint); err != nil {
		return err
	}
	if s.Authority != "" {
		if err := httpsURL("authority", s.Authority); err != nil {
			return err
		}
	}
	switch {
	case !dcrRE.MatchString(s.DCRImmutableID):
		return fmt.Errorf("dcrImmutableId %q should look like dcr-0123456789abcdef0123456789abcdef", s.DCRImmutableID)
	case !streamRE.MatchString(s.StreamName):
		return fmt.Errorf("streamName %q should look like Custom-ExmonEvents", s.StreamName)
	case !guidRE.MatchString(s.TenantID):
		return fmt.Errorf("tenantId must be a GUID")
	case !guidRE.MatchString(s.ClientID):
		return fmt.Errorf("clientId must be a GUID")
	case !s.ClientSecret.IsSet():
		return errors.New("clientSecret must be set")
	case s.TimeoutSeconds < 5 || s.TimeoutSeconds > 120:
		return fmt.Errorf("timeoutSeconds %d is out of range 5-120", s.TimeoutSeconds)
	}
	return nil
}

func httpsURL(name, v string) error {
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("%s %q must be an https:// URL", name, v)
	}
	return nil
}

// Type is the registry entry for this sender type.
var Type = remote.SenderType{
	Validate: func(raw json.RawMessage) error {
		_, err := decode(raw)
		return err
	},
	Describe: func(raw json.RawMessage) string {
		s, _ := decode(raw)
		return fmt.Sprintf("%s stream %s (app %s)", s.Endpoint, s.StreamName, s.ClientID)
	},
	New: func(cfg remote.Config, _ event.Logger) (remote.Sender, error) {
		s, err := decode(cfg.Settings)
		if err != nil {
			return nil, err
		}
		return New(s), nil
	},
}

// Sender posts batches to the Logs Ingestion API.
type Sender struct {
	s      Settings
	url    string
	tokens *entra.ClientCredentials
	http   *http.Client
}

// New creates a Sender for already-validated settings.
func New(s Settings) *Sender {
	client := &http.Client{
		Timeout: time.Duration(s.TimeoutSeconds) * time.Second,
		// Proxy settings come from HTTPS_PROXY / HTTP_PROXY (the default transport).
		// Neither Entra nor the ingestion endpoint redirects; following one would
		// send the token request (with the secret) or the events elsewhere.
		CheckRedirect: entra.RefuseRedirects,
	}
	return &Sender{
		s: s,
		url: strings.TrimRight(s.Endpoint, "/") + "/dataCollectionRules/" + url.PathEscape(s.DCRImmutableID) +
			"/streams/" + url.PathEscape(s.StreamName) + "?api-version=" + apiVersion,
		tokens: &entra.ClientCredentials{TenantID: s.TenantID, ClientID: s.ClientID, Secret: s.ClientSecret,
			Scope: s.Scope, Authority: s.Authority, HTTP: client},
		http: client,
	}
}

// Row is one record as a flat table row. The field names are the column names
// of the ExmonEvents_CL table (docs/azure-monitor.md#the-table).
type Row struct {
	TimeGenerated string `json:"TimeGenerated"` // RFC 3339, UTC
	Host          string `json:"Host"`
	RecordId      string `json:"RecordId"` // the record's unique id, to find duplicates after a resend
	Source        string `json:"Source"`
	Kind          string `json:"Kind"`
	Subject       string `json:"Subject"`
	Detail        string `json:"Detail"`
	Severity      int    `json:"Severity"`
	Held          bool   `json:"Held"`
	ExmonVersion  string `json:"ExmonVersion"`
}

// ToRow flattens a record.
func ToRow(r remote.Record) Row {
	return Row{
		TimeGenerated: r.Event.Time.UTC().Format(time.RFC3339Nano),
		Host:          r.Host,
		RecordId:      r.ID,
		Source:        r.Event.Source,
		Kind:          r.Event.Kind,
		Subject:       r.Event.Subject,
		Detail:        truncate(r.Event.Detail, maxDetailBytes),
		Severity:      r.Event.Severity,
		Held:          r.Event.Held,
		ExmonVersion:  version.Version,
	}
}

// truncate cuts s to at most n bytes without splitting a UTF-8 character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const marker = "…[truncated]"
	cut := n - len(marker)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + marker
}

// chunks encodes rows as JSON arrays, each at most limit bytes.
func chunks(batch []remote.Record, limit int) ([][]byte, error) {
	var out [][]byte
	var buf bytes.Buffer
	for _, r := range batch {
		b, err := json.Marshal(ToRow(r))
		if err != nil {
			return nil, err
		}
		// +2 for the "[" or "," before it and the closing "]".
		if buf.Len() > 0 && buf.Len()+len(b)+2 > limit {
			buf.WriteByte(']')
			out = append(out, bytes.Clone(buf.Bytes()))
			buf.Reset()
		}
		if buf.Len() == 0 {
			buf.WriteByte('[')
		} else {
			buf.WriteByte(',')
		}
		buf.Write(b)
	}
	if buf.Len() > 0 {
		buf.WriteByte(']')
		out = append(out, buf.Bytes())
	}
	return out, nil
}

// Send posts the batch, in as many calls as the size limit needs. If any call
// fails the whole batch is retried later; rows that already arrived are then sent
// twice and can be told apart by RecordId.
func (s *Sender) Send(ctx context.Context, batch []remote.Record) error {
	parts, err := chunks(batch, maxBodyBytes)
	if err != nil {
		return remote.Permanent(err)
	}
	for _, body := range parts {
		if err := s.post(ctx, body, true); err != nil {
			return err
		}
	}
	return nil
}

// post sends one chunk. On 413 (too large, which the chunking should prevent) it
// splits the chunk in half once.
func (s *Sender) post(ctx context.Context, body []byte, maySplit bool) error {
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return err // e.g. a wrong or expired secret: keep the events and retry
	}
	gz, err := gzipBytes(body)
	if err != nil {
		return remote.Permanent(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(gz))
	if err != nil {
		return remote.Permanent(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")

	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("logs ingestion: %w", err)
	}
	defer resp.Body.Close()
	errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))

	switch code := resp.StatusCode; {
	case code == http.StatusNoContent || code == http.StatusOK:
		return nil
	case code == http.StatusUnauthorized:
		s.tokens.Invalidate() // the next attempt gets a fresh token
		return fmt.Errorf("logs ingestion: HTTP 401 (token refreshed; will retry)")
	case code == http.StatusRequestEntityTooLarge && maySplit:
		left, right, ok := splitArray(body)
		if !ok {
			return remote.Permanent(errors.New("logs ingestion: HTTP 413 for a single row"))
		}
		if err := s.post(ctx, left, false); err != nil {
			return err
		}
		return s.post(ctx, right, false)
	case code == http.StatusTooManyRequests || code >= 500:
		return &retryError{code: code, wait: retryAfter(resp.Header.Get("Retry-After"))}
	case code == http.StatusForbidden:
		// Usually the role assignment is missing or still propagating (up to 30 min).
		// Keep the events and retry rather than dropping them.
		return fmt.Errorf("logs ingestion: HTTP 403: the app lacks Monitoring Metrics Publisher on the DCR, or the role is still propagating")
	default:
		// 400 and other client errors: the data or the DCR/stream is wrong, so
		// retrying the same batch can't help.
		return remote.Permanent(fmt.Errorf("logs ingestion: HTTP %d %s", code, errorCode(errBody)))
	}
}

// retryError is a temporary failure that may say how long to wait.
type retryError struct {
	code int
	wait time.Duration
}

func (e *retryError) Error() string { return fmt.Sprintf("logs ingestion: HTTP %d", e.code) }

// RetryAfter makes the remote queue wait at least this long (see remote.retryHint).
func (e *retryError) RetryAfter() time.Duration { return e.wait }

// retryAfter parses a Retry-After header given in seconds.
func retryAfter(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0
	}
	return min(time.Duration(n)*time.Second, time.Hour)
}

// errorCode pulls Azure's error code out of an error body, without echoing the
// rest of it into our logs.
func errorCode(body []byte) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Code != "" {
		return e.Error.Code
	}
	return ""
}

func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// splitArray splits a JSON array of rows into two halves.
func splitArray(body []byte) (left, right []byte, ok bool) {
	var rows []json.RawMessage
	if json.Unmarshal(body, &rows) != nil || len(rows) < 2 {
		return nil, nil, false
	}
	mid := len(rows) / 2
	l, _ := json.Marshal(rows[:mid])
	r, _ := json.Marshal(rows[mid:])
	return l, r, true
}
