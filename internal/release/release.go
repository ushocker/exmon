// Package release holds the release manifest (the payload of a signed
// exmon-release document) and version arithmetic. Both the updater inside exmon
// and the privileged exmon-helper use it, so the helper can read a manifest
// without pulling in any of exmon's networking code.
package release

import (
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"strconv"
	"strings"
	"time"

	"exmon/internal/signdoc"
)

// Manifest is a signed exmon-release document's payload.
type Manifest struct {
	signdoc.Header
	ReleaseVersion string `json:"releaseVersion"`           // the exmon version on offer, e.g. "1.5.0"
	MinVersion     string `json:"minVersion,omitempty"`     // devices older than this must update in steps first
	URL            string `json:"url"`                      // https download of exmon.exe
	SHA256         string `json:"sha256"`                   // hex
	Size           int64  `json:"size"`                     // bytes
	Percent        int    `json:"percent,omitempty"`        // 1-100: share of the targeted devices that take it (0 = 100)
	SpreadMinutes  int    `json:"spreadMinutes,omitempty"`  // spread downloads over this long after issuedAt
	AllowDowngrade bool   `json:"allowDowngrade,omitempty"` // a rollback to an older version
}

// CheckShape checks the fields every consumer relies on (not the device's own
// policy, such as allowed download hosts).
func (m Manifest) CheckShape(maxBytes int64) error {
	if _, err := ParseVersion(m.ReleaseVersion); err != nil {
		return err
	}
	if m.MinVersion != "" {
		if _, err := ParseVersion(m.MinVersion); err != nil {
			return fmt.Errorf("minVersion: %w", err)
		}
	}
	u, err := url.Parse(m.URL)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return fmt.Errorf("url %q must be https", m.URL)
	}
	if b, err := hex.DecodeString(m.SHA256); err != nil || len(b) != 32 {
		return errors.New("sha256 must be 64 hex digits")
	}
	if m.Size <= 0 || m.Size > maxBytes {
		return fmt.Errorf("size %d is outside 1 to %d bytes", m.Size, maxBytes)
	}
	if m.Percent < 0 || m.Percent > 100 {
		return fmt.Errorf("percent %d must be 0-100", m.Percent)
	}
	if m.SpreadMinutes < 0 || m.SpreadMinutes > 1440 {
		return fmt.Errorf("spreadMinutes %d must be 0-1440", m.SpreadMinutes)
	}
	return nil
}

// Host is the download URL's host name, lower case.
func (m Manifest) Host() string {
	u, err := url.Parse(m.URL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// Version is a MAJOR.MINOR.PATCH number.
type Version [3]int

// ParseVersion reads "1.4.0" (or "v1.4.0"). A development build ("dev") counts
// as 0.0.0.
func ParseVersion(s string) (Version, error) {
	var v Version
	if s == "dev" {
		return v, nil
	}
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("version %q must look like 1.4.0", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, fmt.Errorf("version %q must look like 1.4.0", s)
		}
		v[i] = n
	}
	return v, nil
}

// Compare returns -1, 0 or 1 as a is older than, equal to, or newer than b.
func Compare(a, b string) int {
	va, _ := ParseVersion(a)
	vb, _ := ParseVersion(b)
	for i := range va {
		switch {
		case va[i] < vb[i]:
			return -1
		case va[i] > vb[i]:
			return 1
		}
	}
	return 0
}

// Slot is a stable number 0-99 for this device and release, so the same devices
// stay chosen as a rollout's percentage goes up.
func Slot(deviceID, version string) int {
	h := fnv.New32a()
	h.Write([]byte(deviceID + "|" + version))
	return int(h.Sum32() % 100)
}

// Selected reports whether this device takes part in the rollout.
func Selected(deviceID string, m Manifest) bool {
	p := m.Percent
	if p == 0 {
		p = 100
	}
	return Slot(deviceID, m.ReleaseVersion) < p
}

// StartTime is when this device should start downloading: a stable point within
// spreadMinutes after the manifest was issued.
func StartTime(deviceID string, m Manifest) time.Time {
	if m.SpreadMinutes == 0 {
		return m.IssuedAt
	}
	h := fnv.New32a()
	h.Write([]byte("spread|" + deviceID + "|" + m.ReleaseVersion))
	offset := time.Duration(h.Sum32()%uint32(m.SpreadMinutes*60)) * time.Second
	return m.IssuedAt.Add(offset)
}
