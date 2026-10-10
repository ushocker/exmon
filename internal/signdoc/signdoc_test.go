package signdoc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func newSigner(t testing.TB, id string) FileSigner {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return FileSigner{ID: id, Key: k}
}

type testPayload struct {
	Header
	Setting string `json:"setting"`
}

func payload(kind string, mod func(*testPayload)) []byte {
	p := testPayload{Header: Header{Kind: kind, Version: 20261007120000, IssuedAt: now.Add(-time.Minute),
		ExpiresAt: now.Add(24 * time.Hour)}, Setting: "x"}
	if mod != nil {
		mod(&p)
	}
	b, _ := json.Marshal(p)
	return b
}

func TestVerify(t *testing.T) {
	cfg, rel, other := newSigner(t, "cfg-1"), newSigner(t, "rel-1"), newSigner(t, "cfg-1")
	keys := Keys{Config: KeySet{"cfg-1": &cfg.Key.PublicKey}, Release: KeySet{"rel-1": &rel.Key.PublicKey}}
	seal := func(p []byte, s Signer) []byte {
		b, err := Seal(p, s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	good := seal(payload(KindConfig, nil), cfg)
	got, h, err := Verify(good, KindConfig, keys, "dev-1", now)
	if err != nil || h.Version != 20261007120000 {
		t.Fatalf("valid document: %v", err)
	}
	var p testPayload
	if err := DecodeStrict(got, &p); err != nil || p.Setting != "x" {
		t.Errorf("decode: %v %+v", err, p)
	}

	// Tamper with the payload but keep the signature.
	var env Envelope
	json.Unmarshal(good, &env)
	env.Payload = base64.RawURLEncoding.EncodeToString(payload(KindConfig, func(p *testPayload) { p.Setting = "evil" }))
	tampered, _ := json.Marshal(env)

	tests := []struct {
		name string
		doc  []byte
		kind string
		want string
	}{
		{"tampered payload", tampered, KindConfig, "bad signature"},
		{"signed by an untrusted key with a trusted id", seal(payload(KindConfig, nil), other), KindConfig, "bad signature"},
		{"config key can't sign a release", seal(payload(KindRelease, nil), FileSigner{ID: "cfg-1", Key: cfg.Key}), KindRelease, "not trusted"},
		{"wrong kind inside", seal(payload(KindCommand, nil), cfg), KindConfig, "expected"},
		{"expired", seal(payload(KindConfig, func(p *testPayload) { p.ExpiresAt = now.Add(-time.Hour) }), cfg), KindConfig, "expired"},
		{"from the future", seal(payload(KindConfig, func(p *testPayload) { p.IssuedAt = now.Add(time.Hour) }), cfg), KindConfig, "future"},
		{"no version", seal(payload(KindConfig, func(p *testPayload) { p.Version = 0 }), cfg), KindConfig, "version"},
		{"oversize", append(good, make([]byte, MaxSize)...), KindConfig, "limit"},
		{"unknown envelope field", []byte(strings.Replace(string(good), `{"alg"`, `{"extra":1,"alg"`, 1)), KindConfig, "unknown field"},
		{"wrong alg", []byte(strings.Replace(string(good), `"ES256"`, `"none"`, 1)), KindConfig, "algorithm"},
	}
	for _, tt := range tests {
		if _, _, err := Verify(tt.doc, tt.kind, keys, "dev-1", now); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: got %v, want an error containing %q", tt.name, err, tt.want)
		}
	}

	// Targeted at other devices.
	targeted := seal(payload(KindConfig, func(p *testPayload) { p.Target.Devices = []string{"dev-2"} }), cfg)
	if _, _, err := Verify(targeted, KindConfig, keys, "dev-1", now); !errors.Is(err, ErrNotForUs) {
		t.Errorf("other device's document: %v", err)
	}
	if _, _, err := Verify(targeted, KindConfig, keys, "dev-2", now); err != nil {
		t.Errorf("targeted at us: %v", err)
	}

	// Strict decode rejects unknown payload fields (after authentication).
	extra := seal([]byte(strings.Replace(string(payload(KindConfig, nil)), `"setting"`, `"surprise":1,"setting"`, 1)), cfg)
	pl, _, err := Verify(extra, KindConfig, keys, "dev-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if DecodeStrict(pl, &p) == nil {
		t.Error("unknown payload field should fail strict decode")
	}
}

func TestKeyFiles(t *testing.T) {
	priv, pub, err := NewDevKey()
	if err != nil {
		t.Fatal(err)
	}
	p := t.TempDir() + `\dev.pem`
	if err := writeFile(p, priv); err != nil {
		t.Fatal(err)
	}
	s, err := LoadFileSigner(p)
	if err != nil || s.KeyID() != "dev" {
		t.Fatalf("load: %v %q", err, s.ID)
	}
	pk, err := ParsePublicKey(pub)
	if err != nil || !pk.Equal(&s.Key.PublicKey) {
		t.Errorf("public key round trip: %v", err)
	}
	if _, err := BuiltinKeys(); err != nil {
		t.Errorf("built-in keys: %v", err)
	}
}

// FuzzVerify feeds random bytes to Verify: it must never panic, and nothing
// random may ever pass.
func FuzzVerify(f *testing.F) {
	s := newSigner(f, "k")
	keys := Keys{Config: KeySet{"k": &s.Key.PublicKey}, Release: KeySet{}}
	good, _ := Seal(payload(KindConfig, nil), s)
	f.Add(good)
	f.Add([]byte(`{"alg":"ES256","keyId":"k","payload":"","signature":""}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if _, _, err := Verify(b, KindConfig, keys, "d", now); err == nil && string(b) != string(good) {
			// Re-encodings of the same valid document are fine; anything else passing is not.
			var env Envelope
			json.Unmarshal(b, &env)
			var g Envelope
			json.Unmarshal(good, &g)
			if env.Payload != g.Payload || env.Signature != g.Signature {
				t.Fatalf("a modified document verified: %q", b)
			}
		}
	})
}

func writeFile(p string, b []byte) error { return os.WriteFile(p, b, 0o600) }
