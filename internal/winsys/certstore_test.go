package winsys

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestKeyProvInfoLayout(t *testing.T) {
	if got := unsafe.Sizeof(cryptKeyProvInfo{}); got != 48 {
		t.Errorf("CRYPT_KEY_PROV_INFO is %d bytes, want 48 (x64)", got)
	}
	if off := unsafe.Offsetof(cryptKeyProvInfo{}.provParams); off != 32 {
		t.Errorf("rgProvParam at %d, want 32", off)
	}
}

// TestImportAndFind creates a software key, a self-signed certificate for it,
// links the two in an in-memory store, and finds and uses it again: the same path
// `exmon enroll` and the MQTT client take with the real store and the TPM.
func TestImportAndFind(t *testing.T) {
	name := testKeyName(t)
	key, err := CreateKey(ProviderSoftware, name, ECDSAP256, false, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { DeleteKey(ProviderSoftware, name, false) })

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "exmon-test-99"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key) // our Signer signs it
	if err != nil {
		t.Fatalf("create certificate with the CNG signer: %v", err)
	}
	key.Close()

	store, err := openMemoryStore()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CertCloseStore(store, 0)
	if err := importCert(store, der, ProviderSoftware, name, false); err != nil {
		t.Fatal(err)
	}

	byCN := func(c *x509.Certificate) bool { return c.Subject.CommonName == "exmon-test-99" }
	cc, err := findClientCert(store, byCN, false)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	defer cc.Close()
	digest := sha256.Sum256([]byte("linked"))
	sig, err := cc.Signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil || !ecdsa.VerifyASN1(cc.Leaf.PublicKey.(*ecdsa.PublicKey), digest[:], sig) {
		t.Errorf("the found certificate's key doesn't sign for it: %v", err)
	}

	if _, err := findClientCert(store, func(*x509.Certificate) bool { return false }, false); err == nil {
		t.Error("no match should be an error")
	}
	if n, err := removeCerts(store, byCN); n != 1 || err != nil {
		t.Errorf("removed %d, %v", n, err)
	}
}
