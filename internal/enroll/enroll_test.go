package enroll

import (
	"crypto/x509"
	"fmt"
	"os"
	"testing"
	"time"

	"exmon/internal/winsys"
)

// TestEnrollChain runs the whole test-enrolment path with software keys: a CA
// made by MakeCA (standing in for the YubiKey), a device request, signing, and
// import into the current user's store (cleaned up afterwards).
func TestEnrollChain(t *testing.T) {
	suffix := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	caKeyName := "exmon-test-ca-" + suffix
	caKey, err := winsys.CreateKey(winsys.ProviderSoftware, caKeyName, winsys.ECDSAP256, false, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { winsys.DeleteKey(winsys.ProviderSoftware, caKeyName, false) })
	caDER, err := MakeCA(caKey, DefaultCAName, 1)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	if !caCert.IsCA || !caCert.MaxPathLenZero || caCert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Errorf("CA certificate constraints wrong: %+v", caCert)
	}
	ca := &winsys.ClientCert{Leaf: caCert, DER: caDER, Signer: caKey}

	st := State{Name: "exmon-test-77", Provider: winsys.ProviderSoftware, KeyName: "exmon-test-dev-" + suffix}
	t.Cleanup(func() { winsys.DeleteKey(st.Provider, st.KeyName, false) })
	reqPEM, err := createRequest(st)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := ParseRequest(reqPEM)
	if err != nil {
		t.Fatal(err)
	}
	der, err := SignDevice(ca, csr, 30)
	if err != nil {
		t.Fatal(err)
	}
	dev, _ := x509.ParseCertificate(der)
	if err := dev.CheckSignatureFrom(caCert); err != nil {
		t.Errorf("device certificate not signed by the CA: %v", err)
	}
	if dev.IsCA || dev.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || dev.Subject.CommonName != "exmon-test-77" {
		t.Errorf("device certificate fields: %+v", dev)
	}

	byCN := func(c *x509.Certificate) bool {
		return c.Subject.CommonName == "exmon-test-77" && c.SerialNumber.Cmp(dev.SerialNumber) == 0
	}
	t.Cleanup(func() { winsys.RemoveCerts(winsys.CurrentUser, "My", byCN) })
	if _, err := Import(st, der); err != nil {
		t.Fatalf("import: %v", err)
	}
	cc, err := winsys.FindClientCert(winsys.CurrentUser, "My", byCN, false)
	if err != nil {
		t.Fatalf("imported certificate not usable: %v", err)
	}
	cc.Close()

	// A certificate for a different key is refused.
	other := st
	other.KeyName = caKeyName
	if _, err := Import(other, der); err == nil {
		t.Error("importing against the wrong key should fail")
	}
}

func TestRequestNameChecks(t *testing.T) {
	if _, _, err := NewRequest("prod-laptop-1", true); err == nil {
		t.Error("a non-test name should be refused")
	}
	if _, err := SignDevice(&winsys.ClientCert{}, &x509.CertificateRequest{}, 365); err == nil {
		t.Error("365 days should be out of range")
	}
}
