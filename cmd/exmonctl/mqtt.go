package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/gorilla/websocket"
)

// broker is exmonctl's short-lived MQTT connection to Event Grid. It signs in
// with your Entra account (an `az` token, "OAUTH2-JWT"), so admins need no
// certificate; Event Grid allows it through the "EventGrid TopicSpaces
// Publisher/Subscriber" roles on the admin topic space.
type broker struct {
	cm *autopaho.ConnectionManager

	mu       sync.Mutex
	received map[string][]byte // topic -> last payload, for subscriptions
}

func connectBroker(p Profile) (*broker, error) {
	if p.Host == "" {
		return nil, errors.New("no Event Grid host: set host in the profile or use --host")
	}
	token, err := azToken("https://eventgrid.azure.net/")
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse("wss://" + p.Host + ":443/mqtt")
	var id [6]byte
	rand.Read(id[:])
	b := &broker{received: map[string][]byte{}}
	up := make(chan struct{}, 1)
	failed := make(chan error, 1)
	ctx := context.Background()
	cm, err := autopaho.NewConnection(ctx, autopaho.ClientConfig{
		ServerUrls: []*url.URL{u},
		TlsCfg:     &tls.Config{MinVersion: tls.VersionTLS12, ServerName: p.Host},
		WebSocketCfg: &autopaho.WebSocketConfig{Dialer: func(_ *url.URL, tc *tls.Config) *websocket.Dialer {
			return &websocket.Dialer{Proxy: http.ProxyFromEnvironment, TLSClientConfig: tc, Subprotocols: []string{"mqtt"},
				HandshakeTimeout: 30 * time.Second}
		}},
		KeepAlive:                     60,
		CleanStartOnInitialConnection: true,
		ConnectTimeout:                30 * time.Second,
		ConnectPacketBuilder: func(cp *paho.Connect, _ *url.URL) (*paho.Connect, error) {
			if cp.Properties == nil {
				cp.Properties = &paho.ConnectProperties{}
			}
			cp.Properties.AuthMethod = "OAUTH2-JWT"
			cp.Properties.AuthData = []byte(token)
			return cp, nil
		},
		OnConnectionUp: func(*autopaho.ConnectionManager, *paho.Connack) {
			select {
			case up <- struct{}{}:
			default:
			}
		},
		OnConnectError: func(err error) {
			select {
			case failed <- err:
			default:
			}
		},
		ClientConfig: paho.ClientConfig{
			ClientID: "exmonctl-" + hex.EncodeToString(id[:]),
			// paho calls AuthHandler.Authenticated() whenever a CONNACK carries an
			// auth method (Event Grid echoes OAUTH2-JWT) and panics if it is nil.
			AuthHandler: jwtAuth{},
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){func(pr paho.PublishReceived) (bool, error) {
				b.mu.Lock()
				b.received[pr.Packet.Topic] = append([]byte(nil), pr.Packet.Payload...)
				b.mu.Unlock()
				return true, nil
			}},
		},
	})
	if err != nil {
		return nil, err
	}
	b.cm = cm
	select {
	case <-up:
		return b, nil
	case err := <-failed:
		cm.Disconnect(ctx)
		return nil, fmt.Errorf("connect to %s: %w (does your account have the EventGrid TopicSpaces Publisher role?)", p.Host, err)
	case <-time.After(45 * time.Second):
		cm.Disconnect(ctx)
		return nil, fmt.Errorf("connect to %s: timed out", p.Host)
	}
}

// publish sends one message (QoS 1). expiry 0 = none.
func (b *broker) publish(topic string, payload []byte, retain bool, expiry time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pub := &paho.Publish{QoS: 1, Retain: retain, Topic: topic, Payload: payload}
	if expiry > 0 {
		e := uint32(expiry / time.Second)
		pub.Properties = &paho.PublishProperties{MessageExpiry: &e}
	}
	_, err := b.cm.Publish(ctx, pub)
	return err
}

// collect subscribes to a filter and returns what arrives within wait (retained
// messages arrive straight away).
func (b *broker) collect(filter string, wait time.Duration) (map[string][]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := b.cm.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: filter, QoS: 1}}}); err != nil {
		return nil, err
	}
	time.Sleep(wait)
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string][]byte, len(b.received))
	for k, v := range b.received {
		out[k] = v
	}
	return out, nil
}

func (b *broker) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b.cm.Disconnect(ctx)
}

func inboxTopic(device, kind string) string { return "exmon/v1/device/" + device + "/inbox/" + kind }

// jwtAuth satisfies paho.Auther for the OAUTH2-JWT sign-in. The token goes in
// the CONNECT packet, so there is no AUTH exchange: Event Grid answers with a
// CONNACK straight away. (A Go interface is satisfied by any type with the
// right methods; an empty struct is enough when they need no state.)
type jwtAuth struct{}

func (jwtAuth) Authenticate(*paho.Auth) *paho.Auth { return &paho.Auth{ReasonCode: 0} }
func (jwtAuth) Authenticated()                     {}
