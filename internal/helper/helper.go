// Package helper is exmon-helper: a small service that runs as LocalSystem and
// does the only two things exmon (LocalService) isn't allowed to:
//
//  1. grant NT SERVICE\exmon read access to the device certificate's private key
//     (the TPM key Intune or `exmon enroll` created), again after every renewal;
//  2. apply an update exmon prepared: verify it again independently, swap
//     exmon.exe, restart exmon, wait for it to report healthy, roll back if not.
//
// The trust rule: the helper trusts nothing exmon wrote. A compromised exmon
// could write anything into update\incoming, so the helper checks the signed
// manifest with its own built-in keys, its own version records and its own
// install-time settings, and checks the exe on a copy exmon can't touch.
//
// It parses no config file and talks to no network.
package helper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"exmon/internal/release"
	"exmon/internal/signdoc"
	"exmon/internal/winsys"
)

// Config holds the helper's install-time settings, from its own service command
// line (which only an administrator can change).
type Config struct {
	DataDir             string // %ProgramData%\exmon
	InstallDir          string // C:\Program Files\exmon
	Service             string // the service to update: "exmon"
	ServiceSID          string // NT SERVICE\exmon's SID, granted key access
	CertSubject         string // which certificate's key to grant (same rule as exmon's control client)
	CertIssuer          string
	PublisherCN         string // the code-signing certificate new exes must carry
	RequireAuthenticode bool
	HealthMinutes       int // how long the new version has to report healthy
	MaxSizeMB           int // largest exe accepted
}

// Deps are replaceable for tests.
type Deps struct {
	Keys             signdoc.Keys
	Now              func() time.Time
	Authentic        func(path string) (*x509.Certificate, error)
	InstalledVersion func(exe string) (string, error)
	Services         ServiceControl
	Log              func(warning bool, msg string)
	Poll             time.Duration // how often the health watch looks (default 5s)
}

// ServiceControl stops and starts a service, waiting for it to get there.
type ServiceControl interface {
	Stop(name string) error
	Start(name string) error
}

// fileRE is the only shape of staged file name accepted.
var fileRE = regexp.MustCompile(`^exmon-[0-9]+\.[0-9]+\.[0-9]+\.exe$`)

// helperState is what the helper remembers (admin-only folder).
type helperState struct {
	LastDocVersion int64 `json:"lastDocVersion"`
}

// Run does one pass: grant key access, then finish a trial in progress or apply
// a waiting update request. It returns when there is nothing (more) to do.
func Run(ctx context.Context, cfg Config, d Deps) error {
	if d.Poll == 0 {
		d.Poll = 5 * time.Second
	}
	if n, err := GrantKeys(cfg); err != nil {
		d.Log(true, "key access: "+err.Error())
	} else if n > 0 {
		d.Log(false, fmt.Sprintf("granted NT SERVICE\\%s use of the client certificate's key", cfg.Service))
	}

	var t release.Trial
	if ok, err := release.ReadJSON(release.TrialPath(cfg.DataDir), release.MaxRequestBytes, &t); err == nil && ok && t.Outcome == "" {
		return watch(ctx, cfg, d, &t) // a trial was in progress (the helper was stopped, or the PC restarted)
	}
	if _, err := os.Stat(release.RequestPath(cfg.DataDir)); err != nil {
		return nil // nothing to do
	}
	t2, err := apply(cfg, d)
	clearIncoming(cfg) // processed, whatever the outcome: never retried as-is
	if err != nil {
		d.Log(true, "update refused: "+err.Error())
		return nil
	}
	if t2 == nil {
		return nil
	}
	return watch(ctx, cfg, d, t2)
}

// GrantKeys lets the exmon service use the matching client certificate's key.
// It returns how many keys it changed (0 when already granted or no certificate).
func GrantKeys(cfg Config) (int, error) {
	if cfg.CertSubject == "" && cfg.CertIssuer == "" {
		return 0, nil // control plane not configured
	}
	cc, err := winsys.FindClientCert(winsys.LocalMachine, "My", winsys.ClientCertMatch(cfg.CertSubject, cfg.CertIssuer), false)
	if err != nil {
		return 0, nil // no certificate yet (Intune hasn't issued it): nothing to grant
	}
	defer cc.Close()
	changed, err := cc.Signer.GrantKeyRead(cfg.ServiceSID)
	if err != nil {
		return 0, fmt.Errorf("certificate %q: %w", cc.Leaf.Subject.CommonName, err)
	}
	if changed {
		return 1, nil
	}
	return 0, nil
}

// apply verifies the request and swaps the exe. It returns the started trial,
// or nil when there was nothing to install (already on that version).
func apply(cfg Config, d Deps) (*release.Trial, error) {
	req, err := readRequest(cfg)
	if err != nil {
		return nil, err
	}
	// 1. The manifest: signature with the helper's own release keys, kind, version.
	payload, err := signdoc.VerifySignature(req.Envelope, signdoc.KindRelease, d.Keys)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	var m release.Manifest
	if err := signdoc.DecodeStrict(payload, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.ExpiresAt.IsZero() || d.Now().After(m.ExpiresAt.Add(5*time.Minute)) {
		return nil, fmt.Errorf("manifest expired at %s", m.ExpiresAt.Format(time.RFC3339))
	}
	if err := m.CheckShape(int64(cfg.MaxSizeMB) << 20); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	// 2. Replay: only newer manifests than the last one this helper applied.
	var st helperState
	if _, err := release.ReadJSON(release.HelperStatePath(cfg.DataDir), 4096, &st); err != nil {
		return nil, err
	}
	if m.Version <= st.LastDocVersion {
		return nil, fmt.Errorf("manifest version %d is not newer than the last applied (%d)", m.Version, st.LastDocVersion)
	}
	// 3. Versions: the installed exe says what it is (Program Files is admin-only).
	exe := filepath.Join(cfg.InstallDir, "exmon.exe")
	installed, err := d.InstalledVersion(exe)
	if err != nil {
		return nil, fmt.Errorf("installed version: %w", err)
	}
	switch c := release.Compare(m.ReleaseVersion, installed); {
	case c == 0:
		return nil, nil
	case c < 0 && !m.AllowDowngrade:
		return nil, fmt.Errorf("%s is older than the installed %s, and the manifest doesn't allow a downgrade", m.ReleaseVersion, installed)
	}
	// 4. The exe: copied first into a folder only SYSTEM and Administrators can
	// write, then checked there, so exmon can't change it after the check.
	verified, err := copyVerified(cfg, req.File, m)
	if err != nil {
		return nil, err
	}
	if cfg.RequireAuthenticode {
		cert, err := d.Authentic(verified)
		if err != nil {
			os.Remove(verified)
			return nil, fmt.Errorf("code signature: %w", err)
		}
		if cert.Subject.CommonName != cfg.PublisherCN {
			os.Remove(verified)
			return nil, fmt.Errorf("signed by %q, not %q", cert.Subject.CommonName, cfg.PublisherCN)
		}
	}
	// 5. Swap and restart.
	t := &release.Trial{ReleaseVersion: m.ReleaseVersion, PreviousVersion: installed, DocVersion: m.Version,
		Deadline: d.Now().Add(time.Duration(cfg.HealthMinutes) * time.Minute)}
	if err := swap(cfg, d, exe, verified, t); err != nil {
		return nil, err
	}
	if err := release.WriteJSON(release.HelperStatePath(cfg.DataDir), helperState{LastDocVersion: m.Version}); err != nil {
		d.Log(true, "record the applied version: "+err.Error())
	}
	d.Log(false, fmt.Sprintf("updated %s %s -> %s; waiting up to %dm for it to report healthy", cfg.Service, installed, m.ReleaseVersion, cfg.HealthMinutes))
	return t, nil
}

func readRequest(cfg Config) (release.Request, error) {
	var req release.Request
	f, err := os.Open(release.RequestPath(cfg.DataDir))
	if err != nil {
		return req, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, release.MaxRequestBytes+1))
	if err != nil {
		return req, err
	}
	if len(b) > release.MaxRequestBytes {
		return req, errors.New("request.json is too large")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, fmt.Errorf("request.json: %w", err)
	}
	if !fileRE.MatchString(req.File) {
		return req, fmt.Errorf("request.json: file %q is not an exmon-<version>.exe name", req.File)
	}
	return req, nil
}

// copyVerified copies the staged file into update\verified, checking size and
// SHA-256 on the way.
func copyVerified(cfg Config, name string, m release.Manifest) (string, error) {
	src, err := os.Open(filepath.Join(release.IncomingDir(cfg.DataDir), name))
	if err != nil {
		return "", err
	}
	defer src.Close()
	if err := os.MkdirAll(release.VerifiedDir(cfg.DataDir), 0o755); err != nil {
		return "", err
	}
	dstPath := filepath.Join(release.VerifiedDir(cfg.DataDir), "exmon-"+m.ReleaseVersion+".exe")
	dst, err := os.Create(dstPath)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(src, m.Size+1))
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != m.Size {
		err = fmt.Errorf("staged file is %d bytes, the manifest says %d", n, m.Size)
	}
	if err == nil && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), m.SHA256) {
		err = errors.New("staged file's sha256 doesn't match the manifest")
	}
	if err != nil {
		os.Remove(dstPath)
		return "", err
	}
	return dstPath, nil
}

// swap stops the service, puts the verified exe in place (keeping the old one as
// exmon.old.exe), records the trial and starts the service again.
func swap(cfg Config, d Deps, exe, verified string, t *release.Trial) error {
	if err := d.Services.Stop(cfg.Service); err != nil {
		return fmt.Errorf("stop %s: %w", cfg.Service, err)
	}
	old := filepath.Join(cfg.InstallDir, "exmon.old.exe")
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		d.Services.Start(cfg.Service)
		return fmt.Errorf("move the old exe aside: %w", err)
	}
	if err := moveFile(verified, exe); err != nil {
		os.Rename(old, exe)
		d.Services.Start(cfg.Service)
		return fmt.Errorf("put the new exe in place: %w", err)
	}
	// The rename kept update\verified's admin-only permissions, which the service
	// account can't read (Windows then refuses to start it: "Access is denied").
	// Give the exe Program Files' permissions instead.
	if err := winsys.InheritDACL(exe); err != nil {
		os.Remove(exe)
		os.Rename(old, exe)
		d.Services.Start(cfg.Service)
		return fmt.Errorf("set the new exe's permissions: %w", err)
	}
	if err := release.WriteJSON(release.TrialPath(cfg.DataDir), t); err != nil {
		os.Remove(exe)
		os.Rename(old, exe)
		d.Services.Start(cfg.Service)
		return fmt.Errorf("record the trial: %w", err)
	}
	if err := d.Services.Start(cfg.Service); err != nil {
		d.Log(true, fmt.Sprintf("start %s %s: %v (the health watch will roll back if it doesn't come up)", cfg.Service, t.ReleaseVersion, err))
	}
	return nil
}

// watch waits for the new version's health report, or rolls back at the deadline.
func watch(ctx context.Context, cfg Config, d Deps, t *release.Trial) error {
	for {
		var h release.Health
		release.ReadJSON(release.HealthPath(cfg.DataDir), release.MaxRequestBytes, &h)
		if h.Healthy && h.DocVersion == t.DocVersion && h.Version == t.ReleaseVersion {
			t.Outcome = "healthy"
			d.Log(false, fmt.Sprintf("%s %s is healthy; update complete", cfg.Service, t.ReleaseVersion))
			return release.WriteJSON(release.TrialPath(cfg.DataDir), t)
		}
		if d.Now().After(t.Deadline) {
			return rollback(cfg, d, t, "no healthy report by "+t.Deadline.Format(time.RFC3339))
		}
		select {
		case <-ctx.Done():
			return nil // stopped: the next run picks the trial up again
		case <-time.After(d.Poll):
		}
	}
}

// rollback puts exmon.old.exe back and starts the service.
func rollback(cfg Config, d Deps, t *release.Trial, why string) error {
	exe := filepath.Join(cfg.InstallDir, "exmon.exe")
	old := filepath.Join(cfg.InstallDir, "exmon.old.exe")
	failed := filepath.Join(cfg.InstallDir, "exmon.failed.exe")
	d.Services.Stop(cfg.Service)
	os.Remove(failed)
	if err := os.Rename(exe, failed); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("rollback: move the new exe aside: %w", err)
	}
	if err := os.Rename(old, exe); err != nil {
		return fmt.Errorf("rollback: put the old exe back: %w", err)
	}
	t.Outcome, t.Detail = "rolled_back", why
	release.WriteJSON(release.TrialPath(cfg.DataDir), t) // the old version logs UPDATE/rolled_back from this
	d.Log(true, fmt.Sprintf("rolled %s back from %s to %s: %s", cfg.Service, t.ReleaseVersion, t.PreviousVersion, why))
	return d.Services.Start(cfg.Service)
}

// clearIncoming removes the request and staged files, so a refused request isn't
// tried again.
func clearIncoming(cfg Config) {
	entries, _ := os.ReadDir(release.IncomingDir(cfg.DataDir))
	for _, e := range entries {
		os.Remove(filepath.Join(release.IncomingDir(cfg.DataDir), e.Name()))
	}
}

// moveFile renames, or copies when the folders are on different volumes.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	in.Close()
	return os.Remove(src)
}

// ExeVersion asks an installed exmon.exe its version (`exmon.exe version`).
func ExeVersion(exe string) (string, error) {
	out, err := exec.Command(exe, "version").Output()
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(out))
	if _, err := release.ParseVersion(v); err != nil {
		return "", err
	}
	return v, nil
}
