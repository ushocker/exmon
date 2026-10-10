package winsys

// The Windows certificate store: finding the device's client certificate (and a
// Signer for its private key), and importing a certificate linked to a key that
// already exists in a key storage provider (as `exmon enroll` does).

import (
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	crypt32                               = windows.NewLazySystemDLL("crypt32.dll")
	procCertSetCertificateContextProperty = crypt32.NewProc("CertSetCertificateContextProperty")
)

const (
	certKeyProvInfoPropID = 2 // CERT_KEY_PROV_INFO_PROP_ID
	encoding              = windows.X509_ASN_ENCODING | windows.PKCS_7_ASN_ENCODING
)

// StoreLocation picks the machine's or the current user's certificate stores.
type StoreLocation uint32

const (
	LocalMachine StoreLocation = windows.CERT_SYSTEM_STORE_LOCAL_MACHINE
	CurrentUser  StoreLocation = windows.CERT_SYSTEM_STORE_CURRENT_USER
)

// ParseStoreLocation reads "LocalMachine" or "CurrentUser".
func ParseStoreLocation(s string) (StoreLocation, error) {
	switch strings.ToLower(s) {
	case "localmachine", "":
		return LocalMachine, nil
	case "currentuser":
		return CurrentUser, nil
	}
	return 0, fmt.Errorf("certificate store location %q must be LocalMachine or CurrentUser", s)
}

// openSystemStore opens a named system store such as "My" (Personal).
func openSystemStore(loc StoreLocation, name string) (windows.Handle, error) {
	return windows.CertOpenStore(windows.CERT_STORE_PROV_SYSTEM, 0, 0,
		uint32(loc)|windows.CERT_STORE_OPEN_EXISTING_FLAG, uintptr(unsafe.Pointer(utf16(name))))
}

// openMemoryStore opens an empty in-memory store (used by tests).
func openMemoryStore() (windows.Handle, error) {
	return windows.CertOpenStore(windows.CERT_STORE_PROV_MEMORY, 0, 0, 0, 0)
}

// ClientCert is a certificate whose private key we can use.
type ClientCert struct {
	Leaf   *x509.Certificate
	DER    []byte
	Signer *Signer
}

// Close releases the key handle.
func (c *ClientCert) Close() {
	if c.Signer != nil {
		c.Signer.Close()
	}
}

// FindClientCert returns the newest currently valid certificate in the given
// store ("My" = Personal) that match accepts and whose private key is available.
// interactive=false never lets Windows show UI, which is right for a service.
func FindClientCert(loc StoreLocation, store string, match func(*x509.Certificate) bool, interactive bool) (*ClientCert, error) {
	h, err := openSystemStore(loc, store)
	if err != nil {
		return nil, fmt.Errorf("open certificate store %s: %w", store, err)
	}
	defer windows.CertCloseStore(h, 0)
	return findClientCert(h, match, interactive)
}

func findClientCert(store windows.Handle, match func(*x509.Certificate) bool, interactive bool) (*ClientCert, error) {
	now := time.Now()
	var best *windows.CertContext
	var bestCert *x509.Certificate
	var prev *windows.CertContext
	for {
		// CertEnumCertificatesInStore frees prev and returns the next one.
		ctx, err := windows.CertEnumCertificatesInStore(store, prev)
		if err != nil || ctx == nil {
			break
		}
		prev = ctx
		cert, err := x509.ParseCertificate(certBytes(ctx))
		if err != nil || now.Before(cert.NotBefore) || now.After(cert.NotAfter) || !match(cert) {
			continue
		}
		if bestCert != nil && !cert.NotBefore.After(bestCert.NotBefore) {
			continue
		}
		if best != nil {
			windows.CertFreeCertificateContext(best)
		}
		best, bestCert = windows.CertDuplicateCertificateContext(ctx), cert
	}
	if best == nil {
		return nil, errors.New("no matching, currently valid certificate")
	}
	defer windows.CertFreeCertificateContext(best)

	flags := uint32(windows.CRYPT_ACQUIRE_ONLY_NCRYPT_KEY_FLAG)
	if !interactive {
		flags |= windows.CRYPT_ACQUIRE_SILENT_FLAG
	}
	// Without CRYPT_ACQUIRE_CACHE_FLAG Windows hands us our own key handle
	// (callerFree is true), so it stays valid after the context is freed below.
	var key windows.Handle
	var keySpec uint32
	var callerFree bool
	if err := windows.CryptAcquireCertificatePrivateKey(best, flags, nil, &key, &keySpec, &callerFree); err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.Errno(ntePerm)) {
			err = fmt.Errorf("%w (%v)", ErrKeyAccess, err)
		}
		return nil, fmt.Errorf("certificate %q: its private key isn't available: %w", bestCert.Subject.CommonName, err)
	}
	signer, err := newSigner(uintptr(key), callerFree)
	if err != nil {
		return nil, err
	}
	signer.silent = !interactive
	return &ClientCert{Leaf: bestCert, DER: bestCert.Raw, Signer: signer}, nil
}

// certBytes copies the encoded certificate out of a context.
func certBytes(ctx *windows.CertContext) []byte {
	return append([]byte(nil), unsafe.Slice(ctx.EncodedCert, ctx.Length)...)
}

// cryptKeyProvInfo mirrors CRYPT_KEY_PROV_INFO (wincrypt.h). On 64-bit Windows Go
// lays it out the same way C does, padding included (48 bytes; tested).
type cryptKeyProvInfo struct {
	containerName *uint16
	provName      *uint16
	provType      uint32 // 0 for CNG providers
	flags         uint32 // NCRYPT_MACHINE_KEY_FLAG for machine keys
	provParamN    uint32
	provParams    uintptr
	keySpec       uint32 // 0 for CNG keys
}

// setKeyProvInfo wraps CertSetCertificateContextProperty for the property that
// links a certificate to its private key's provider and key name.
func setKeyProvInfo(ctx *windows.CertContext, info *cryptKeyProvInfo) error {
	r, _, err := procCertSetCertificateContextProperty.Call(uintptr(unsafe.Pointer(ctx)), certKeyProvInfoPropID, 0,
		uintptr(unsafe.Pointer(info)))
	if r == 0 { // this function returns TRUE (non-zero) on success
		return fmt.Errorf("CertSetCertificateContextProperty: %w", err)
	}
	return nil
}

// ImportCert adds a DER certificate to a system store ("My"), linked to the
// named key in the given provider, replacing an identical certificate already
// there. The key must already exist (see CreateKey).
func ImportCert(loc StoreLocation, store string, der []byte, provider, keyName string) error {
	h, err := openSystemStore(loc, store)
	if err != nil {
		return fmt.Errorf("open certificate store %s: %w", store, err)
	}
	defer windows.CertCloseStore(h, 0)
	return importCert(h, der, provider, keyName, loc == LocalMachine)
}

func importCert(store windows.Handle, der []byte, provider, keyName string, machine bool) error {
	if len(der) == 0 {
		return errors.New("empty certificate")
	}
	ctx, err := windows.CertCreateCertificateContext(encoding, &der[0], uint32(len(der)))
	if err != nil {
		return fmt.Errorf("parse certificate: %w", err)
	}
	defer windows.CertFreeCertificateContext(ctx)
	info := cryptKeyProvInfo{containerName: utf16(keyName), provName: utf16(provider)}
	if machine {
		info.flags = ncryptMachineKeyFlag
	}
	if err := setKeyProvInfo(ctx, &info); err != nil {
		return err
	}
	// The property is copied along with the certificate into the store.
	if err := windows.CertAddCertificateContextToStore(store, ctx, windows.CERT_STORE_ADD_REPLACE_EXISTING, nil); err != nil {
		return fmt.Errorf("add certificate to store: %w", err)
	}
	return nil
}

// RemoveCerts deletes every certificate in a system store that match accepts,
// returning how many were removed. Used to clean up after tests and on un-enroll.
func RemoveCerts(loc StoreLocation, store string, match func(*x509.Certificate) bool) (int, error) {
	h, err := openSystemStore(loc, store)
	if err != nil {
		return 0, err
	}
	defer windows.CertCloseStore(h, 0)
	return removeCerts(h, match)
}

func removeCerts(store windows.Handle, match func(*x509.Certificate) bool) (int, error) {
	n := 0
	var prev *windows.CertContext
	for {
		ctx, err := windows.CertEnumCertificatesInStore(store, prev)
		if err != nil || ctx == nil {
			return n, nil
		}
		prev = ctx
		cert, perr := x509.ParseCertificate(certBytes(ctx))
		if perr != nil || !match(cert) {
			continue
		}
		// Deleting frees the context it is given, so delete a duplicate and keep
		// enumerating from the original.
		if err := windows.CertDeleteCertificateFromStore(windows.CertDuplicateCertificateContext(ctx)); err != nil {
			windows.CertFreeCertificateContext(ctx)
			return n, err
		}
		n++
	}
}
