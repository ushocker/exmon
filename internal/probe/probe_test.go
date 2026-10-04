package probe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"exmon/internal/event"
)

func TestTransition(t *testing.T) {
	s := New(DefaultConfig())
	steps := []struct {
		up   bool
		want string
	}{
		{true, ""}, // first round up: nothing to report
		{true, ""},
		{false, "PROBE/down"},
		{false, ""}, // still down: report once
		{true, "PROBE/up"},
	}
	for i, st := range steps {
		kind, _ := s.transition(result{name: "x", up: st.up})
		if kind != st.want {
			t.Errorf("step %d: got %q, want %q", i, kind, st.want)
		}
	}
	if kind, _ := s.transition(result{name: "new", up: false}); kind != "PROBE/down" {
		t.Error("a probe that is down on its first round should report PROBE/down")
	}
}

func TestEndpointTCP(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	r := checkEndpoint(context.Background(), Endpoint{Name: "local", Target: "127.0.0.1", Port: port}, 1, time.Second)
	if !r.up || r.metrics["reachable"] != 1 || r.metrics["loss_pct"] != 0 {
		t.Errorf("got %+v", r)
	}

	ln.Close()
	r = checkEndpoint(context.Background(), Endpoint{Name: "local", Target: "127.0.0.1", Port: port}, 1, time.Second)
	if r.up || r.metrics["reachable"] != 0 || !strings.Contains(r.detail, "tcp FAILED") {
		t.Errorf("closed port: got %+v", r)
	}
}

func TestHTTPPhases(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Azure-Ref", "abc")
		time.Sleep(20 * time.Millisecond) // so time-to-first-byte is measurable
		w.Write([]byte("hello"))
	}))
	defer srv.Close()
	// The test server uses a self-signed certificate; srv.Client()'s transport trusts it.
	r := checkHTTPWith(context.Background(), HTTPTest{Name: "t", URL: srv.URL}, 5*time.Second, srv.Client().Transport.(*http.Transport))
	if !r.up || r.metrics["http_ok"] != 1 {
		t.Fatalf("got %+v", r)
	}
	tlsMs, ttfb, total := r.metrics["http_tls_ms"], r.metrics["http_ttfb_ms"], r.metrics["http_total_ms"]
	if tlsMs <= 0 || ttfb < 20 || ttfb > total {
		t.Errorf("phases tls=%v ttfb=%v total=%v", tlsMs, ttfb, total)
	}
	if !strings.Contains(r.detail, "cdn=AzureFrontDoor") || !strings.Contains(r.detail, "bytes=5") {
		t.Errorf("detail %q", r.detail)
	}
}

func TestHTTPFailure(t *testing.T) {
	r := checkHTTP(context.Background(), HTTPTest{Name: "t", URL: "http://127.0.0.1:1/"}, time.Second)
	if r.up || r.metrics["http_ok"] != 0 || !strings.Contains(r.detail, "FAILED") {
		t.Errorf("got %+v", r)
	}
}

func TestValidate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Endpoints = []Endpoint{{Name: "x", Target: "h", Port: 0}}
	if cfg.Validate() == nil {
		t.Error("port 0 should fail")
	}
	cfg = DefaultConfig()
	cfg.HTTPTests = []HTTPTest{{Name: "x", URL: "ftp://h"}}
	if cfg.Validate() == nil {
		t.Error("ftp url should fail")
	}
}

func TestTraceLoopback(t *testing.T) {
	hops := trace("127.0.0.1", 5)
	if len(hops) != 1 || !hops[0].reached || hops[0].from != "127.0.0.1" {
		t.Errorf("got %+v, want one hop reaching 127.0.0.1", hops)
	}
}

func TestTraceReal(t *testing.T) {
	if testing.Short() {
		t.Skip("network")
	}
	start := time.Now()
	hops := trace("1.1.1.1", 30)
	for _, h := range hops {
		t.Log(h)
	}
	if len(hops) == 0 {
		t.Skip("no route")
	}
	t.Logf("%d hops in %v", len(hops), time.Since(start))
}

func TestHTTP2Server(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Proto))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	r := checkHTTPWith(context.Background(), HTTPTest{Name: "h2", URL: srv.URL}, 5*time.Second, srv.Client().Transport.(*http.Transport))
	if !r.up || !strings.Contains(r.detail, " 200 ") {
		t.Errorf("HTTP/2 server: %+v", r)
	}
}

func TestTracerAttachesToRunningTrace(t *testing.T) {
	tr := &Tracer{}
	if tr.attach("a") {
		t.Fatal("attach should fail when nothing is running")
	}
	tr.running = true
	if !tr.attach("a") || !tr.attach("b") {
		t.Fatal("attach should succeed while running")
	}
	if ids := tr.finish(); len(ids) != 2 || tr.running {
		t.Errorf("finish returned %v, running=%v", ids, tr.running)
	}
}

// heldLogger lets a test switch Held on and off and records lines.
type heldLogger struct {
	held  bool
	lines []string
}

func (l *heldLogger) Log(kind, detail string)                   { l.lines = append(l.lines, kind) }
func (l *heldLogger) Metrics(string, map[string]float64)        {}
func (l *heldLogger) DeclareMetrics(map[string]event.Direction) {}
func (l *heldLogger) Held() bool                                { return l.held }

// TestDownReannouncedWhenLive: a probe that went down while events were held is
// reported again (live) on the first round after they are released.
func TestDownReannouncedWhenLive(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DNSNames = nil
	cfg.Endpoints = []Endpoint{{Name: "closed", Target: "127.0.0.1", Port: 1}}
	cfg.PingCount = 1
	cfg.TimeoutSeconds = 1
	s := New(cfg)
	l := &heldLogger{held: true}
	s.round(context.Background(), l) // held: PROBE/down logged, but held
	l.lines, l.held = nil, false
	s.round(context.Background(), l) // live: must report PROBE/down again
	found := false
	for _, k := range l.lines {
		found = found || k == "PROBE/down"
	}
	if !found {
		t.Errorf("want PROBE/down re-announced when live; got %v", l.lines)
	}
}
