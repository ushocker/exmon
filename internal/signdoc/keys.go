package signdoc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"embed"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
)

// trusted holds the production public keys, built into the exe:
//
//	trust/config/<keyId>.pem    may sign configs and commands
//	trust/release/<keyId>.pem   may sign release manifests
//
// The file name (without .pem) is the key id that envelopes name. Export them
// from Key Vault with `exmonctl keys export`. Adding or rotating a key means a new
// exmon release, which is deliberate: only a signed release can change which keys
// a device trusts.
//
//go:embed trust
var trusted embed.FS

// extraTrust is filled in by trust_dev.go in builds with -tags exmondev. Each
// entry has config/ and release/ folders at its root.
var extraTrust []fs.FS

// KeySetName reports which key sets this build trusts: "prod", or "prod+dev".
func KeySetName() string {
	if len(extraTrust) > 0 {
		return "prod+dev"
	}
	return "prod"
}

// BuiltinKeys returns the keys built into this exe.
func BuiltinKeys() (Keys, error) {
	k := Keys{Config: KeySet{}, Release: KeySet{}}
	prod, err := fs.Sub(trusted, "trust") // view the "trust" folder as the root
	if err != nil {
		return k, err
	}
	for _, fsys := range append([]fs.FS{prod}, extraTrust...) {
		if err := loadDir(fsys, "config", k.Config); err != nil {
			return k, err
		}
		if err := loadDir(fsys, "release", k.Release); err != nil {
			return k, err
		}
	}
	return k, nil
}

func loadDir(fsys fs.FS, dir string, into KeySet) error {
	entries, err := fs.ReadDir(fsys, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pem") {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		pub, err := ParsePublicKey(b)
		if err != nil {
			return fmt.Errorf("%s/%s: %w", dir, e.Name(), err)
		}
		into[strings.TrimSuffix(e.Name(), ".pem")] = pub
	}
	return nil
}

// ParsePublicKey reads a PEM "PUBLIC KEY" holding an ECDSA P-256 key.
func ParsePublicKey(b []byte) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, errors.New("not a PEM PUBLIC KEY")
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := k.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, errors.New("must be an ECDSA P-256 key")
	}
	return pub, nil
}

// PublicKeyPEM encodes a public key as PEM.
func PublicKeyPEM(pub *ecdsa.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// FileSigner signs with a local ECDSA key. It is for development and tests only;
// production keys live in Key Vault.
type FileSigner struct {
	ID  string
	Key *ecdsa.PrivateKey
}

func (f FileSigner) KeyID() string { return f.ID }

func (f FileSigner) SignDigest(digest []byte) ([]byte, error) {
	r, s, err := ecdsa.Sign(rand.Reader, f.Key, digest)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 64)
	r.FillBytes(out[:32]) // FillBytes left-pads with zeros to the full 32 bytes
	s.FillBytes(out[32:])
	return out, nil
}

// LoadFileSigner reads a PEM "EC PRIVATE KEY" or "PRIVATE KEY" file; the key id
// is the file name without its extension.
func LoadFileSigner(p string) (FileSigner, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return FileSigner{}, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return FileSigner{}, errors.New("not a PEM file")
	}
	var key *ecdsa.PrivateKey
	switch block.Type {
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	case "PRIVATE KEY":
		var k any
		k, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		key, _ = k.(*ecdsa.PrivateKey)
	}
	if err != nil || key == nil || key.Curve != elliptic.P256() {
		return FileSigner{}, errors.New("must be an ECDSA P-256 private key")
	}
	id := strings.TrimSuffix(path.Base(strings.ReplaceAll(p, `\`, "/")), path.Ext(p))
	return FileSigner{ID: id, Key: key}, nil
}

// NewDevKey makes a new P-256 key pair for development, returning the private
// key PEM (keep it out of the repository) and the public key PEM.
func NewDevKey() (private, public []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	pub, err := PublicKeyPEM(&key.PublicKey)
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), pub, err
}
