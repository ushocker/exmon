// Package enroll gives a test device its MQTT client certificate without Intune:
//
//	exmon enroll new --name exmon-test-01   create a TPM key and a certificate request
//	exmon enroll sign-local                 sign it with the YubiKey test CA plugged into this PC
//	exmon enroll import --cert x.pem        or: install a certificate signed elsewhere
//
// Production devices get their certificate (and TPM key) from an Intune SCEP
// profile instead and never run these commands.
//
// The device key is created in the TPM and can't be exported; the certificate
// request only carries the public key. The CA's key never leaves the YubiKey: it
// signs through the Windows smart-card key storage provider, which needs the PIN
// and (with the touch policy from the setup guide) a touch for every signature.
package enroll

import (
	"crypto"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"exmon/internal/winsys"
)

// TestNameRE is the shape of test device names the YubiKey CA will sign, so a
// mistaken name (or someone trying to mint a production-looking identity) is
// refused. Event Grid's dev namespace has a matching pool of clients.
var TestNameRE = regexp.MustCompile(`^exmon-test-[0-9]{2,3}$`)

// DefaultCAName is the test CA certificate's common name (see the test setup guide).
const DefaultCAName = "exmon Test Device CA"

// State records the enrolment, so later steps know which key to use. It lives in
// %ProgramData%\exmon\state, which only administrators can read or change.
type State struct {
	Name     string `json:"name"`     // the device's authentication name (certificate CN)
	Provider string `json:"provider"` // key storage provider
	KeyName  string `json:"keyName"`  // key container name in that provider
	Machine  bool   `json:"machine"`  // a machine key (always, except in tests)
}

// StatePath is where State is kept.
func StatePath(dataDir string) string { return filepath.Join(dataDir, "state", "enrollment.json") }

// Save writes the state file.
func (s State) Save(dataDir string) error {
	p := StatePath(dataDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(p, b, 0o644)
}

// LoadState reads the state file written by NewRequest.
func LoadState(dataDir string) (State, error) {
	var s State
	b, err := os.ReadFile(StatePath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return s, errors.New("not enrolled yet: run `exmon enroll new --name exmon-test-NN` first")
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

// NewRequest creates the device key (in the TPM unless allowSoftware) and returns
// a PEM certificate request for CN=name.
func NewRequest(name string, allowSoftware bool) (State, []byte, error) {
	if !TestNameRE.MatchString(name) {
		return State{}, nil, fmt.Errorf("name %q must look like exmon-test-01 (the dev namespace's client pool)", name)
	}
	st := State{Name: name, Provider: winsys.ProviderTPM, KeyName: "exmon-device-" + name, Machine: true}
	csr, err := createRequest(st)
	if err != nil && allowSoftware {
		st.Provider = winsys.ProviderSoftware
		csr, err = createRequest(st)
	} else if err != nil {
		err = fmt.Errorf("%w (use --allow-software-key on a VM without a TPM)", err)
	}
	return st, csr, err
}

// createRequest makes (or replaces) the key st describes and a request for it.
func createRequest(st State) ([]byte, error) {
	key, err := winsys.CreateKey(st.Provider, st.KeyName, winsys.ECDSAP256, st.Machine, true)
	if err != nil {
		return nil, fmt.Errorf("create a key in %s: %w", st.Provider, err)
	}
	defer key.Close()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: st.Name},
	}, key)
	if err != nil {
		return nil, fmt.Errorf("certificate request: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// ParseRequest decodes a PEM certificate request and checks its signature (proof
// that whoever made it holds the private key) and its name.
func ParseRequest(pemBytes []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("not a PEM certificate request")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("certificate request signature: %w", err)
	}
	if !TestNameRE.MatchString(csr.Subject.CommonName) {
		return nil, fmt.Errorf("refusing to sign %q: test certificates must be named like exmon-test-01", csr.Subject.CommonName)
	}
	return csr, nil
}

// FindCA finds the test CA certificate (and its YubiKey key) in the current user's
// Personal store. Windows copies certificates from a plugged-in smart card there
// automatically (the Certificate Propagation service).
func FindCA(cn string) (*winsys.ClientCert, error) {
	ca, err := winsys.FindClientCert(winsys.CurrentUser, "My", func(c *x509.Certificate) bool {
		return c.IsCA && c.Subject.CommonName == cn
	}, true)
	if err != nil {
		return nil, fmt.Errorf("test CA %q not found: is the YubiKey plugged in, and was `exmonctl ca init` run? (%w)", cn, err)
	}
	return ca, nil
}

// SignDevice issues the device certificate: CN = the request's name, client
// authentication only, valid for days (1-90).
func SignDevice(ca *winsys.ClientCert, csr *x509.CertificateRequest, days int) ([]byte, error) {
	if days < 1 || days > 90 {
		return nil, fmt.Errorf("days %d is out of range 1-90", days)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: csr.Subject.CommonName},
		NotBefore:             now.Add(-5 * time.Minute), // allow for small clock differences
		NotAfter:              now.Add(time.Duration(days) * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	return x509.CreateCertificate(rand.Reader, tmpl, ca.Leaf, csr.PublicKey, ca.Signer)
}

// Import links a signed certificate to the device key recorded in st and adds
// it to the machine's Personal store, after checking it really is for that key.
func Import(st State, der []byte) (*x509.Certificate, error) {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	key, err := winsys.OpenKey(st.Provider, st.KeyName, st.Machine)
	if err != nil {
		return nil, fmt.Errorf("device key %s: %w", st.KeyName, err)
	}
	defer key.Close()
	type equaler interface{ Equal(crypto.PublicKey) bool }
	if pub, ok := cert.PublicKey.(equaler); !ok || !pub.Equal(key.Public()) {
		return nil, errors.New("this certificate is not for this device's key (was it made from this PC's request?)")
	}
	if cert.Subject.CommonName != st.Name {
		return nil, fmt.Errorf("certificate is for %q, but this device enrolled as %q", cert.Subject.CommonName, st.Name)
	}
	loc := winsys.LocalMachine
	if !st.Machine {
		loc = winsys.CurrentUser
	}
	if err := winsys.ImportCert(loc, "My", der, st.Provider, st.KeyName); err != nil {
		return nil, err
	}
	return cert, nil
}

// ParseCertPEM reads a PEM (or raw DER) certificate.
func ParseCertPEM(b []byte) ([]byte, error) {
	if block, _ := pem.Decode(b); block != nil {
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("PEM block is %q, want CERTIFICATE", block.Type)
		}
		return block.Bytes, nil
	}
	if _, err := x509.ParseCertificate(b); err != nil {
		return nil, errors.New("not a PEM or DER certificate")
	}
	return b, nil
}

// MakeCA creates a self-signed CA certificate for a key, typically the YubiKey's
// slot 9c key: CA:true, may only sign end-entity certificates (path length 0),
// valid for years.
func MakeCA(signer crypto.Signer, cn string, years int) ([]byte, error) {
	if years < 1 || years > 10 {
		return nil, fmt.Errorf("years %d is out of range 1-10", years)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return nil, err
	}
	ski := sha1.Sum(pubDER) // subject key identifier: a hash of the public key
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(years, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true, // "0" means exactly zero, not "unset"
		SubjectKeyId:          ski[:],
	}
	return x509.CreateCertificate(rand.Reader, tmpl, tmpl, signer.Public(), signer)
}

// PEMCert encodes a DER certificate as PEM.
func PEMCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
