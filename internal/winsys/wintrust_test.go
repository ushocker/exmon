package winsys

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyAuthenticode(t *testing.T) {
	// A Windows system file is signed by Microsoft.
	sys := filepath.Join(os.Getenv("SystemRoot"), "explorer.exe")
	cert, err := VerifyAuthenticode(sys)
	if err != nil {
		t.Skipf("couldn't verify %s here (offline revocation check?): %v", sys, err)
	}
	if !strings.Contains(cert.Subject.CommonName, "Microsoft") {
		t.Errorf("signer of %s is %q", sys, cert.Subject.CommonName)
	}

	// An unsigned file fails.
	p := filepath.Join(t.TempDir(), "unsigned.exe")
	os.WriteFile(p, []byte("MZ not really a program"), 0o644)
	if _, err := VerifyAuthenticode(p); err == nil {
		t.Error("an unsigned file must not verify")
	}
}
