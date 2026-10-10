package control

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"

	"exmon/internal/event"
	"exmon/internal/signdoc"
)

// fakeTransport records publishes; the test feeds events by calling the client.
type fakeTransport struct {
	mu        sync.Mutex
	published []string // "topic retain payload"
	events    chan TransportEvent
}

func (f *fakeTransport) Start(context.Context) error   { return nil }
func (f *fakeTransport) Events() <-chan TransportEvent { return f.events }
func (f *fakeTransport) DeviceID() string              { return "exmon-test-01" }
func (f *fakeTransport) Close(context.Context) error   { return nil }
func (f *fakeTransport) Publish(_ context.Context, topic string, p []byte, retain bool, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, fmt.Sprintf("%s %v %s", topic, retain, p))
	return nil
}
func (f *fakeTransport) all() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.published, "\n")
}

type logRec struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRec) Log(kind, detail string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, kind+" "+detail)
}
func (l *logRec) Metrics(string, map[string]float64)        {}
func (l *logRec) DeclareMetrics(map[string]event.Direction) {}
func (l *logRec) Held() bool                                { return false }
func (l *logRec) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

type harness struct {
	c      *Client
	tr     *fakeTransport
	log    *logRec
	now    time.Time
	signer signdoc.FileSigner
	ran    []string
}

func newHarness(t *testing.T) *harness {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	h := &harness{tr: &fakeTransport{events: make(chan TransportEvent, 10)}, log: &logRec{},
		now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), signer: signdoc.FileSigner{ID: "cfg", Key: k}}
	st, err := LoadState(filepath.Join(t.TempDir(), "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Host = "ns.eastus-1.ts.eventgrid.azure.net"
	h.c = New(cfg, Deps{
		Transport: h.tr,
		Keys:      signdoc.Keys{Config: signdoc.KeySet{"cfg": &k.PublicKey}, Release: signdoc.KeySet{}},
		State:     st,
		Log:       h.log,
		Handlers: map[string]Handler{"config": func(_, p []byte, hd signdoc.Header) (string, error) {
			h.ran = append(h.ran, fmt.Sprintf("config v%d", hd.Version))
			return "applied", nil
		}},
		Commands: map[string]func() (string, error){"report-status": func() (string, error) {
			h.ran = append(h.ran, "report-status")
			return "sent", nil
		}},
		Now: func() time.Time { return h.now },
	})
	h.c.started, h.c.downSince = h.now, h.now
	return h
}

func (h *harness) doc(t *testing.T, kind string, ver int64, extra map[string]any) []byte {
	p := map[string]any{"kind": kind, "version": ver, "issuedAt": h.now.Add(-time.Minute), "expiresAt": h.now.Add(time.Hour)}
	for k, v := range extra {
		p[k] = v
	}
	b, _ := json.Marshal(p)
	env, err := signdoc.Seal(b, h.signer)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func (h *harness) send(topicSuffix string, payload []byte) {
	h.c.handle(context.Background(), TransportEvent{Kind: Message,
		Topic: "exmon/v1/device/exmon-test-01/inbox/" + topicSuffix, Payload: payload})
}

func TestChirp(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.c.handle(ctx, TransportEvent{Kind: ConnectFailed, Err: &net.DNSError{Err: "no such host", Name: "ns"}})

	h.now = h.now.Add(14 * time.Minute)
	h.c.maybeChirp(h.now)
	if strings.Contains(h.log.text(), "unreachable") {
		t.Fatal("chirped before chirpMinutes")
	}
	h.now = h.now.Add(time.Minute)
	h.c.maybeChirp(h.now)
	h.c.maybeChirp(h.now.Add(time.Minute)) // not again within chirpMinutes
	if n := strings.Count(h.log.text(), "CONTROL/unreachable"); n != 1 || !strings.Contains(h.log.text(), "reason=dns") {
		t.Fatalf("want one dns chirp, got:\n%s", h.log.text())
	}
	h.now = h.now.Add(15 * time.Minute)
	h.c.maybeChirp(h.now)
	if n := strings.Count(h.log.text(), "CONTROL/unreachable"); n != 2 {
		t.Errorf("want a second chirp after another chirpMinutes, got %d", n)
	}

	h.c.handle(ctx, TransportEvent{Kind: Up})
	if !strings.Contains(h.log.text(), "CONTROL/connected device=exmon-test-01") || !strings.Contains(h.log.text(), "downtime=30m0s") {
		t.Errorf("connected line:\n%s", h.log.text())
	}
	if !strings.Contains(h.tr.all(), "exmon/v1/device/exmon-test-01/status true") {
		t.Errorf("status not published (retained) on connect:\n%s", h.tr.all())
	}
	h.c.maybeChirp(h.now.Add(time.Hour))
	if n := strings.Count(h.log.text(), "CONTROL/unreachable"); n != 2 {
		t.Error("no chirps while connected")
	}
}

func TestMessages(t *testing.T) {
	h := newHarness(t)
	h.send("config", h.doc(t, signdoc.KindConfig, 100, nil))
	h.send("config", h.doc(t, signdoc.KindConfig, 100, nil)) // retained copy again: ignored silently
	if len(h.ran) != 1 || h.c.deps.State.Version(signdoc.KindConfig) != 100 {
		t.Fatalf("ran %v, version %d", h.ran, h.c.deps.State.Version(signdoc.KindConfig))
	}
	if !strings.Contains(h.tr.all(), `outbox false {"detail":"applied","kind":"exmon-config","ok":true`) {
		t.Errorf("no ack on the outbox:\n%s", h.tr.all())
	}

	h.send("config", h.doc(t, signdoc.KindConfig, 50, nil))
	if !strings.Contains(h.log.text(), "older than the applied version 100") {
		t.Errorf("replay of an older version should be rejected:\n%s", h.log.text())
	}

	h.send("config", h.doc(t, signdoc.KindConfig, 200, nil))
	if !strings.Contains(h.log.text(), "too soon") || len(h.ran) != 1 {
		t.Errorf("a second config within 5 minutes should wait: ran %v", h.ran)
	}
	h.now = h.now.Add(6 * time.Minute)
	h.send("config", h.doc(t, signdoc.KindConfig, 200, nil))
	if len(h.ran) != 2 {
		t.Errorf("after the gap it should apply: ran %v", h.ran)
	}

	bad := h.doc(t, signdoc.KindConfig, 300, nil)
	bad[len(bad)-5] ^= 1 // corrupt the signature
	h.send("config", bad)
	if !strings.Contains(h.log.text(), "CONTROL/rejected kind=exmon-config") {
		t.Errorf("bad signature should be rejected:\n%s", h.log.text())
	}

	h.send("cmd", h.doc(t, signdoc.KindCommand, 1, map[string]any{"command": "report-status"}))
	h.send("cmd", h.doc(t, signdoc.KindCommand, 2, map[string]any{"command": "format-c"}))
	if h.ran[len(h.ran)-1] != "report-status" || !strings.Contains(h.log.text(), `unknown command \"format-c\"`) {
		t.Errorf("commands: ran %v\n%s", h.ran, h.log.text())
	}

	h.send("release", h.doc(t, signdoc.KindRelease, 1, nil)) // signed with the config key
	if !strings.Contains(h.log.text(), "not trusted for exmon-release") {
		t.Errorf("a config key must not sign releases:\n%s", h.log.text())
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("wrap: %w", errNoClientCert), reasonNoCert},
		{&net.DNSError{Err: "no such host"}, reasonDNS},
		{&net.OpError{Op: "dial", Err: errors.New("connection refused")}, reasonTCP},
		{errors.New("proxyconnect tcp: dial tcp: connection refused"), reasonProxy},
		{errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority"), reasonTLS},
		{errors.New("websocket connection failed: websocket: bad handshake"), reasonWebSock},
		// Seen on a test PC with an outbound firewall block (WSAEACCES).
		{errors.New("failed to connect to wss://x:443/mqtt: websocket connection failed: dial tcp 203.0.113.7:443: connectex: An attempt was made to access a socket in a way forbidden by its access permissions."), reasonTCP},
		{errors.New("websocket connection failed: dial tcp 203.0.113.7:443: i/o timeout"), reasonTimeout},
		{autopaho.NewConnackError(errors.New("refused"), &paho.Connack{ReasonCode: 0x87}), reasonAuth},
		{autopaho.NewConnackError(errors.New("refused"), &paho.Connack{ReasonCode: 0x89}), reasonBroker},
		{errors.New("something odd"), reasonUnknown},
	}
	for _, tt := range tests {
		if got := classify(tt.err); got != tt.want {
			t.Errorf("%v: got %s, want %s", tt.err, got, tt.want)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	c := DefaultConfig()
	if c.Validate() != nil {
		t.Error("disabled config must validate")
	}
	c.Enabled = true
	c.Host = "ns.eastus-1.ts.eventgrid.azure.net"
	c.Cert.SubjectCN = "exmon-test-01"
	if err := c.Validate(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	bad := []func(*Config){
		func(c *Config) { c.Host = "wss://x" },
		func(c *Config) { c.Cert = CertConfig{} },
		func(c *Config) { c.ChirpMinutes = 0 },
		func(c *Config) { c.KeepAliveSeconds = 2000 },
		func(c *Config) { c.TLSMaxVersion = "1.0" },
	}
	for i, f := range bad {
		cc := c
		f(&cc)
		if cc.Validate() == nil {
			t.Errorf("bad config %d accepted", i)
		}
	}
}

func TestReconnectBackoff(t *testing.T) {
	if d := reconnectBackoff(0); d != 2*time.Second {
		t.Errorf("first = %v", d)
	}
	for i := 1; i < 20; i++ {
		if d := reconnectBackoff(i); d < 4*time.Second || d > 12*time.Minute {
			t.Errorf("attempt %d = %v", i, d)
		}
	}
}
