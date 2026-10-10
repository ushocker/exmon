package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"exmon/internal/event"
	"exmon/internal/signdoc"
	"exmon/internal/version"
)

// Transport is the MQTT connection, behind an interface so the client's logic
// can be tested with a fake. The paho implementation is in transport.go.
//
// A transport reports what happens on its Events channel and never calls back
// into the client, so the client's state is only touched by its own goroutine.
type Transport interface {
	// Start begins connecting (and reconnecting) in the background.
	Start(ctx context.Context) error
	// Events delivers connection changes and received messages.
	Events() <-chan TransportEvent
	// Publish sends a message (QoS 1). expiry 0 = no expiry.
	Publish(ctx context.Context, topic string, payload []byte, retain bool, expiry time.Duration) error
	// DeviceID is the authentication name in use ("" until a certificate is found).
	DeviceID() string
	// Close says goodbye to the broker and stops reconnecting.
	Close(ctx context.Context) error
}

// TransportEvent is one thing that happened on the connection.
type TransportEvent struct {
	Kind    EventKind
	Err     error  // for ConnectFailed and Down
	Topic   string // for Message
	Payload []byte // for Message
}

// EventKind says what a TransportEvent is.
type EventKind int

const (
	Up EventKind = iota + 1
	Down
	ConnectFailed
	Message
	TLSFallback // the RSA key can't do TLS 1.3's PSS signatures; using TLS 1.2
)

// Handler applies a verified document. raw is the whole signed envelope (to
// store, e.g. the cloud config), payload its verified body. It returns a short
// result for the outbox and the log, or an error.
type Handler func(raw, payload []byte, h signdoc.Header) (string, error)

// ErrScheduled is returned by a Handler that accepted a document but will act
// on it later (an update waiting for its turn in the rollout). The client logs and
// acknowledges it, but doesn't record the version as applied, so the retained copy
// delivered again after a restart schedules it again.
var ErrScheduled = errors.New("scheduled")

// Deps are what the client needs from the rest of exmon.
type Deps struct {
	Transport Transport
	Keys      signdoc.Keys
	State     *State
	Log       event.Logger
	// Handlers by inbox topic suffix ("config", "release"). Commands ("cmd") are
	// handled by Commands.
	Handlers map[string]Handler
	// Commands are the fixed list of commands a signed command document may run,
	// by name. There is deliberately no way to run anything else.
	Commands map[string]func() (string, error)
	// Status adds fields to the status message (config version, update state ...).
	Status func() map[string]any
	// StartHelper starts exmon-helper (key access grant). nil = not available.
	StartHelper func() error
	Now         func() time.Time // nil = time.Now
}

// Client is the control-plane client. It implements source.Source, so main runs
// it like a monitor.
type Client struct {
	cfg  Config
	deps Deps

	started   time.Time
	connected bool
	everUp    atomic.Bool // has connected at least once (read by the updater's health check)
	downSince time.Time   // when the broker last became unreachable
	lastChirp time.Time
	attempts  int    // failed attempts since downSince
	lastErr   string // the last failure, for the chirp
	reason    string // its classified reason

	lastApply  time.Time // rate limit for config/release documents
	lastHelper time.Time // when exmon-helper was last started for key access
	statusMu   sync.Mutex
	lastSent   []byte // last status sent, to republish only on change
}

// minApplyGap is the least time between applying two config or release
// documents, so a burst of (validly signed) documents can't restart exmon in a
// loop.
const minApplyGap = 5 * time.Minute

// New creates the client.
func New(cfg Config, deps Deps) *Client {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Client{cfg: cfg, deps: deps}
}

// DeviceID is the device's authentication name ("" until a certificate is found).
func (c *Client) DeviceID() string { return c.deps.Transport.DeviceID() }

// HasConnected reports whether the client has connected to the broker at least
// once since exmon started. Safe to call from any goroutine.
func (c *Client) HasConnected() bool { return c.everUp.Load() }

// Name implements source.Source.
func (c *Client) Name() string { return "control" }

// Run implements source.Source: it connects and handles events until ctx ends.
func (c *Client) Run(ctx context.Context, log event.Logger) error {
	if c.deps.Log == nil {
		c.deps.Log = log
	}
	now := c.deps.Now()
	c.started, c.downSince = now, now
	if err := c.deps.Transport.Start(ctx); err != nil {
		return err
	}
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	statusEvery := time.Duration(c.cfg.StatusMinutes) * time.Minute
	lastStatus := now

	for {
		select {
		case <-ctx.Done():
			c.shutdown()
			return nil
		case ev := <-c.deps.Transport.Events():
			c.handle(ctx, ev)
		case t := <-tick.C:
			c.maybeChirp(t)
			c.startHelper(t, 24*time.Hour, "daily") // also catches a certificate Intune renewed
			if c.connected && t.Sub(lastStatus) >= statusEvery {
				c.publishStatus(ctx, true)
				lastStatus = t
			}
		}
	}
}

func (c *Client) log(kind, format string, args ...any) {
	c.deps.Log.Log(kind, fmt.Sprintf(format, args...))
}

func (c *Client) handle(ctx context.Context, ev TransportEvent) {
	now := c.deps.Now()
	switch ev.Kind {
	case Up:
		if c.attempts > 0 || !c.lastChirp.IsZero() {
			c.log("CONTROL/connected", "device=%s broker=%s downtime=%s after %d failed attempt(s)",
				c.deps.Transport.DeviceID(), c.cfg.Host, now.Sub(c.downSince).Round(time.Second), c.attempts)
		} else {
			c.log("CONTROL/connected", "device=%s broker=%s", c.deps.Transport.DeviceID(), c.cfg.Host)
		}
		c.connected, c.attempts, c.lastErr, c.reason, c.lastChirp = true, 0, "", "", time.Time{}
		c.everUp.Store(true)
		c.publishStatus(ctx, true)
	case Down:
		if c.connected {
			c.log("CONTROL/disconnected", "reason=%s error=%q", classify(ev.Err), errText(ev.Err))
		}
		c.connected, c.downSince = false, now
	case ConnectFailed:
		c.attempts++
		c.lastErr, c.reason = errText(ev.Err), classify(ev.Err)
		if c.reason == reasonKeyAcc {
			c.startHelper(now, time.Hour, "key_access")
		}
	case Message:
		c.onMessage(ctx, ev.Topic, ev.Payload)
	case TLSFallback:
		c.log("CONTROL/tls_fallback", "the certificate's key (TPM) can't make the RSA-PSS signatures TLS 1.3 needs; using TLS 1.2")
	}
}

// maybeChirp logs CONTROL/unreachable once the broker has been unreachable for
// chirpMinutes, and again every chirpMinutes while it stays that way.
func (c *Client) maybeChirp(now time.Time) {
	if c.connected {
		return
	}
	every := time.Duration(c.cfg.ChirpMinutes) * time.Minute
	if now.Sub(c.downSince) < every || (!c.lastChirp.IsZero() && now.Sub(c.lastChirp) < every) {
		return
	}
	reason := c.reason
	if reason == "" {
		reason = reasonTimeout // no attempt has even finished
	}
	c.log("CONTROL/unreachable", "broker=%s for=%s reason=%s attempts=%d last=%q",
		c.cfg.Host, now.Sub(c.downSince).Round(time.Minute), reason, c.attempts, c.lastErr)
	c.lastChirp = now
}

// startHelper starts exmon-helper so it (re)grants access to the certificate's
// key, at most once per every.
func (c *Client) startHelper(now time.Time, every time.Duration, why string) {
	if c.deps.StartHelper == nil || (!c.lastHelper.IsZero() && now.Sub(c.lastHelper) < every) {
		return
	}
	if c.lastHelper.IsZero() && why == "daily" {
		c.lastHelper = now // the helper already ran at boot; the first daily start is a day from now
		return
	}
	c.lastHelper = now
	if err := c.deps.StartHelper(); err != nil {
		c.log("CONTROL/helper_failed", "reason=%s error=%q", why, err.Error())
		return
	}
	c.log("CONTROL/helper_started", "reason=%s", why)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// onMessage verifies and dispatches one inbox message.
func (c *Client) onMessage(ctx context.Context, topic string, raw []byte) {
	id := c.deps.Transport.DeviceID()
	sub, ok := inboxKind(id, topic)
	if !ok {
		return // not ours (the broker shouldn't send it)
	}
	if len(raw) == 0 {
		return // an empty retained message just clears a topic
	}
	kinds := map[string]string{"config": signdoc.KindConfig, "release": signdoc.KindRelease, "cmd": signdoc.KindCommand}
	kind, ok := kinds[sub]
	if !ok {
		c.log("CONTROL/rejected", "topic=%s reason=%q", sub, "unknown inbox topic")
		return
	}
	payload, h, err := signdoc.Verify(raw, kind, c.deps.Keys, id, c.deps.Now())
	if errors.Is(err, signdoc.ErrNotForUs) {
		return
	}
	if err != nil {
		c.log("CONTROL/rejected", "kind=%s reason=%q", kind, err.Error())
		return
	}
	last := c.deps.State.Version(kind)
	switch {
	case h.Version == last:
		return // already applied (e.g. the retained copy, sent again on reconnect)
	case h.Version < last:
		c.log("CONTROL/rejected", "kind=%s version=%d reason=%q", kind, h.Version,
			fmt.Sprintf("older than the applied version %d", last))
		return
	}

	var result string
	if kind == signdoc.KindCommand {
		result, err = c.runCommand(payload)
	} else {
		now := c.deps.Now()
		if kind == signdoc.KindConfig && !c.lastApply.IsZero() && now.Sub(c.lastApply) < minApplyGap {
			c.log("CONTROL/rejected", "kind=%s version=%d reason=%q", kind, h.Version, "too soon after the previous one; will apply when sent again")
			return
		}
		handler := c.deps.Handlers[sub]
		if handler == nil {
			c.log("CONTROL/rejected", "kind=%s reason=%q", kind, "this build can't apply it")
			return
		}
		if kind == signdoc.KindConfig {
			c.lastApply = now
		}
		result, err = handler(raw, payload, h)
	}
	if errors.Is(err, ErrScheduled) {
		c.log("CONTROL/scheduled", "kind=%s version=%d %s", kind, h.Version, result)
		c.reply(ctx, kind, h.Version, true, result)
		return
	}
	if err != nil {
		c.log("CONTROL/failed", "kind=%s version=%d error=%q", kind, h.Version, err.Error())
		c.reply(ctx, kind, h.Version, false, err.Error())
		return
	}
	if serr := c.deps.State.SetVersion(kind, h.Version); serr != nil {
		c.log("CONTROL/state_error", "%s", serr.Error())
	}
	c.log("CONTROL/applied", "kind=%s version=%d %s", kind, h.Version, result)
	c.reply(ctx, kind, h.Version, true, result)
}

// commandPayload is a command document's body.
type commandPayload struct {
	signdoc.Header
	Command string `json:"command"`
}

func (c *Client) runCommand(payload []byte) (string, error) {
	var p commandPayload
	if err := signdoc.DecodeStrict(payload, &p); err != nil {
		return "", err
	}
	fn, ok := c.deps.Commands[p.Command]
	if !ok {
		return "", fmt.Errorf("unknown command %q", p.Command)
	}
	return fn()
}

// reply publishes a result on the outbox topic.
func (c *Client) reply(ctx context.Context, kind string, ver int64, ok bool, detail string) {
	b, _ := json.Marshal(map[string]any{"kind": kind, "version": ver, "ok": ok, "detail": detail,
		"time": c.deps.Now().UTC().Format(time.RFC3339)})
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = c.deps.Transport.Publish(pctx, outboxTopic(c.deps.Transport.DeviceID()), b, false, 24*time.Hour)
}

// PublishStatus sends the status now (the report-status command).
func (c *Client) PublishStatus(ctx context.Context) { c.publishStatus(ctx, true) }

// publishStatus sends the retained status message. With force false it is only
// sent if something changed.
func (c *Client) publishStatus(ctx context.Context, force bool) {
	st := map[string]any{
		"online":  true,
		"device":  c.deps.Transport.DeviceID(),
		"version": version.Version,
		"keyset":  signdoc.KeySetName(),
		"uptimeS": int(c.deps.Now().Sub(c.started).Seconds()),
		"time":    c.deps.Now().UTC().Format(time.RFC3339),
	}
	if c.deps.Status != nil {
		for k, v := range c.deps.Status() {
			st[k] = v
		}
	}
	b, _ := json.Marshal(st)
	c.statusMu.Lock()
	same := string(b) == string(c.lastSent)
	c.lastSent = b
	c.statusMu.Unlock()
	if same && !force {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = c.deps.Transport.Publish(pctx, statusTopic(c.deps.Transport.DeviceID()), b, true, 0)
}

// shutdown marks the device offline and disconnects cleanly.
func (c *Client) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if c.connected {
		b, _ := json.Marshal(map[string]any{"online": false, "device": c.deps.Transport.DeviceID(),
			"version": version.Version, "time": c.deps.Now().UTC().Format(time.RFC3339), "reason": reasonShutdown})
		_ = c.deps.Transport.Publish(ctx, statusTopic(c.deps.Transport.DeviceID()), b, true, 0)
	}
	_ = c.deps.Transport.Close(ctx)
}
