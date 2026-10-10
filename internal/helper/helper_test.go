package helper

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"exmon/internal/release"
	"exmon/internal/signdoc"
)

var newExe = []byte("MZ exmon 1.5.0")

type fakeServices struct {
	mu      sync.Mutex
	calls   []string
	onStart func()
}

func (f *fakeServices) Stop(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "stop "+name)
	return nil
}

func (f *fakeServices) Start(name string) error {
	f.mu.Lock()
	f.calls = append(f.calls, "start "+name)
	f.mu.Unlock()
	if f.onStart != nil {
		f.onStart()
	}
	return nil
}

type env struct {
	cfg     Config
	d       Deps
	svcs    *fakeServices
	release signdoc.FileSigner
	logs    []string
	now     time.Time
}

func newEnv(t *testing.T) *env {
	dir := t.TempDir()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	e := &env{svcs: &fakeServices{}, release: signdoc.FileSigner{ID: "rel", Key: k}, now: time.Now()}
	e.cfg = Config{DataDir: filepath.Join(dir, "data"), InstallDir: filepath.Join(dir, "pf"), Service: "exmon",
		PublisherCN: "Contoso Ltd", RequireAuthenticode: true, HealthMinutes: 10, MaxSizeMB: 8}
	os.MkdirAll(e.cfg.InstallDir, 0o755)
	os.WriteFile(filepath.Join(e.cfg.InstallDir, "exmon.exe"), []byte("MZ exmon 1.4.0"), 0o644)
	e.d = Deps{
		Keys: signdoc.Keys{Config: signdoc.KeySet{}, Release: signdoc.KeySet{"rel": &k.PublicKey}},
		Now:  func() time.Time { return e.now },
		Authentic: func(string) (*x509.Certificate, error) {
			return &x509.Certificate{Subject: pkix.Name{CommonName: "Contoso Ltd"}}, nil
		},
		InstalledVersion: func(string) (string, error) { return "1.4.0", nil },
		Services:         e.svcs,
		Log:              func(_ bool, msg string) { e.logs = append(e.logs, msg) },
		Poll:             time.Millisecond,
	}
	return e
}

// stage writes what exmon would: the staged exe and request.json.
func (e *env) stage(t *testing.T, file []byte, mod func(map[string]any), signer signdoc.Signer) {
	sum := sha256.Sum256(newExe)
	m := map[string]any{"kind": signdoc.KindRelease, "version": 100, "issuedAt": e.now.Add(-time.Minute),
		"expiresAt": e.now.Add(time.Hour), "releaseVersion": "1.5.0", "url": "https://dl.example.com/exmon.exe",
		"sha256": hex.EncodeToString(sum[:]), "size": len(newExe)}
	if mod != nil {
		mod(m)
	}
	payload, _ := json.Marshal(m)
	if signer == nil {
		signer = e.release
	}
	env, err := signdoc.Seal(payload, signer)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(release.IncomingDir(e.cfg.DataDir), 0o755)
	os.WriteFile(filepath.Join(release.IncomingDir(e.cfg.DataDir), "exmon-1.5.0.exe"), file, 0o644)
	if err := release.WriteJSON(release.RequestPath(e.cfg.DataDir), release.Request{Envelope: env, File: "exmon-1.5.0.exe"}); err != nil {
		t.Fatal(err)
	}
}

func (e *env) installed() string {
	b, _ := os.ReadFile(filepath.Join(e.cfg.InstallDir, "exmon.exe"))
	return string(b)
}

func (e *env) trial() release.Trial {
	var tr release.Trial
	release.ReadJSON(release.TrialPath(e.cfg.DataDir), release.MaxRequestBytes, &tr)
	return tr
}

func TestUpdateHealthy(t *testing.T) {
	e := newEnv(t)
	e.stage(t, newExe, nil, nil)
	// When the service starts on the new exe, it reports healthy (as exmon would).
	e.svcs.onStart = func() {
		release.WriteJSON(release.HealthPath(e.cfg.DataDir), release.Health{DocVersion: 100, Version: "1.5.0", Healthy: true})
	}
	if err := Run(context.Background(), e.cfg, e.d); err != nil {
		t.Fatal(err)
	}
	if e.installed() != string(newExe) {
		t.Errorf("new exe not installed: %q", e.installed())
	}
	if b, _ := os.ReadFile(filepath.Join(e.cfg.InstallDir, "exmon.old.exe")); string(b) != "MZ exmon 1.4.0" {
		t.Errorf("old exe not kept: %q", b)
	}
	if tr := e.trial(); tr.Outcome != "healthy" || tr.PreviousVersion != "1.4.0" {
		t.Errorf("trial: %+v", tr)
	}
	if _, err := os.Stat(release.RequestPath(e.cfg.DataDir)); err == nil {
		t.Error("request should be cleared")
	}
	if strings.Join(e.svcs.calls, ",") != "stop exmon,start exmon" {
		t.Errorf("service calls: %v", e.svcs.calls)
	}
	// The same manifest again is refused as a replay.
	e.d.InstalledVersion = func(string) (string, error) { return "1.4.0", nil }
	e.stage(t, newExe, nil, nil)
	os.Remove(release.TrialPath(e.cfg.DataDir))
	Run(context.Background(), e.cfg, e.d)
	if !strings.Contains(strings.Join(e.logs, "\n"), "not newer than the last applied") {
		t.Errorf("replay not refused: %v", e.logs)
	}
}

func TestUpdateRolledBack(t *testing.T) {
	e := newEnv(t)
	e.stage(t, newExe, nil, nil)
	calls := 0
	e.d.Now = func() time.Time { // time runs out after a few polls
		calls++
		return e.now.Add(time.Duration(calls) * 3 * time.Minute)
	}
	if err := Run(context.Background(), e.cfg, e.d); err != nil {
		t.Fatal(err)
	}
	if e.installed() != "MZ exmon 1.4.0" {
		t.Errorf("old exe not restored: %q", e.installed())
	}
	if b, _ := os.ReadFile(filepath.Join(e.cfg.InstallDir, "exmon.failed.exe")); string(b) != string(newExe) {
		t.Errorf("failed exe not kept: %q", b)
	}
	if tr := e.trial(); tr.Outcome != "rolled_back" {
		t.Errorf("trial: %+v", tr)
	}
}

func TestUpdateRefusals(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	stranger := signdoc.FileSigner{ID: "rel", Key: k} // right id, wrong key
	cases := []struct {
		name   string
		file   []byte
		mod    func(map[string]any)
		signer signdoc.Signer
		setup  func(*env)
		want   string
	}{
		{"signed by another key", newExe, nil, stranger, nil, "bad signature"},
		{"file changed after the download", []byte("MZ evil!!!!!!!"), nil, nil, nil, "sha256"},
		{"downgrade without permission", newExe, func(m map[string]any) { m["releaseVersion"] = "1.3.0" }, nil, nil, "older than the installed"},
		{"expired", newExe, func(m map[string]any) { m["expiresAt"] = time.Now().Add(-time.Hour) }, nil, nil, "expired"},
		{"too big for the helper", newExe, func(m map[string]any) { m["size"] = 100 << 20 }, nil, nil, "size"},
		{"unsigned exe", newExe, nil, nil, func(e *env) {
			e.d.Authentic = func(string) (*x509.Certificate, error) { return nil, errors.New("no signature") }
		}, "code signature"},
		{"wrong publisher", newExe, nil, nil, func(e *env) {
			e.d.Authentic = func(string) (*x509.Certificate, error) {
				return &x509.Certificate{Subject: pkix.Name{CommonName: "Mallory"}}, nil
			}
		}, "not \"Contoso Ltd\""},
		{"older manifest than the last applied", newExe, nil, nil, func(e *env) {
			release.WriteJSON(release.HelperStatePath(e.cfg.DataDir), helperState{LastDocVersion: 500})
		}, "not newer"},
	}
	for _, c := range cases {
		e := newEnv(t)
		if c.setup != nil {
			c.setup(e)
		}
		e.stage(t, c.file, c.mod, c.signer)
		if err := Run(context.Background(), e.cfg, e.d); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		log := strings.Join(e.logs, "\n")
		if !strings.Contains(log, "update refused") || !strings.Contains(log, c.want) {
			t.Errorf("%s: log: %s", c.name, log)
		}
		if e.installed() != "MZ exmon 1.4.0" || len(e.svcs.calls) != 0 {
			t.Errorf("%s: something changed (exe %q, services %v)", c.name, e.installed(), e.svcs.calls)
		}
		if _, err := os.Stat(release.RequestPath(e.cfg.DataDir)); err == nil {
			t.Errorf("%s: refused request should be cleared", c.name)
		}
	}
}

func TestBadRequestFileName(t *testing.T) {
	e := newEnv(t)
	e.stage(t, newExe, nil, nil)
	var req release.Request
	release.ReadJSON(release.RequestPath(e.cfg.DataDir), release.MaxRequestBytes, &req)
	req.File = `..\..\Windows\System32\cmd.exe`
	release.WriteJSON(release.RequestPath(e.cfg.DataDir), req)
	Run(context.Background(), e.cfg, e.d)
	if !strings.Contains(strings.Join(e.logs, "\n"), "not an exmon-<version>.exe name") || e.installed() != "MZ exmon 1.4.0" {
		t.Errorf("path in request not refused: %v", e.logs)
	}
}
