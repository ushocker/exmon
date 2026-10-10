package config

// The cloud config layer: settings delivered over the control plane as a signed
// document. Load order is defaults < local exmon.json < cloud config < flags.
//
// A cloud config can't change where exmon writes files, where it connects for
// control, or which certificate it uses; those stay with the local config and the
// install flags. cloudAllowed lists what it may set, and stricter ranges apply
// (validateCloud), so a bad cloud config can't turn a fleet into a traffic source.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"exmon/internal/limits"
	"exmon/internal/remote"
	"exmon/internal/remote/senders"
	"exmon/internal/signdoc"
)

// CloudPayload is the body of a signed exmon-config document.
type CloudPayload struct {
	signdoc.Header
	Config json.RawMessage `json:"config"` // the cloud-settable part of exmon.json
}

// cloudAllowed lists the top-level sections a cloud config may contain, and for
// some of them which keys. nil = every key of that section (paths are refused
// separately below).
var cloudAllowed = map[string][]string{
	"wifi":     nil,
	"nic":      nil,
	"vpn":      nil,
	"services": nil,
	"probe":    nil,
	"power":    nil,
	"incident": {"enabled", "windowMinutes", "postTriggerSeconds", "cooldownSeconds", "maxIncidents", "maxIncidentsBySeverity", "maxHoldSeconds", "triggers"},
	"baseline": {"enabled", "summaryMinutes", "maxSizeMB", "maxFiles"},
	"control":  {"chirpMinutes", "statusMinutes"},
	"remotes":  nil, // checked per remote below
}

// remoteCloudForbidden are remote keys a cloud config can't set.
var remoteCloudForbidden = []string{"spoolDir", "simulateOffline"}

// checkCloudKeys rejects anything a cloud config isn't allowed to set.
func checkCloudKeys(raw json.RawMessage) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return fmt.Errorf("cloud config: %w", err)
	}
	for section, body := range top {
		keys, ok := cloudAllowed[section]
		if !ok {
			return fmt.Errorf("cloud config can't set %q (only local config and install flags can)", section)
		}
		if section == "remotes" {
			var list []map[string]json.RawMessage
			if err := json.Unmarshal(body, &list); err != nil {
				return fmt.Errorf("cloud config remotes: %w", err)
			}
			for _, r := range list {
				for _, k := range remoteCloudForbidden {
					if _, bad := r[k]; bad {
						return fmt.Errorf("cloud config can't set a remote's %q", k)
					}
				}
			}
			continue
		}
		if keys == nil {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			return fmt.Errorf("cloud config %s: %w", section, err)
		}
		for k := range fields {
			if !contains(keys, k) {
				return fmt.Errorf("cloud config can't set %s.%s", section, k)
			}
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// applyCloud overlays a cloud config onto opts, strictly: an unknown key is an
// error, as is anything checkCloudKeys refuses.
func applyCloud(raw json.RawMessage, opts *Options) error {
	if err := checkCloudKeys(raw); err != nil {
		return err
	}
	var top map[string]json.RawMessage
	json.Unmarshal(raw, &top)
	if _, ok := top["remotes"]; ok {
		opts.Remotes = nil // a remotes list replaces the local one, as in a local file
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(opts); err != nil {
		return fmt.Errorf("cloud config: %w", err)
	}
	return nil
}

// validateCloud applies the stricter limits for settings that came from the
// cloud. Together with the counts every config has (at most 25 endpoints, 25 HTTP
// tests, 10 DNS names), a probe interval of at least 60 seconds caps a device at
// roughly 185 probe actions a minute.
func validateCloud(o Options) error {
	errs := []error{
		limits.Range("probe.intervalSeconds (cloud)", o.Probe.IntervalSeconds, 60, 86400),
		limits.Range("wifi.pollSeconds (cloud)", o.WiFi.PollSeconds, 2, 3600),
		limits.Range("nic.pollSeconds (cloud)", o.NIC.PollSeconds, 10, 3600),
		limits.Range("vpn.pollSeconds (cloud)", o.VPN.PollSeconds, 10, 3600),
		limits.Range("services.pollSeconds (cloud)", o.Services.PollSeconds, 10, 3600),
	}
	if m := o.Probe.PathTrace.IntervalMinutes; m != 0 {
		errs = append(errs, limits.Range("probe.pathTrace.intervalMinutes (cloud)", m, 10, 1440))
	}
	for _, r := range o.Remotes {
		if t, ok := remote.Lookup(r.Type); ok && !t.CloudAllowed {
			errs = append(errs, fmt.Errorf("remotes[%s]: type %q can't be set by a cloud config (it writes local files)", r.Name, r.Type))
		}
	}
	return limits.First(errs...)
}

// ValidateCloudConfig checks a cloud config's "config" object the way a device
// will: only allowed keys, strict decoding, the stricter cloud ranges, and the
// normal validation of every section it sets. exmonctl runs it before signing.
func ValidateCloudConfig(raw json.RawMessage) error {
	senders.RegisterAll()
	if err := checkCloudLayer(raw); err != nil {
		return err
	}
	o := Defaults()
	o.Remotes = nil
	if err := applyCloud(raw, &o); err != nil {
		return err
	}
	for i := range o.Remotes {
		o.Remotes[i].ApplyDefaults()
		if p, err := remote.ExpandPath(o.Remotes[i].SpoolDir); err == nil {
			o.Remotes[i].SpoolDir = p
		}
	}
	return o.Validate()
}

// checkCloudLayer applies the stricter cloud limits to what the cloud config
// itself sets: it is laid over the built-in defaults (not the local config, whose
// own values may legitimately be outside the cloud limits) and validated.
func checkCloudLayer(raw json.RawMessage) error {
	only := Defaults()
	only.Remotes = nil
	if err := applyCloud(raw, &only); err != nil {
		return err
	}
	for i := range only.Remotes {
		only.Remotes[i].ApplyDefaults()
	}
	return validateCloud(only)
}

// CloudDir is where the service keeps the cloud config (admin-only).
func CloudDir() string { return cloudDir() }

// cloudDir is a variable so tests can point it at a temporary folder.
var cloudDir = func() string { return expand(`%ProgramData%\exmon\config\cloud`) }

func activePath() string { return filepath.Join(CloudDir(), "active.json") }
func lkgPath() string    { return filepath.Join(CloudDir(), "last-known-good.json") }

// readCloud reads and verifies the active cloud config. The signature is checked
// again on every load, so a file someone changed on disk is refused; expiry and
// target were checked when it was received, and an applied config keeps working
// after its delivery window ends.
func readCloud(keys signdoc.Keys) (*CloudPayload, error) {
	raw, err := os.ReadFile(activePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return verifyCloud(raw, keys)
}

func verifyCloud(raw []byte, keys signdoc.Keys) (*CloudPayload, error) {
	payload, err := signdoc.VerifySignature(raw, signdoc.KindConfig, keys)
	if err != nil {
		return nil, fmt.Errorf("cloud config %s: %w", activePath(), err)
	}
	var p CloudPayload
	if err := signdoc.DecodeStrict(payload, &p); err != nil {
		return nil, fmt.Errorf("cloud config: %w", err)
	}
	return &p, nil
}

// cloudKeys returns the built-in keys; a variable so tests can use their own.
var cloudKeys = signdoc.BuiltinKeys

// TestCloud checks that a received cloud config would load: it is merged with
// the local config and flags exactly as at startup, and validated. Nothing is
// written.
func TestCloud(args []string, trust TrustFunc, envelope []byte) (Options, error) {
	keys, err := cloudKeys()
	if err != nil {
		return Options{}, err
	}
	p, err := verifyCloud(envelope, keys)
	if err != nil {
		return Options{}, err
	}
	return loadWith(args, trust, p)
}

// InstallCloud makes envelope the active cloud config, keeping the previous one
// as last-known-good. The write is atomic (temporary file, then rename).
func InstallCloud(envelope []byte) error {
	if err := os.MkdirAll(CloudDir(), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(activePath()); err == nil {
		if err := copyFile(activePath(), lkgPath()); err != nil {
			return fmt.Errorf("keep the previous cloud config: %w", err)
		}
	} else {
		os.Remove(lkgPath()) // nothing before this one: reverting means "no cloud config"
	}
	return writeAtomic(activePath(), envelope)
}

// RevertCloud puts the last-known-good cloud config back (or removes the cloud
// config if there was none before).
func RevertCloud() error {
	if _, err := os.Stat(lkgPath()); err == nil {
		return os.Rename(lkgPath(), activePath())
	}
	err := os.Remove(activePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeAtomic(dst, b)
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Pending records a change that is being tried: a new cloud config (or, in
// internal/updater, a new release). If exmon doesn't run healthily with it by
// Deadline, or fails to start MaxStarts times, the change is reverted.
type Pending struct {
	Kind     string    `json:"kind"` // "config" or "release"
	Version  int64     `json:"version"`
	Deadline time.Time `json:"deadline"`
	Starts   int       `json:"starts"` // how many times exmon has started with it
}

// MaxStarts is how many starts a pending change gets before it is reverted.
const MaxStarts = 3

// PendingPath is where Pending is kept.
func PendingPath() string { return expand(`%ProgramData%\exmon\state\pending-config.json`) }

// ReadPending returns the pending change, or nil.
func ReadPending(path string) (*Pending, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p Pending
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// WritePending saves p (or removes the file for nil).
func WritePending(path string, p *Pending) error {
	if p == nil {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	return writeAtomic(path, b)
}

// describeCloud is the startup line for the cloud layer.
func describeCloud(p *CloudPayload) string {
	if p == nil {
		return ""
	}
	var top map[string]json.RawMessage
	json.Unmarshal(p.Config, &top)
	sections := make([]string, 0, len(top))
	for k := range top {
		sections = append(sections, k)
	}
	return fmt.Sprintf("version %d (%s)", p.Version, strings.Join(sortStrings(sections), ", "))
}

func sortStrings(s []string) []string {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	return s
}
