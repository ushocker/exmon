package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"exmon/internal/signdoc"
)

func TestSealVerifies(t *testing.T) {
	priv, pub, err := signdoc.NewDevKey()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "dev.pem")
	os.WriteFile(keyFile, priv, 0o600)
	pk, _ := signdoc.ParsePublicKey(pub)
	keys := signdoc.Keys{Config: signdoc.KeySet{"dev": pk}, Release: signdoc.KeySet{"dev": pk}}

	p := Profile{keyFile: keyFile}
	env, ver, err := seal(p, signdoc.KindCommand, map[string]any{"command": "report-status"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, h, err := signdoc.Verify(env, signdoc.KindCommand, keys, "exmon-test-01", time.Now())
	if err != nil || h.Version != ver || ver < 20260000000000 {
		t.Errorf("verify: %v (version %d)", err, ver)
	}
	if _, err := (Profile{}).signer(signdoc.KindRelease); err == nil {
		t.Error("no key configured should be an error")
	}
}
