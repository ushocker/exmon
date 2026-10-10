// Package signdoc signs and verifies the documents the control plane sends to
// devices: cloud configs, release manifests and commands.
//
// A signed document is a small JSON envelope:
//
//	{"alg":"ES256","keyId":"cfg-2026a","payload":"<base64url>","signature":"<base64url r||s>"}
//
// The payload is carried as base64 of its exact bytes, so there is no question of
// how JSON was formatted: the signature covers exactly those bytes. Signatures are
// ECDSA P-256 with SHA-256 ("ES256"), made by a key in Azure Key Vault (or a dev
// key file); the matching public keys are built into exmon (see keys.go).
//
// Verify checks, in this order, before the caller looks at the payload:
//  1. size limit;
//  2. the envelope decodes and names a known key for this kind of document;
//  3. the signature (nothing in the payload is parsed before this);
//  4. kind, expiry, issue time and target device.
//
// The caller then checks the version is newer than the last one it applied, and
// decodes the payload strictly into its own type.
package signdoc

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"
)

// Document kinds.
const (
	KindConfig  = "exmon-config"
	KindRelease = "exmon-release"
	KindCommand = "exmon-command"
)

const (
	alg = "ES256"
	// MaxSize is the largest envelope accepted. Event Grid's retained messages are
	// at most 64 KB, so nothing legitimate is bigger.
	MaxSize = 64 << 10
	// clockSkew tolerates small differences between the signer's and device's clocks.
	clockSkew = 5 * time.Minute
)

// Envelope is the signed wrapper.
type Envelope struct {
	Alg       string `json:"alg"`
	KeyID     string `json:"keyId"`
	Payload   string `json:"payload"`   // base64url (no padding) of the payload bytes
	Signature string `json:"signature"` // base64url of r||s, 32 bytes each
}

// Header holds the fields every payload has. Payload types embed it.
type Header struct {
	Kind      string    `json:"kind"`
	Version   int64     `json:"version"` // only ever goes up per kind; exmonctl uses a timestamp like 20261007153000
	IssuedAt  time.Time `json:"issuedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Target    Target    `json:"target"`
}

// Target limits a document to some devices. Empty = any device that receives it.
type Target struct {
	Devices []string `json:"devices,omitempty"`
}

// KeySet holds trusted public keys by key id.
type KeySet map[string]*ecdsa.PublicKey

// Keys is the set of trusted keys for each purpose. Configs and commands are
// checked against Config; releases against Release, so a key that signs configs
// can never approve new code.
type Keys struct {
	Config  KeySet
	Release KeySet
}

func (k Keys) forKind(kind string) (KeySet, error) {
	switch kind {
	case KindConfig, KindCommand:
		return k.Config, nil
	case KindRelease:
		return k.Release, nil
	}
	return nil, fmt.Errorf("unknown document kind %q", kind)
}

// ErrNotForUs means the document is valid but targets other devices.
var ErrNotForUs = errors.New("document targets other devices")

// Verify checks a signed document (see the package comment for the order) and
// returns its payload bytes and header. device is this device's authentication
// name.
func Verify(raw []byte, kind string, keys Keys, device string, now time.Time) ([]byte, Header, error) {
	payload, h, err := verifySigned(raw, kind, keys)
	if err != nil {
		return nil, h, err
	}
	switch {
	case h.ExpiresAt.IsZero() || now.After(h.ExpiresAt.Add(clockSkew)):
		return nil, h, fmt.Errorf("document expired at %s", h.ExpiresAt.Format(time.RFC3339))
	case h.IssuedAt.After(now.Add(clockSkew)):
		return nil, h, fmt.Errorf("document issued in the future (%s); is this PC's clock right?", h.IssuedAt.Format(time.RFC3339))
	case len(h.Target.Devices) > 0 && !slices.Contains(h.Target.Devices, device):
		return nil, h, ErrNotForUs
	}
	return payload, h, nil
}

// VerifySignature checks only the envelope, signature, kind and version, not
// expiry or target. It is for a document this device already accepted and
// stored (the active cloud config), which keeps working after its delivery
// window ends.
func VerifySignature(raw []byte, kind string, keys Keys) ([]byte, error) {
	payload, _, err := verifySigned(raw, kind, keys)
	return payload, err
}

// verifySigned does steps 1-3 of the package comment, then reads the header.
func verifySigned(raw []byte, kind string, keys Keys) ([]byte, Header, error) {
	var h Header
	if len(raw) > MaxSize {
		return nil, h, fmt.Errorf("document is %d bytes, over the %d limit", len(raw), MaxSize)
	}
	var env Envelope
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return nil, h, fmt.Errorf("envelope: %w", err)
	}
	if env.Alg != alg {
		return nil, h, fmt.Errorf("algorithm %q not accepted", env.Alg)
	}
	set, err := keys.forKind(kind)
	if err != nil {
		return nil, h, err
	}
	pub, ok := set[env.KeyID]
	if !ok {
		return nil, h, fmt.Errorf("key %q is not trusted for %s documents", env.KeyID, kind)
	}
	payload, err := base64.RawURLEncoding.DecodeString(env.Payload)
	if err != nil {
		return nil, h, fmt.Errorf("payload encoding: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(env.Signature)
	if err != nil || len(sig) != 64 {
		return nil, h, errors.New("signature must be 64 bytes (r||s) in base64url")
	}
	digest := sha256.Sum256(payload)
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		return nil, h, errors.New("bad signature")
	}

	// Only now is the (authentic) payload parsed, for its header fields.
	if err := json.Unmarshal(payload, &h); err != nil {
		return nil, h, fmt.Errorf("payload: %w", err)
	}
	switch {
	case h.Kind != kind:
		return nil, h, fmt.Errorf("document is %q, expected %q", h.Kind, kind)
	case h.Version <= 0:
		return nil, h, errors.New("document has no version")
	}
	return payload, h, nil
}

// DecodeStrict decodes an authenticated payload into v, rejecting unknown keys.
func DecodeStrict(payload []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// Signer makes ES256 signatures: SignDigest returns r||s for a SHA-256 digest.
// A Key Vault key (exmonctl) and a local key file (dev) both implement it.
type Signer interface {
	KeyID() string
	SignDigest(digest []byte) ([]byte, error)
}

// Seal signs payload and returns the envelope JSON.
func Seal(payload []byte, s Signer) ([]byte, error) {
	digest := sha256.Sum256(payload)
	sig, err := s.SignDigest(digest[:])
	if err != nil {
		return nil, err
	}
	if len(sig) != 64 {
		return nil, fmt.Errorf("signer returned %d bytes, want 64 (r||s)", len(sig))
	}
	return json.Marshal(Envelope{
		Alg:       alg,
		KeyID:     s.KeyID(),
		Payload:   base64.RawURLEncoding.EncodeToString(payload),
		Signature: base64.RawURLEncoding.EncodeToString(sig),
	})
}
