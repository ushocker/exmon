package updater

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"exmon/internal/control"
	"exmon/internal/event"
	"exmon/internal/release"
	"exmon/internal/signdoc"
	"exmon/internal/version"
	"exmon/internal/winsys"
)

// HelperService is the privileged service that applies updates.
const HelperService = "exmon-helper"

// Updater handles release manifests for one run of exmon.
type Updater struct {
	cfg       control.UpdateConfig
	asService bool
	dataDir   string // %ProgramData%\exmon
	deviceID  func() string
	log       event.Logger
	ctx       context.Context // ends when this run of exmon ends

	// Replaceable in tests.
	now         func() time.Time
	client      *http.Client
	authentic   func(path string) (*x509.Certificate, error)
	startHelper func() error

	mu      sync.Mutex
	pending int64              // document version of the scheduled job (0 = none)
	cancel  context.CancelFunc // cancels the scheduled job
}

// New creates the updater.
func New(ctx context.Context, cfg control.UpdateConfig, asService bool, dataDir string, deviceID func() string, log event.Logger) *Updater {
	u := &Updater{cfg: cfg, asService: asService, dataDir: dataDir, deviceID: deviceID, log: log, ctx: ctx,
		now: time.Now, authentic: winsys.VerifyAuthenticode,
		startHelper: func() error { return winsys.StartServiceOnly(HelperService) }}
	u.client = &http.Client{
		Timeout: 30 * time.Minute,
		// Only https, and only to allowed hosts, even after a redirect.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" || !slices.Contains(u.cfg.AllowedHosts, strings.ToLower(req.URL.Hostname())) {
				return fmt.Errorf("redirect to %s is not allowed", req.URL.Host)
			}
			return nil
		},
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 30 * time.Second},
	}
	return u
}

// Handle is the control client's handler for inbox/release. The manifest's
// signature, expiry and target were already checked.
func (u *Updater) Handle(raw, payload []byte, h signdoc.Header) (string, error) {
	var m Manifest
	if err := signdoc.DecodeStrict(payload, &m); err != nil {
		return "", err
	}
	if err := validate(m, u.cfg); err != nil {
		return "", err
	}
	cur := version.Version
	switch c := release.Compare(m.ReleaseVersion, cur); {
	case c == 0:
		return "already running " + cur, nil
	case c < 0 && !m.AllowDowngrade:
		return fmt.Sprintf("running %s, newer than %s", cur, m.ReleaseVersion), nil
	}
	if m.MinVersion != "" && release.Compare(cur, m.MinVersion) < 0 {
		return "", fmt.Errorf("running %s; %s needs at least %s first", cur, m.ReleaseVersion, m.MinVersion)
	}
	// The retained manifest is delivered again after every reconnect. If this
	// exact manifest was already tried here and rolled back, don't download it
	// again (the helper would refuse it as a replay anyway). A nil error records
	// its version as handled, so it stays ignored until a new manifest comes.
	var t release.Trial
	if ok, err := release.ReadJSON(release.TrialPath(u.dataDir), release.MaxRequestBytes, &t); ok && err == nil &&
		t.Outcome == "rolled_back" && t.DocVersion == h.Version {
		return fmt.Sprintf("%s was rolled back on this device; publish a new release to try again", m.ReleaseVersion), nil
	}
	if !release.Selected(u.deviceID(), m) {
		return fmt.Sprintf("not in this rollout's %d%%", m.Percent), nil
	}
	if !u.asService {
		return "", errors.New("updates are applied only by the installed service")
	}

	start := release.StartTime(u.deviceID(), m)
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.pending == h.Version {
		return fmt.Sprintf("update to %s already scheduled", m.ReleaseVersion), control.ErrScheduled
	}
	if u.cancel != nil {
		u.cancel() // a newer manifest replaces the scheduled one
	}
	ctx, cancel := context.WithCancel(u.ctx)
	u.pending, u.cancel = h.Version, cancel
	envelope := append([]byte(nil), raw...) // keep our own copy; the caller may reuse raw
	go u.run(ctx, m, h.Version, envelope, start)
	wait := max(0, start.Sub(u.now())).Round(time.Second)
	return fmt.Sprintf("update to %s scheduled in %s", m.ReleaseVersion, wait), control.ErrScheduled
}

// run waits for this device's turn, downloads, does the early checks, and hands
// over to exmon-helper.
func (u *Updater) run(ctx context.Context, m Manifest, docVersion int64, envelope []byte, start time.Time) {
	defer func() {
		u.mu.Lock()
		if u.pending == docVersion {
			u.pending, u.cancel = 0, nil
		}
		u.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return
	case <-time.After(max(0, start.Sub(u.now()))):
	}
	u.log.Log("UPDATE/downloading", fmt.Sprintf("version=%s from %s", m.ReleaseVersion, m.Host()))
	staged, err := u.download(ctx, m)
	if err != nil {
		u.log.Log("UPDATE/failed", fmt.Sprintf("version=%s step=download error=%q", m.ReleaseVersion, err.Error()))
		return
	}
	// An early check only: the helper checks the signature again on its own copy.
	if err := u.verify(staged); err != nil {
		os.Remove(staged)
		u.log.Log("UPDATE/failed", fmt.Sprintf("version=%s step=verify error=%q", m.ReleaseVersion, err.Error()))
		return
	}
	u.log.Log("UPDATE/verified", fmt.Sprintf("version=%s sha256 and code signature OK", m.ReleaseVersion))
	if ctx.Err() != nil {
		return
	}
	req := release.Request{Envelope: envelope, File: filepath.Base(staged)}
	if err := release.WriteJSON(release.RequestPath(u.dataDir), req); err != nil {
		u.log.Log("UPDATE/failed", fmt.Sprintf("version=%s step=handover error=%q", m.ReleaseVersion, err.Error()))
		return
	}
	if err := u.startHelper(); err != nil {
		u.log.Log("UPDATE/failed", fmt.Sprintf("version=%s step=handover error=%q", m.ReleaseVersion, err.Error()))
		return
	}
	u.log.Log("UPDATE/handed_over", fmt.Sprintf("version=%s; %s checks it again, swaps it in and restarts exmon", m.ReleaseVersion, HelperService))
}

// download streams the file into update\incoming, stopping at the declared size
// and checking the SHA-256 as it goes.
func (u *Updater) download(ctx context.Context, m Manifest) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	dir := release.IncomingDir(u.dataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "exmon-"+m.ReleaseVersion+".exe")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	// io.MultiWriter writes every chunk to both the file and the hash; reading one
	// byte more than declared detects a file that is too long.
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, m.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != m.Size {
		err = fmt.Errorf("got %d bytes, expected %d", n, m.Size)
	}
	if err == nil && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), m.SHA256) {
		err = errors.New("sha256 doesn't match the manifest")
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// verify checks the code signature and the pinned publisher.
func (u *Updater) verify(path string) error {
	if !u.cfg.RequireAuthenticode {
		return nil
	}
	cert, err := u.authentic(path)
	if err != nil {
		return err
	}
	if cert.Subject.CommonName != u.cfg.PublisherCN {
		return fmt.Errorf("signed by %q, not the expected publisher %q", cert.Subject.CommonName, u.cfg.PublisherCN)
	}
	return nil
}
