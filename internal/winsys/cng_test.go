package winsys

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// testKeyName is unique per run, so a crashed test never collides with a later one.
func testKeyName(t *testing.T) string {
	return fmt.Sprintf("exmon-test-%s-%d-%d", t.Name(), os.Getpid(), time.Now().UnixNano())
}

func TestSoftwareKeySignVerify(t *testing.T) {
	digest := sha256.Sum256([]byte("hello exmon"))
	for _, alg := range []KeyAlgorithm{ECDSAP256, RSA2048} {
		name := testKeyName(t)
		s, err := CreateKey(ProviderSoftware, name, alg, false, false) // a per-user key: no admin needed
		if err != nil {
			t.Fatalf("%s: create: %v", alg, err)
		}
		t.Cleanup(func() { DeleteKey(ProviderSoftware, name, false) })

		switch pub := s.Public().(type) {
		case *ecdsa.PublicKey:
			sig, err := s.Sign(rand.Reader, digest[:], crypto.SHA256)
			if err != nil || !ecdsa.VerifyASN1(pub, digest[:], sig) {
				t.Errorf("ECDSA signature doesn't verify: %v", err)
			}
		case *rsa.PublicKey:
			if pub.N.BitLen() != 2048 {
				t.Errorf("RSA key is %d bits", pub.N.BitLen())
			}
			sig, err := s.Sign(rand.Reader, digest[:], crypto.SHA256)
			if err != nil || rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig) != nil {
				t.Errorf("PKCS#1 signature doesn't verify: %v", err)
			}
			pss := &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: crypto.SHA256}
			sig, err = s.Sign(rand.Reader, digest[:], pss)
			if err != nil || rsa.VerifyPSS(pub, crypto.SHA256, digest[:], sig, pss) != nil {
				t.Errorf("PSS signature doesn't verify: %v", err)
			}
		default:
			t.Fatalf("unexpected public key %T", pub)
		}

		// Reopening by name gives the same public key.
		s.Close()
		again, err := OpenKey(ProviderSoftware, name, false)
		if err != nil {
			t.Fatalf("%s: reopen: %v", alg, err)
		}
		if !publicEqual(again.Public(), s.Public()) {
			t.Errorf("%s: reopened key differs", alg)
		}
		if err := again.Delete(); err != nil {
			t.Errorf("%s: delete: %v", alg, err)
		}
		if _, err := OpenKey(ProviderSoftware, name, false); !errors.Is(err, ErrKeyNotFound) {
			t.Errorf("%s: after delete, open gave %v, want ErrKeyNotFound", alg, err)
		}
	}
}

func publicEqual(a, b crypto.PublicKey) bool {
	type equaler interface{ Equal(crypto.PublicKey) bool }
	e, ok := a.(equaler)
	return ok && e.Equal(b)
}

func TestECDSAToASN1(t *testing.T) {
	raw := make([]byte, 64)
	raw[31], raw[63] = 1, 2
	der, err := ecdsaToASN1(raw)
	if err != nil || len(der) != 8 { // SEQUENCE{INTEGER 1, INTEGER 2}
		t.Errorf("der = %x, %v", der, err)
	}
	if _, err := ecdsaToASN1(raw[:63]); err == nil {
		t.Error("odd length should fail")
	}
}

// A smart card (YubiKey PIV) reports its P-256 key as an ECDH blob ("ECK1");
// it must parse the same as an ECDSA one ("ECS1").
func TestParseECCPublicBlobMagics(t *testing.T) {
	for _, magic := range []uint32{magicECDSAP256, magicECDHP256} {
		b := make([]byte, 8+64)
		binary.LittleEndian.PutUint32(b, magic)
		binary.LittleEndian.PutUint32(b[4:], 32)
		b[8+31], b[8+63] = 1, 2
		pub, err := parseECCPublicBlob(b)
		if err != nil {
			t.Fatalf("magic 0x%08X: %v", magic, err)
		}
		if pub.Curve.Params().BitSize != 256 || pub.X.Int64() != 1 || pub.Y.Int64() != 2 {
			t.Errorf("magic 0x%08X: got %v X=%v Y=%v", magic, pub.Curve.Params().Name, pub.X, pub.Y)
		}
	}
	b := make([]byte, 8+64)
	binary.LittleEndian.PutUint32(b, 0x12345678)
	if _, err := parseECCPublicBlob(b); err == nil {
		t.Error("unknown magic should fail")
	}
}

// TestTPMKey only runs on machines with a TPM and as administrator (machine keys).
func TestTPMKey(t *testing.T) {
	if !IsElevated() {
		t.Skip("needs an elevated test run")
	}
	name := testKeyName(t)
	s, err := CreateKey(ProviderTPM, name, ECDSAP256, true, false)
	if err != nil {
		t.Skipf("no usable TPM: %v", err)
	}
	defer s.Delete()
	digest := sha256.Sum256([]byte("tpm"))
	sig, err := s.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil || !ecdsa.VerifyASN1(s.Public().(*ecdsa.PublicKey), digest[:], sig) {
		t.Errorf("TPM signature: %v", err)
	}
}
