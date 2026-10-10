package winsys

// Windows CNG key storage (ncrypt.dll): private keys that live in the TPM, a smart
// card (YubiKey PIV) or the software key store, used through Go's crypto.Signer
// interface. The private key never leaves its provider; Go only ever sees the
// public key and the signatures the provider makes.

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Key storage providers built into Windows.
const (
	ProviderTPM       = "Microsoft Platform Crypto Provider"        // keys inside the TPM chip
	ProviderSoftware  = "Microsoft Software Key Storage Provider"   // keys in files, protected by DPAPI
	ProviderSmartCard = "Microsoft Smart Card Key Storage Provider" // PIV cards such as a YubiKey
)

// KeyAlgorithm is the kind of key CreateKey makes.
type KeyAlgorithm string

const (
	ECDSAP256 KeyAlgorithm = "ECDSA_P256" // BCRYPT_ECDSA_P256_ALGORITHM
	RSA2048   KeyAlgorithm = "RSA"        // BCRYPT_RSA_ALGORITHM; length set separately
)

var (
	ncrypt                        = windows.NewLazySystemDLL("ncrypt.dll")
	procNCryptOpenStorageProvider = ncrypt.NewProc("NCryptOpenStorageProvider")
	procNCryptOpenKey             = ncrypt.NewProc("NCryptOpenKey")
	procNCryptCreatePersistedKey  = ncrypt.NewProc("NCryptCreatePersistedKey")
	procNCryptSetProperty         = ncrypt.NewProc("NCryptSetProperty")
	procNCryptGetProperty         = ncrypt.NewProc("NCryptGetProperty")
	procNCryptFinalizeKey         = ncrypt.NewProc("NCryptFinalizeKey")
	procNCryptExportKey           = ncrypt.NewProc("NCryptExportKey")
	procNCryptSignHash            = ncrypt.NewProc("NCryptSignHash")
	procNCryptDeleteKey           = ncrypt.NewProc("NCryptDeleteKey")
	procNCryptFreeObject          = ncrypt.NewProc("NCryptFreeObject")
)

// Flags and property names from ncrypt.h / bcrypt.h.
const (
	ncryptMachineKeyFlag   = 0x20
	ncryptSilentFlag       = 0x40
	ncryptOverwriteKeyFlag = 0x80
	bcryptPadPKCS1         = 0x2
	bcryptPadPSS           = 0x8

	propExportPolicy = "Export Policy"  // NCRYPT_EXPORT_POLICY_PROPERTY; 0 = never exportable
	propLength       = "Length"         // NCRYPT_LENGTH_PROPERTY (RSA key size)
	propPIN          = "SmartCardPin"   // NCRYPT_PIN_PROPERTY
	propSecurity     = "Security Descr" // NCRYPT_SECURITY_DESCR_PROPERTY: who may use the key

	blobECCPublic = "ECCPUBLICBLOB" // BCRYPT_ECCPUBLIC_BLOB
	blobRSAPublic = "RSAPUBLICBLOB" // BCRYPT_RSAPUBLIC_BLOB

	magicECDSAP256 = 0x31534345 // "ECS1"
	magicECDSAP384 = 0x33534345 // "ECS3"
	// Smart-card keys (e.g. a YubiKey through Windows' PIV minidriver) report
	// their public key as an ECDH blob even though they sign with ECDSA; the
	// X/Y layout is identical, only the magic differs.
	magicECDHP256 = 0x314B4345 // "ECK1"
	magicECDHP384 = 0x334B4345 // "ECK3"
	magicRSAPublic = 0x31415352 // "RSA1"
)

// ErrKeyNotFound is returned by OpenKey when the provider has no key by that name.
var ErrKeyNotFound = errors.New("key not found")

// ErrKeyAccess means the key exists but this account may not use it. For the
// exmon service that means exmon-helper hasn't granted it access (yet).
var ErrKeyAccess = errors.New("not allowed to use the key")

const (
	nteBadKeyset = 0x80090016 // NTE_BAD_KEYSET: "Keyset does not exist"
	ntePerm      = 0x80090010 // NTE_PERM: "Access denied"
)

// cngError turns an NCrypt status (an HRESULT, 0 = success) into an error.
// windows.Errno's Error method asks Windows for the message text.
func cngError(fn string, status uintptr) error {
	if status == 0 {
		return nil
	}
	switch uint32(status) {
	case nteBadKeyset:
		return fmt.Errorf("%s: %w", fn, ErrKeyNotFound)
	case ntePerm, uint32(windows.ERROR_ACCESS_DENIED):
		return fmt.Errorf("%s: %w", fn, ErrKeyAccess)
	}
	return fmt.Errorf("%s: %w (0x%08X)", fn, windows.Errno(status), uint32(status))
}

func utf16(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s) // only fails on NUL bytes, which our names never have
	return p
}

func machineFlag(machine bool) uintptr {
	if machine {
		return ncryptMachineKeyFlag
	}
	return 0
}

// --- one small typed wrapper per ncrypt.dll function ---

func openStorageProvider(name string) (uintptr, error) {
	var h uintptr
	r, _, _ := procNCryptOpenStorageProvider.Call(uintptr(unsafe.Pointer(&h)), uintptr(unsafe.Pointer(utf16(name))), 0)
	return h, cngError("NCryptOpenStorageProvider", r)
}

func openKey(prov uintptr, name string, flags uintptr) (uintptr, error) {
	var h uintptr
	r, _, _ := procNCryptOpenKey.Call(prov, uintptr(unsafe.Pointer(&h)), uintptr(unsafe.Pointer(utf16(name))), 0, flags)
	return h, cngError("NCryptOpenKey", r)
}

func createPersistedKey(prov uintptr, alg KeyAlgorithm, name string, flags uintptr) (uintptr, error) {
	var h uintptr
	r, _, _ := procNCryptCreatePersistedKey.Call(prov, uintptr(unsafe.Pointer(&h)),
		uintptr(unsafe.Pointer(utf16(string(alg)))), uintptr(unsafe.Pointer(utf16(name))), 0, flags)
	return h, cngError("NCryptCreatePersistedKey", r)
}

func setProperty(obj uintptr, name string, value []byte) error {
	var p *byte
	if len(value) > 0 {
		p = &value[0]
	}
	r, _, _ := procNCryptSetProperty.Call(obj, uintptr(unsafe.Pointer(utf16(name))),
		uintptr(unsafe.Pointer(p)), uintptr(len(value)), 0)
	return cngError("NCryptSetProperty("+name+")", r)
}

func setDWORDProperty(obj uintptr, name string, v uint32) error {
	b := binary.LittleEndian.AppendUint32(nil, v)
	return setProperty(obj, name, b)
}

func finalizeKey(key uintptr) error {
	r, _, _ := procNCryptFinalizeKey.Call(key, 0)
	return cngError("NCryptFinalizeKey", r)
}

// exportKey returns a public key blob. Like most size-returning Windows APIs it is
// called twice: first to learn the size, then to fill a buffer of that size.
func exportKey(key uintptr, blobType string) ([]byte, error) {
	var size uint32
	r, _, _ := procNCryptExportKey.Call(key, 0, uintptr(unsafe.Pointer(utf16(blobType))), 0, 0, 0,
		uintptr(unsafe.Pointer(&size)), 0)
	if err := cngError("NCryptExportKey", r); err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	r, _, _ = procNCryptExportKey.Call(key, 0, uintptr(unsafe.Pointer(utf16(blobType))), 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(size), uintptr(unsafe.Pointer(&size)), 0)
	if err := cngError("NCryptExportKey", r); err != nil {
		return nil, err
	}
	return buf[:size], nil
}

// signHash signs a digest. padding points at a BCRYPT_*_PADDING_INFO (or is nil
// for ECDSA). Two calls again: size, then signature.
func signHash(key uintptr, padding unsafe.Pointer, digest []byte, flags uintptr) ([]byte, error) {
	var size uint32
	r, _, _ := procNCryptSignHash.Call(key, uintptr(padding), uintptr(unsafe.Pointer(&digest[0])), uintptr(len(digest)),
		0, 0, uintptr(unsafe.Pointer(&size)), flags)
	if err := cngError("NCryptSignHash", r); err != nil {
		return nil, err
	}
	sig := make([]byte, size)
	r, _, _ = procNCryptSignHash.Call(key, uintptr(padding), uintptr(unsafe.Pointer(&digest[0])), uintptr(len(digest)),
		uintptr(unsafe.Pointer(&sig[0])), uintptr(size), uintptr(unsafe.Pointer(&size)), flags)
	if err := cngError("NCryptSignHash", r); err != nil {
		return nil, err
	}
	return sig[:size], nil
}

func deleteKey(key uintptr) error {
	r, _, _ := procNCryptDeleteKey.Call(key, 0) // also frees the handle on success
	return cngError("NCryptDeleteKey", r)
}

func freeObject(h uintptr) {
	if h != 0 {
		procNCryptFreeObject.Call(h)
	}
}

// --- crypto.Signer on top ---

// Signer is a private key held by a Windows key storage provider. It implements
// crypto.Signer, so Go's TLS client and x509.CreateCertificate[Request] can use it
// as if it were an ordinary private key.
type Signer struct {
	mu     sync.Mutex // NCrypt handles may not be used from several threads at once
	key    uintptr
	pub    crypto.PublicKey
	free   bool   // close the handle in Close (false when Windows owns it)
	silent bool   // never show Windows UI (PIN prompts); right for a service
	pin    string // smart-card PIN to present before signing ("" = none)
}

// OpenKey opens an existing persisted key by name.
func OpenKey(provider, name string, machine bool) (*Signer, error) {
	prov, err := openStorageProvider(provider)
	if err != nil {
		return nil, err
	}
	defer freeObject(prov)
	key, err := openKey(prov, name, machineFlag(machine)|ncryptSilentFlag)
	if err != nil {
		return nil, err
	}
	return newSigner(key, true)
}

// CreateKey creates a new persisted, non-exportable key. With overwrite false it
// fails if a key with that name exists. RSA keys are 2048 bits.
func CreateKey(provider, name string, alg KeyAlgorithm, machine, overwrite bool) (*Signer, error) {
	prov, err := openStorageProvider(provider)
	if err != nil {
		return nil, err
	}
	defer freeObject(prov)
	flags := machineFlag(machine)
	if overwrite {
		flags |= ncryptOverwriteKeyFlag
	}
	key, err := createPersistedKey(prov, alg, name, flags)
	if err != nil {
		return nil, err
	}
	// Properties must be set between create and finalize.
	if err := setDWORDProperty(key, propExportPolicy, 0); err != nil && provider != ProviderTPM {
		// The TPM provider never exports private keys and may reject the property.
		freeObject(key)
		return nil, err
	}
	if alg == RSA2048 {
		if err := setDWORDProperty(key, propLength, 2048); err != nil {
			freeObject(key)
			return nil, err
		}
	}
	if err := finalizeKey(key); err != nil {
		freeObject(key)
		return nil, err
	}
	return newSigner(key, true)
}

// DeleteKey removes a persisted key. It is not an error if it doesn't exist.
func DeleteKey(provider, name string, machine bool) error {
	prov, err := openStorageProvider(provider)
	if err != nil {
		return err
	}
	defer freeObject(prov)
	key, err := openKey(prov, name, machineFlag(machine)|ncryptSilentFlag)
	if errors.Is(err, ErrKeyNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := deleteKey(key); err != nil {
		freeObject(key)
		return err
	}
	return nil
}

// newSigner wraps a key handle, reading its public key.
func newSigner(key uintptr, free bool) (*Signer, error) {
	pub, err := publicKey(key)
	if err != nil {
		if free {
			freeObject(key)
		}
		return nil, err
	}
	return &Signer{key: key, pub: pub, free: free, silent: true}, nil
}

// SetPIN presents a smart-card PIN before each signature, so no Windows dialog is
// needed. Use it for the YubiKey in an interactive command; services never do.
func (s *Signer) SetPIN(pin string) { s.pin = pin }

// AllowUI lets the provider show Windows UI (e.g. a PIN dialog) when it needs to.
func (s *Signer) AllowUI() { s.silent = false }

// Public returns the public key (*ecdsa.PublicKey or *rsa.PublicKey).
func (s *Signer) Public() crypto.PublicKey { return s.pub }

// Close releases the key handle. The key itself stays in its provider.
func (s *Signer) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.free {
		freeObject(s.key)
	}
	s.key = 0
}

// Delete removes the key from its provider and closes the handle.
func (s *Signer) Delete() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key == 0 {
		return errors.New("key is closed")
	}
	err := deleteKey(s.key)
	if err == nil {
		s.key = 0
	}
	return err
}

// The padding-info structs NCryptSignHash takes for RSA (bcrypt.h).
type pkcs1PaddingInfo struct {
	algID *uint16
}

type pssPaddingInfo struct {
	algID *uint16
	salt  uint32
}

var hashAlgIDs = map[crypto.Hash]string{
	crypto.SHA1: "SHA1", crypto.SHA256: "SHA256", crypto.SHA384: "SHA384", crypto.SHA512: "SHA512",
}

// Sign signs digest (already hashed with opts.HashFunc()). The rand argument is
// ignored: the provider uses its own randomness. For RSA, opts of type
// *rsa.PSSOptions selects PSS padding, otherwise PKCS#1 v1.5.
func (s *Signer) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key == 0 {
		return nil, errors.New("key is closed")
	}
	if len(digest) == 0 {
		return nil, errors.New("empty digest")
	}
	if s.pin != "" {
		pin := windows.StringToUTF16(s.pin) // includes the terminating NUL, as the property needs
		b := unsafe.Slice((*byte)(unsafe.Pointer(&pin[0])), len(pin)*2)
		if err := setProperty(s.key, propPIN, b); err != nil {
			return nil, err
		}
	}
	var flags uintptr
	if s.silent {
		flags |= ncryptSilentFlag
	}

	switch pub := s.pub.(type) {
	case *ecdsa.PublicKey:
		raw, err := signHash(s.key, nil, digest, flags)
		if err != nil {
			return nil, err
		}
		return ecdsaToASN1(raw)

	case *rsa.PublicKey:
		hash := opts.HashFunc()
		id, ok := hashAlgIDs[hash]
		if !ok {
			return nil, fmt.Errorf("unsupported hash %v", hash)
		}
		alg := utf16(id)
		if pss, ok := opts.(*rsa.PSSOptions); ok {
			salt := pss.SaltLength
			// TLS 1.3 requires salt length == hash length; that is also what "auto"
			// means here, because some TPMs can't do longer salts.
			if salt == rsa.PSSSaltLengthEqualsHash || salt == rsa.PSSSaltLengthAuto {
				salt = hash.Size()
			}
			info := pssPaddingInfo{algID: alg, salt: uint32(salt)}
			return signHash(s.key, unsafe.Pointer(&info), digest, flags|bcryptPadPSS)
		}
		info := pkcs1PaddingInfo{algID: alg}
		return signHash(s.key, unsafe.Pointer(&info), digest, flags|bcryptPadPKCS1)

	default:
		return nil, fmt.Errorf("unsupported key type %T", pub)
	}
}

// ecdsaToASN1 converts CNG's r||s signature into the ASN.1 form Go (and TLS) use.
func ecdsaToASN1(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw)%2 != 0 {
		return nil, fmt.Errorf("bad ECDSA signature length %d", len(raw))
	}
	half := len(raw) / 2
	return asn1.Marshal(struct{ R, S *big.Int }{
		new(big.Int).SetBytes(raw[:half]), new(big.Int).SetBytes(raw[half:]),
	})
}

// publicKey exports a key's public half, trying EC first, then RSA.
func publicKey(key uintptr) (crypto.PublicKey, error) {
	if blob, err := exportKey(key, blobECCPublic); err == nil {
		return parseECCPublicBlob(blob)
	}
	blob, err := exportKey(key, blobRSAPublic)
	if err != nil {
		return nil, err
	}
	return parseRSAPublicBlob(blob)
}

// parseECCPublicBlob reads a BCRYPT_ECCKEY_BLOB: {magic, cbKey uint32} then X, Y.
func parseECCPublicBlob(b []byte) (*ecdsa.PublicKey, error) {
	if len(b) < 8 {
		return nil, errors.New("short ECC blob")
	}
	magic, n := binary.LittleEndian.Uint32(b), int(binary.LittleEndian.Uint32(b[4:]))
	var curve elliptic.Curve
	switch magic {
	case magicECDSAP256, magicECDHP256:
		curve = elliptic.P256()
	case magicECDSAP384, magicECDHP384:
		curve = elliptic.P384()
	default:
		return nil, fmt.Errorf("unsupported ECC key (magic 0x%08X)", magic)
	}
	if len(b) < 8+2*n {
		return nil, errors.New("short ECC blob")
	}
	return &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(b[8 : 8+n]), Y: new(big.Int).SetBytes(b[8+n : 8+2*n])}, nil
}

// parseRSAPublicBlob reads a BCRYPT_RSAKEY_BLOB: six uint32s (magic, bit length,
// exponent size, modulus size, two prime sizes), then the exponent and modulus,
// both big-endian.
func parseRSAPublicBlob(b []byte) (*rsa.PublicKey, error) {
	if len(b) < 24 {
		return nil, errors.New("short RSA blob")
	}
	if magic := binary.LittleEndian.Uint32(b); magic != magicRSAPublic {
		return nil, fmt.Errorf("not an RSA public blob (magic 0x%08X)", magic)
	}
	expLen, modLen := int(binary.LittleEndian.Uint32(b[8:])), int(binary.LittleEndian.Uint32(b[12:]))
	if len(b) < 24+expLen+modLen || expLen > 8 {
		return nil, errors.New("short RSA blob")
	}
	e := new(big.Int).SetBytes(b[24 : 24+expLen])
	return &rsa.PublicKey{N: new(big.Int).SetBytes(b[24+expLen : 24+expLen+modLen]), E: int(e.Int64())}, nil
}
