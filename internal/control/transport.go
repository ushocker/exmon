package control

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/gorilla/websocket"

	"exmon/internal/winsys"
)

// pahoTransport is the real MQTT connection: eclipse/paho.golang's autopaho
// package (which reconnects by itself) over WebSockets with TLS.
type pahoTransport struct {
	cfg    Config
	events chan TransportEvent

	mu       sync.Mutex
	cert     *winsys.ClientCert // the certificate in use; picked again on each handshake
	deviceID string
	tls12    bool // the key can't do the RSA-PSS signatures TLS 1.3 needs: use TLS 1.2
	tlsNoted bool

	cm *autopaho.ConnectionManager
}

// NewTransport creates the paho transport for cfg.
func NewTransport(cfg Config) Transport {
	return &pahoTransport{cfg: cfg, events: make(chan TransportEvent, 64)}
}

func (t *pahoTransport) Events() <-chan TransportEvent { return t.events }

func (t *pahoTransport) DeviceID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.deviceID
}

// emit hands an event to the client without ever blocking: paho calls our
// callbacks from its own goroutines, and they must return quickly. If the
// client is far behind, the event is dropped (connection events repeat anyway).
func (t *pahoTransport) emit(ev TransportEvent) {
	select {
	case t.events <- ev:
	default:
	}
}

// selectCert finds the configured client certificate, reusing the open one while
// it is still the newest valid match.
func (t *pahoTransport) selectCert() (*winsys.ClientCert, error) {
	loc, err := winsys.ParseStoreLocation(t.cfg.Cert.Store)
	if err != nil {
		return nil, err
	}
	match := winsys.ClientCertMatch(t.cfg.Cert.SubjectCN, t.cfg.Cert.IssuerCN)
	cc, err := winsys.FindClientCert(loc, "My", match, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNoClientCert, err) // two %w: both stay findable with errors.Is
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cert != nil && t.cert.Leaf.SerialNumber.Cmp(cc.Leaf.SerialNumber) == 0 && t.cert.Leaf.Issuer.String() == cc.Leaf.Issuer.String() {
		cc.Close() // same certificate: keep the handle we have
		return t.cert, nil
	}
	if t.cert != nil {
		t.cert.Close()
	}
	t.cert, t.deviceID = cc, cc.Leaf.Subject.CommonName
	t.tls12 = needsTLS12(cc)
	return cc, nil
}

// needsTLS12 reports whether an RSA key fails the PSS signature TLS 1.3 needs.
// Some TPMs can't make PSS signatures with salt length = hash length; for those,
// TLS 1.2 with PKCS#1 v1.5 signatures still works.
func needsTLS12(cc *winsys.ClientCert) bool {
	pub, ok := cc.Signer.Public().(*rsa.PublicKey)
	if !ok {
		return false
	}
	digest := sha256.Sum256([]byte("exmon tls self-test"))
	opts := &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: crypto.SHA256}
	sig, err := cc.Signer.Sign(rand.Reader, digest[:], opts)
	return err != nil || rsa.VerifyPSS(pub, crypto.SHA256, digest[:], sig, opts) != nil
}

// tlsConfig is the TLS setup for each connection attempt.
func (t *pahoTransport) tlsConfig() *tls.Config {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: t.cfg.Host,
		// Called during every handshake, so a renewed certificate is picked up
		// without restarting exmon.
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			cc, err := t.selectCert()
			if err != nil {
				return nil, err
			}
			tc := &tls.Certificate{Certificate: [][]byte{cc.DER}, PrivateKey: cc.Signer, Leaf: cc.Leaf}
			t.mu.Lock()
			if t.tls12 {
				tc.SupportedSignatureAlgorithms = []tls.SignatureScheme{tls.PKCS1WithSHA256, tls.PKCS1WithSHA384}
			}
			t.mu.Unlock()
			return tc, nil
		},
	}
	switch t.cfg.TLSMaxVersion {
	case "1.2":
		cfg.MaxVersion = tls.VersionTLS12
	case "1.3":
		cfg.MaxVersion = tls.VersionTLS13
	}
	return cfg
}

// dialer builds the WebSocket dialer for one attempt: the "mqtt" subprotocol
// (required), the proxy from HTTPS_PROXY (set with `exmon install --proxy`), and
// TLS 1.2 if the key failed the PSS self-test.
func (t *pahoTransport) dialer(_ *url.URL, tc *tls.Config) *websocket.Dialer {
	tc = tc.Clone()
	t.mu.Lock()
	notice := false
	if t.tls12 {
		tc.MaxVersion = tls.VersionTLS12
		notice, t.tlsNoted = !t.tlsNoted, true
	}
	t.mu.Unlock()
	if notice {
		t.emit(TransportEvent{Kind: TLSFallback})
	}
	return &websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: time.Duration(t.cfg.ConnectTimeoutSeconds) * time.Second,
		TLSClientConfig:  tc,
		Subprotocols:     []string{"mqtt"},
	}
}

// Start begins connecting. autopaho keeps reconnecting (with our backoff) until
// ctx ends or Close is called.
func (t *pahoTransport) Start(ctx context.Context) error {
	u, err := url.Parse("wss://" + t.cfg.Host + ":" + strconv.Itoa(t.cfg.Port) + t.cfg.Path)
	if err != nil {
		return err
	}
	// Find the certificate once up front, so the first CONNECT has the right name.
	// Not finding one isn't fatal: we keep trying, and the chirp says why.
	if _, err := t.selectCert(); err != nil {
		t.emit(TransportEvent{Kind: ConnectFailed, Err: err})
	}
	maxPacket := uint32(t.cfg.MaxInboundKB) << 10
	sessionExpiry := uint32(t.cfg.SessionExpiryMinutes * 60)
	willDelay := uint32(30) // the broker publishes "offline" only if we don't come back within 30s

	cfg := autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{u},
		TlsCfg:                        t.tlsConfig(),
		WebSocketCfg:                  &autopaho.WebSocketConfig{Dialer: t.dialer},
		KeepAlive:                     uint16(t.cfg.KeepAliveSeconds),
		CleanStartOnInitialConnection: false, // keep the session so commands queued while we were away arrive
		SessionExpiryInterval:         sessionExpiry,
		ConnectTimeout:                time.Duration(t.cfg.ConnectTimeoutSeconds) * time.Second,
		ReconnectBackoff:              reconnectBackoff,
		// The device's name changes only if its certificate does, so it is set
		// per attempt. Event Grid wants the authentication name as the username.
		// No user properties are added: Event Grid fails CONNECTs that carry them.
		ConnectPacketBuilder: func(cp *paho.Connect, _ *url.URL) (*paho.Connect, error) {
			cc, err := t.selectCert()
			if err != nil {
				return nil, err
			}
			id := cc.Leaf.Subject.CommonName
			cp.ClientID, cp.Username, cp.UsernameFlag = id, id, true
			if cp.Properties == nil {
				cp.Properties = &paho.ConnectProperties{}
			}
			cp.Properties.MaximumPacketSize = &maxPacket
			cp.WillMessage = &paho.WillMessage{Retain: true, QoS: 1, Topic: statusTopic(id),
				Payload: []byte(`{"online":false,"reason":"lost"}`)}
			cp.WillProperties = &paho.WillProperties{WillDelayInterval: &willDelay}
			return cp, nil
		},
		OnConnectionUp: func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
			// Subscribing waits for the broker, and this callback must not block.
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				_, err := cm.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{
					{Topic: inboxFilter(t.DeviceID()), QoS: 1},
				}})
				if err != nil {
					t.emit(TransportEvent{Kind: Down, Err: fmt.Errorf("subscribe to the inbox: %w", err)})
					return
				}
				t.emit(TransportEvent{Kind: Up})
			}()
		},
		OnConnectionDown: func() bool {
			t.emit(TransportEvent{Kind: Down})
			return true // keep reconnecting
		},
		OnConnectError: func(err error) { t.emit(TransportEvent{Kind: ConnectFailed, Err: err}) },
		ClientConfig: paho.ClientConfig{
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(pr paho.PublishReceived) (bool, error) {
					// Copy what we keep: paho may reuse the packet.
					t.emit(TransportEvent{Kind: Message, Topic: pr.Packet.Topic, Payload: append([]byte(nil), pr.Packet.Payload...)})
					return true, nil
				},
			},
		},
	}
	// Not ctx itself: autopaho drops the connection the moment its context ends,
	// which is exactly when Client.shutdown still needs it to publish "offline".
	// context.WithoutCancel keeps ctx's values but not its cancellation, so the
	// connection lives until Close sends a clean DISCONNECT.
	cm, err := autopaho.NewConnection(context.WithoutCancel(ctx), cfg)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.cm = cm
	t.mu.Unlock()
	return nil
}

// reconnectBackoff waits 5s before the first retry, doubling up to 10 minutes,
// with ±20% jitter so a fleet that lost the broker together doesn't return together.
func reconnectBackoff(attempt int) time.Duration {
	if attempt == 0 {
		return 2 * time.Second // the first connection, shortly after startup
	}
	d := 5 * time.Second << min(attempt-1, 7) // 5s, 10s ... 640s
	d = min(d, 10*time.Minute)
	var b [1]byte
	rand.Read(b[:])
	jitter := 0.8 + 0.4*float64(b[0])/255 // 0.8 .. 1.2
	return time.Duration(float64(d) * jitter)
}

func (t *pahoTransport) Publish(ctx context.Context, topic string, payload []byte, retain bool, expiry time.Duration) error {
	t.mu.Lock()
	cm := t.cm
	t.mu.Unlock()
	if cm == nil {
		return fmt.Errorf("not connected")
	}
	p := &paho.Publish{QoS: 1, Retain: retain, Topic: topic, Payload: payload}
	if expiry > 0 {
		e := uint32(expiry / time.Second)
		p.Properties = &paho.PublishProperties{MessageExpiry: &e}
	}
	_, err := cm.Publish(ctx, p)
	return err
}

func (t *pahoTransport) Close(ctx context.Context) error {
	t.mu.Lock()
	cm := t.cm
	t.mu.Unlock()
	var err error
	if cm != nil {
		err = cm.Disconnect(ctx) // a clean DISCONNECT: the broker does not publish the will
	}
	t.mu.Lock()
	if t.cert != nil {
		t.cert.Close()
		t.cert = nil
	}
	t.mu.Unlock()
	return err
}
