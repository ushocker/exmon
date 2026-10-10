package azuremonitor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"exmon/internal/event"
	"exmon/internal/remote"
)

const (
	testDCR    = "dcr-0123456789abcdef0123456789abcdef"
	testTenant = "11111111-2222-3333-4444-555555555555"
	testClient = "66666666-7777-8888-9999-000000000000"
)

// fakeAzure plays both Entra (token endpoint) and the Logs Ingestion API.
type fakeAzure struct {
	mu       sync.Mutex
	srv      *httptest.Server
	tokens   int
	rows     []Row
	bodies   []int       // uncompressed size of each ingestion call
	statuses []int       // statuses to answer with, in order; empty = 204
	headers  http.Header // headers of the last ingestion call
}

func newFake(t *testing.T) *fakeAzure {
	f := &fakeAzure{}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token") {
			f.tokens++
			fmt.Fprintf(w, `{"access_token":"tok%d","expires_in":3600}`, f.tokens)
			return
		}
		want := "/dataCollectionRules/" + testDCR + "/streams/Custom-ExmonEvents"
		if r.URL.Path != want || r.URL.Query().Get("api-version") != apiVersion {
			t.Errorf("ingestion URL %s", r.URL)
		}
		f.headers = r.Header.Clone()
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Errorf("body not gzipped: %v", err)
			return
		}
		raw, _ := io.ReadAll(zr)
		f.bodies = append(f.bodies, len(raw))
		if len(f.statuses) > 0 {
			code := f.statuses[0]
			f.statuses = f.statuses[1:]
			if code == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "42")
			}
			w.WriteHeader(code)
			fmt.Fprint(w, `{"error":{"code":"InvalidStream","message":"details we must not log"}}`)
			return
		}
		var rows []Row
		if err := json.Unmarshal(raw, &rows); err != nil {
			t.Errorf("body is not a JSON array of rows: %v", err)
		}
		f.rows = append(f.rows, rows...)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAzure) settings() Settings {
	s := Settings{Endpoint: f.srv.URL, DCRImmutableID: testDCR, StreamName: "Custom-ExmonEvents",
		TenantID: testTenant, ClientID: testClient, ClientSecret: "s3cret", TimeoutSeconds: 10,
		Authority: f.srv.URL, Scope: defaultScope}
	return s
}

// sender returns a Sender that trusts the fake server's test certificate.
func (f *fakeAzure) sender() *Sender {
	s := New(f.settings())
	s.http = f.srv.Client()
	s.tokens.HTTP = f.srv.Client()
	return s
}

func records(n, detailSize int) []remote.Record {
	var out []remote.Record
	for i := 0; i < n; i++ {
		out = append(out, remote.Record{ID: fmt.Sprint(i), Host: "PC01", Event: event.Event{
			Time: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), Source: "incident", Kind: "INCIDENT/triggered",
			Subject: "gateway", Detail: strings.Repeat("x", detailSize), Severity: 2}})
	}
	return out
}

func TestSendRowsAndToken(t *testing.T) {
	f := newFake(t)
	s := f.sender()
	if err := s.Send(context.Background(), records(3, 10)); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), records(2, 10)); err != nil {
		t.Fatal(err)
	}
	if len(f.rows) != 5 || f.tokens != 1 {
		t.Fatalf("rows=%d tokens=%d, want 5 rows with one cached token", len(f.rows), f.tokens)
	}
	r := f.rows[0]
	if r.Host != "PC01" || r.Kind != "INCIDENT/triggered" || r.Severity != 2 || r.RecordId != "0" ||
		r.TimeGenerated != "2026-10-07T12:00:00Z" || r.ExmonVersion == "" {
		t.Errorf("row = %+v", r)
	}
	if f.headers.Get("Authorization") != "Bearer tok1" || f.headers.Get("Content-Encoding") != "gzip" {
		t.Errorf("headers = %v", f.headers)
	}
}

func TestSplitUnder1MB(t *testing.T) {
	f := newFake(t)
	// 100 rows of ~30 KB each is ~3 MB: must go in several calls, each under the limit.
	if err := f.sender().Send(context.Background(), records(100, 30_000)); err != nil {
		t.Fatal(err)
	}
	if len(f.bodies) < 3 || len(f.rows) != 100 {
		t.Fatalf("calls=%d rows=%d", len(f.bodies), len(f.rows))
	}
	for _, n := range f.bodies {
		if n > maxBodyBytes {
			t.Errorf("a call carried %d bytes, over %d", n, maxBodyBytes)
		}
	}
}

func TestResponses(t *testing.T) {
	tests := []struct {
		status    int
		permanent bool
		wait      time.Duration
	}{
		{http.StatusTooManyRequests, false, 42 * time.Second},
		{http.StatusInternalServerError, false, 0},
		{http.StatusForbidden, false, 0},
		{http.StatusBadRequest, true, 0},
	}
	for _, tt := range tests {
		f := newFake(t)
		f.statuses = []int{tt.status}
		err := f.sender().Send(context.Background(), records(1, 10))
		if err == nil {
			t.Fatalf("%d: want an error", tt.status)
		}
		if remote.IsPermanent(err) != tt.permanent {
			t.Errorf("%d: permanent=%v, want %v (%v)", tt.status, remote.IsPermanent(err), tt.permanent, err)
		}
		if tt.wait > 0 {
			var r interface{ RetryAfter() time.Duration }
			if re, ok := err.(interface{ RetryAfter() time.Duration }); !ok || re.RetryAfter() != tt.wait {
				t.Errorf("%d: want RetryAfter %v, got %v", tt.status, tt.wait, r)
			}
		}
		if strings.Contains(err.Error(), "details we must not log") || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%d: error leaks the body or secret: %v", tt.status, err)
		}
	}
}

func Test401RefreshesToken(t *testing.T) {
	f := newFake(t)
	s := f.sender()
	f.statuses = []int{http.StatusUnauthorized}
	if err := s.Send(context.Background(), records(1, 10)); err == nil || remote.IsPermanent(err) {
		t.Fatalf("401 should be a temporary error, got %v", err)
	}
	if err := s.Send(context.Background(), records(1, 10)); err != nil {
		t.Fatal(err)
	}
	if f.tokens != 2 {
		t.Errorf("tokens fetched = %d, want 2 (refreshed after 401)", f.tokens)
	}
}

func Test413Splits(t *testing.T) {
	f := newFake(t)
	f.statuses = []int{http.StatusRequestEntityTooLarge}
	if err := f.sender().Send(context.Background(), records(4, 10)); err != nil {
		t.Fatal(err)
	}
	if len(f.rows) != 4 || len(f.bodies) != 3 {
		t.Errorf("rows=%d calls=%d, want 4 rows in 1 rejected + 2 halves", len(f.rows), len(f.bodies))
	}
}

func TestValidateAndDescribe(t *testing.T) {
	good := `{"endpoint":"https://x.ingest.monitor.azure.com","dcrImmutableId":"` + testDCR +
		`","streamName":"Custom-ExmonEvents","tenantId":"` + testTenant + `","clientId":"` + testClient +
		`","clientSecret":"s3cret"}`
	if err := Type.Validate(json.RawMessage(good)); err != nil {
		t.Fatalf("valid settings: %v", err)
	}
	if d := Type.Describe(json.RawMessage(good)); strings.Contains(d, "s3cret") || !strings.Contains(d, "Custom-ExmonEvents") {
		t.Errorf("describe = %q", d)
	}
	bad := []string{
		strings.Replace(good, "https://", "http://", 1),
		strings.Replace(good, testDCR, "dcr-nothex", 1),
		strings.Replace(good, `"clientSecret":"s3cret"`, `"clientSecret":""`, 1),
		strings.Replace(good, "Custom-ExmonEvents", "ExmonEvents", 1),
		strings.Replace(good, `}`, `,"clientSecert":"typo"}`, 1),
	}
	for i, b := range bad {
		if Type.Validate(json.RawMessage(b)) == nil {
			t.Errorf("bad settings %d accepted", i)
		}
	}
}

func TestTruncate(t *testing.T) {
	s := strings.Repeat("é", 100) // 2 bytes each
	got := truncate(s, 51)
	if len(got) > 51 || !strings.HasSuffix(got, "…[truncated]") || !bytes.Equal([]byte(got), []byte(string([]rune(got)))) {
		t.Errorf("truncate = %q (%d bytes)", got, len(got))
	}
}
