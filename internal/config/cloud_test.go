package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"exmon/internal/signdoc"
)

// cloudEnv points the cloud layer at a temporary folder and a test signing key,
// and writes a local config with the control plane on.
type cloudEnv struct {
	local  string
	signer signdoc.FileSigner
}

func newCloudEnv(t *testing.T) *cloudEnv {
	dir := t.TempDir()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	e := &cloudEnv{local: filepath.Join(dir, "exmon.json"), signer: signdoc.FileSigner{ID: "test", Key: k}}
	oldDir, oldKeys := cloudDir, cloudKeys
	cloudDir = func() string { return filepath.Join(dir, "cloud") }
	cloudKeys = func() (signdoc.Keys, error) {
		return signdoc.Keys{Config: signdoc.KeySet{"test": &k.PublicKey}, Release: signdoc.KeySet{}}, nil
	}
	t.Cleanup(func() { cloudDir, cloudKeys = oldDir, oldKeys })
	os.WriteFile(e.local, []byte(`{"control": {"enabled": true, "host": "ns.eastus-1.ts.eventgrid.azure.net",
		"cert": {"subjectCN": "exmon-test-01"}}, "probe": {"intervalSeconds": 30}}`), 0o644)
	return e
}

func (e *cloudEnv) envelope(t *testing.T, version int64, config string) []byte {
	p := map[string]any{"kind": signdoc.KindConfig, "version": version, "issuedAt": time.Now().Add(-time.Minute),
		"expiresAt": time.Now().Add(time.Hour), "config": json.RawMessage(config)}
	b, _ := json.Marshal(p)
	env, err := signdoc.Seal(b, e.signer)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestCloudKeysAllowed(t *testing.T) {
	refused := []string{
		`{"log": {"path": "C:\\x.log"}}`,
		`{"incident": {"dir": "C:\\x"}}`,
		`{"baseline": {"path": "C:\\x.jsonl"}}`,
		`{"control": {"host": "evil.example.com"}}`,
		`{"outputRoots": ["C:\\"]}`,
		`{"remotes": [{"name": "r", "type": "azureMonitor", "spoolDir": "C:\\x"}]}`,
	}
	for _, c := range refused {
		if err := checkCloudKeys(json.RawMessage(c)); err == nil {
			t.Errorf("cloud config should not be able to set %s", c)
		}
	}
	if err := checkCloudKeys(json.RawMessage(`{"probe": {"intervalSeconds": 600}, "control": {"chirpMinutes": 30}}`)); err != nil {
		t.Errorf("allowed keys refused: %v", err)
	}
}

func TestCloudLayer(t *testing.T) {
	e := newCloudEnv(t)
	args := []string{"--config", e.local}

	// Without a cloud config: the local value.
	opts, err := Load(args)
	if err != nil || opts.Probe.IntervalSeconds != 30 || opts.CloudVersion != 0 {
		t.Fatalf("local only: interval %d, cloud %d, %v", opts.Probe.IntervalSeconds, opts.CloudVersion, err)
	}

	// A received cloud config is tried first, then installed: it overrides the
	// local file, and flags still override it.
	env := e.envelope(t, 7, `{"probe": {"intervalSeconds": 600}, "control": {"chirpMinutes": 30}}`)
	if _, err := TestCloud(args, nil, env); err != nil {
		t.Fatalf("TestCloud: %v", err)
	}
	if err := InstallCloud(env); err != nil {
		t.Fatal(err)
	}
	opts, err = Load(args)
	if err != nil || opts.Probe.IntervalSeconds != 600 || opts.Control.ChirpMinutes != 30 || opts.CloudVersion != 7 {
		t.Fatalf("with cloud: interval %d chirp %d version %d %v", opts.Probe.IntervalSeconds, opts.Control.ChirpMinutes, opts.CloudVersion, err)
	}
	if opts, _ = Load(append(args, "--probe-interval-seconds", "120")); opts.Probe.IntervalSeconds != 120 {
		t.Errorf("flags must override the cloud layer, got %d", opts.Probe.IntervalSeconds)
	}

	// A second one; reverting brings the first back.
	env2 := e.envelope(t, 8, `{"probe": {"intervalSeconds": 900}}`)
	if err := InstallCloud(env2); err != nil {
		t.Fatal(err)
	}
	if err := RevertCloud(); err != nil {
		t.Fatal(err)
	}
	if opts, _ = Load(args); opts.CloudVersion != 7 {
		t.Errorf("after revert: version %d, want 7", opts.CloudVersion)
	}

	// Tampering with the file on disk: it is ignored with a warning.
	os.WriteFile(activePath(), []byte(strings.Replace(string(env), `"ES256"`, `"ES256" `, 1)+" "), 0o644)
	b, _ := os.ReadFile(activePath())
	b[len(b)-20] ^= 1
	os.WriteFile(activePath(), b, 0o644)
	opts, err = Load(args)
	if err != nil || opts.CloudVersion != 0 || len(opts.ConfigWarnings) == 0 {
		t.Errorf("tampered cloud config: version %d, warnings %v, err %v", opts.CloudVersion, opts.ConfigWarnings, err)
	}

	// Control plane off: the cloud layer isn't used at all.
	InstallCloud(env)
	if opts, _ = Load(append(args, "--control-enabled=false")); opts.CloudVersion != 0 {
		t.Error("cloud config used with the control plane off")
	}
}

func TestCloudLimits(t *testing.T) {
	e := newCloudEnv(t)
	args := []string{"--config", e.local}
	bad := []string{
		`{"probe": {"intervalSeconds": 30}}`,                                            // under the cloud minimum (the local 30 is fine)
		`{"remotes": [{"name": "r", "type": "outbox", "settings": {"path": "C:\\x"}}]}`, // writes local files
		`{"probe": {"surprise": 1}}`,                                                    // unknown key
	}
	for _, c := range bad {
		if _, err := TestCloud(args, nil, e.envelope(t, 9, c)); err == nil {
			t.Errorf("cloud config %s should be refused", c)
		}
	}
	// Signed with an unknown key.
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	other := cloudEnv{signer: signdoc.FileSigner{ID: "test", Key: k}}
	if _, err := TestCloud(args, nil, other.envelope(t, 9, `{}`)); err == nil {
		t.Error("a config signed by another key should be refused")
	}
}

func TestPending(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pending.json")
	if got, err := ReadPending(p); got != nil || err != nil {
		t.Fatalf("none yet: %v %v", got, err)
	}
	want := &Pending{Kind: "config", Version: 3, Deadline: time.Now().Round(time.Second), Starts: 1}
	if err := WritePending(p, want); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadPending(p)
	if got.Version != 3 || got.Starts != 1 || !got.Deadline.Equal(want.Deadline) {
		t.Errorf("round trip: %+v", got)
	}
	WritePending(p, nil)
	if got, _ := ReadPending(p); got != nil {
		t.Error("nil should remove it")
	}
}

func TestValidateCloudConfig(t *testing.T) {
	if err := ValidateCloudConfig(json.RawMessage(`{"probe": {"intervalSeconds": 600}}`)); err != nil {
		t.Errorf("valid: %v", err)
	}
	for _, c := range []string{`{"probe": {"intervalSeconds": 30}}`, `{"log": {}}`, `{"wifi": {"pollSeconds": 0}}`} {
		if ValidateCloudConfig(json.RawMessage(c)) == nil {
			t.Errorf("%s should fail", c)
		}
	}
}
