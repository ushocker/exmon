package updater

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"exmon/internal/control"
	"exmon/internal/event"
	"exmon/internal/release"
	"exmon/internal/signdoc"
	"exmon/internal/version"
)

type logRec struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRec) Log(kind, detail string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, kind+" "+detail)
}
func (l *logRec) Metrics(string, map[string]float64)        {}
func (l *logRec) DeclareMetrics(map[string]event.Direction) {}
func (l *logRec) Held() bool                                { return false }
func (l *logRec) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

var body = []byte("MZ pretend this is exmon 1.5.0")

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// env is an updater wired to a fake download server and a temp install folder.
type env struct {
	u       *Updater
	srv     *httptest.Server
	log     *logRec
	dir     string
	started int // times exmon-helper was started
}

func newEnv(t *testing.T) *env {
	e := &env{log: &logRec{}, dir: t.TempDir()}
	mux := http.NewServeMux()
	mux.HandleFunc("/exmon.exe", func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.com/exmon.exe", http.StatusFound)
	})
	e.srv = httptest.NewTLSServer(mux)
	t.Cleanup(e.srv.Close)

	cfg := control.UpdateConfig{Enabled: true, AllowedHosts: []string{"127.0.0.1"}, MaxSizeMB: 8, HealthMinutes: 10,
		PublisherCN: "Contoso Ltd", RequireAuthenticode: true}
	e.u = New(context.Background(), cfg, true, filepath.Join(e.dir, "data"), func() string { return "dev-1" }, e.log)
	e.u.client.Transport = e.srv.Client().Transport // trust the test server's certificate
	e.u.authentic = func(string) (*x509.Certificate, error) {
		return &x509.Certificate{Subject: pkix.Name{CommonName: "Contoso Ltd"}}, nil
	}
	e.u.startHelper = func() error { e.started++; return nil }
	return e
}

func (e *env) manifest(mod func(*Manifest)) Manifest {
	m := Manifest{Header: signdoc.Header{Kind: signdoc.KindRelease, Version: 1, IssuedAt: time.Now().Add(-time.Hour),
		ExpiresAt: time.Now().Add(time.Hour)}, ReleaseVersion: "1.5.0", URL: e.srv.URL + "/exmon.exe",
		SHA256: sum(body), Size: int64(len(body))}
	if mod != nil {
		mod(&m)
	}
	return m
}

func payload(m Manifest) []byte { b, _ := json.Marshal(m); return b }

func TestManifestRules(t *testing.T) {
	e := newEnv(t)
	cfg := e.u.cfg
	bad := []func(*Manifest){
		func(m *Manifest) { m.URL = "http://127.0.0.1/exmon.exe" },
		func(m *Manifest) { m.URL = "https://evil.example.com/exmon.exe" },
		func(m *Manifest) { m.SHA256 = "abc" },
		func(m *Manifest) { m.Size = 100 << 20 },
		func(m *Manifest) { m.ReleaseVersion = "latest" },
		func(m *Manifest) { m.Percent = 101 },
	}
	for i, mod := range bad {
		if validate(e.manifest(mod), cfg) == nil {
			t.Errorf("bad manifest %d accepted", i)
		}
	}
	if err := validate(e.manifest(nil), cfg); err != nil {
		t.Errorf("good manifest: %v", err)
	}
	if release.Compare("1.10.0", "1.9.9") != 1 || release.Compare("dev", "0.0.1") != -1 || release.Compare("1.2.3", "v1.2.3") != 0 {
		t.Error("Compare")
	}
}

func TestSelectionIsStable(t *testing.T) {
	m := Manifest{ReleaseVersion: "1.5.0", Percent: 25}
	chosen := 0
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("dev-%d", i)
		in25 := release.Selected(id, m)
		if in25 {
			chosen++
		}
		m50 := m
		m50.Percent = 50
		if in25 && !release.Selected(id, m50) {
			t.Fatalf("%s was in 25%% but not in 50%%", id)
		}
	}
	if chosen < 180 || chosen > 320 {
		t.Errorf("25%% chose %d of 1000", chosen)
	}
	m.SpreadMinutes = 60
	start := release.StartTime("dev-1", m)
	if start.Before(m.IssuedAt) || start.After(m.IssuedAt.Add(time.Hour)) || !release.StartTime("dev-1", m).Equal(start) {
		t.Errorf("start time %v not stable within the spread", start)
	}
}

func TestHandleDecisions(t *testing.T) {
	old := version.Version
	version.Version = "1.4.0"
	defer func() { version.Version = old }()
	e := newEnv(t)
	h := signdoc.Header{Version: 1}

	res, err := e.u.Handle(nil, payload(e.manifest(func(m *Manifest) { m.ReleaseVersion = "1.4.0" })), h)
	if err != nil || !strings.Contains(res, "already running") {
		t.Errorf("same version: %q %v", res, err)
	}
	res, err = e.u.Handle(nil, payload(e.manifest(func(m *Manifest) { m.ReleaseVersion = "1.3.0" })), h)
	if err != nil || !strings.Contains(res, "newer") {
		t.Errorf("older without allowDowngrade: %q %v", res, err)
	}
	if _, err = e.u.Handle(nil, payload(e.manifest(func(m *Manifest) { m.MinVersion = "1.4.5" })), h); err == nil {
		t.Error("minVersion not met should fail")
	}
	e.u.asService = false
	if _, err = e.u.Handle(nil, payload(e.manifest(nil)), h); err == nil {
		t.Error("a console run must not update")
	}
	e.u.asService = true
	// Spread pushes the start into the future: scheduled, and a repeat is recognised.
	future := e.manifest(func(m *Manifest) { m.IssuedAt = time.Now(); m.SpreadMinutes = 600 })
	res, err = e.u.Handle(nil, payload(future), signdoc.Header{Version: 5})
	if !errors.Is(err, control.ErrScheduled) || !strings.Contains(res, "scheduled in") {
		t.Errorf("scheduled: %q %v", res, err)
	}
	if res, err = e.u.Handle(nil, payload(future), signdoc.Header{Version: 5}); !strings.Contains(res, "already scheduled") {
		t.Errorf("repeat: %q %v", res, err)
	}
	// A manifest this device already rolled back is ignored (no download), while
	// a newer manifest for the same release is tried again.
	if err := release.WriteJSON(release.TrialPath(e.u.dataDir), release.Trial{ReleaseVersion: "1.5.0", DocVersion: 7, Outcome: "rolled_back"}); err != nil {
		t.Fatal(err)
	}
	if res, err = e.u.Handle(nil, payload(e.manifest(nil)), signdoc.Header{Version: 7}); err != nil || !strings.Contains(res, "rolled back") {
		t.Errorf("rolled-back manifest: %q %v", res, err)
	}
	if _, err = e.u.Handle(nil, payload(e.manifest(nil)), signdoc.Header{Version: 8}); !errors.Is(err, control.ErrScheduled) {
		t.Errorf("a new manifest after a rollback should be scheduled: %v", err)
	}
}

func TestDownloadHandsOver(t *testing.T) {
	old := version.Version
	version.Version = "1.4.0"
	defer func() { version.Version = old }()
	e := newEnv(t)
	m := e.manifest(nil)
	e.u.run(context.Background(), m, 7, []byte(`{"signed":"envelope"}`), time.Now())
	if !strings.Contains(e.log.text(), "UPDATE/handed_over version=1.5.0") || e.started != 1 {
		t.Fatalf("helper started %d times; log:\n%s", e.started, e.log.text())
	}
	var req release.Request
	if ok, err := release.ReadJSON(release.RequestPath(e.u.dataDir), release.MaxRequestBytes, &req); !ok || err != nil {
		t.Fatalf("request.json: %v %v", ok, err)
	}
	if req.File != "exmon-1.5.0.exe" || string(req.Envelope) != `{"signed":"envelope"}` {
		t.Errorf("request = %+v (%s)", req, req.Envelope)
	}
	if b, _ := os.ReadFile(filepath.Join(release.IncomingDir(e.u.dataDir), req.File)); string(b) != string(body) {
		t.Errorf("staged file: %q", b)
	}
}

func TestDownloadRefusals(t *testing.T) {
	cases := []struct {
		name string
		mod  func(*Manifest)
		auth func(string) (*x509.Certificate, error)
		want string
	}{
		{"wrong hash", func(m *Manifest) { m.SHA256 = strings.Repeat("0", 64) }, nil, "sha256"},
		{"too long", func(m *Manifest) { m.Size = 5 }, nil, "expected 5"},
		{"redirect off the allow-list", func(m *Manifest) { m.URL = strings.Replace(m.URL, "/exmon.exe", "/away", 1) }, nil, "not allowed"},
		{"unsigned", nil, func(string) (*x509.Certificate, error) { return nil, errors.New("no signature") }, "no signature"},
		{"other publisher", nil, func(string) (*x509.Certificate, error) {
			return &x509.Certificate{Subject: pkix.Name{CommonName: "Mallory"}}, nil
		}, "not the expected publisher"},
	}
	for _, c := range cases {
		e := newEnv(t)
		if c.auth != nil {
			e.u.authentic = c.auth
		}
		e.u.run(context.Background(), e.manifest(c.mod), 1, []byte("{}"), time.Now())
		if !strings.Contains(e.log.text(), "UPDATE/failed") || !strings.Contains(e.log.text(), c.want) {
			t.Errorf("%s: log:\n%s", c.name, e.log.text())
		}
		if e.started != 0 {
			t.Errorf("%s: helper started", c.name)
		}
		if _, err := os.Stat(release.RequestPath(e.u.dataDir)); err == nil {
			t.Errorf("%s: a request was written", c.name)
		}
	}
}

func TestCheckOnStartRolledBack(t *testing.T) {
	dir := t.TempDir()
	log := &logRec{}
	release.WriteJSON(release.TrialPath(dir), release.Trial{ReleaseVersion: "9.9.9", DocVersion: 4, Outcome: "rolled_back", Detail: "not healthy"})
	CheckOnStart(context.Background(), dir, log, func() bool { return true })
	CheckOnStart(context.Background(), dir, log, func() bool { return true }) // a second start: not logged again
	if n := strings.Count(log.text(), "UPDATE/rolled_back version=9.9.9"); n != 1 {
		t.Errorf("logged %d times: %s", n, log.text())
	}
}
