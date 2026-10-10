package control

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"

	"github.com/eclipse/paho.golang/autopaho"

	"exmon/internal/winsys"
)

// errNoClientCert means no usable client certificate was found.
var errNoClientCert = errors.New("no usable client certificate")

// Reasons a connection attempt failed, as shown in CONTROL/unreachable.
const (
	reasonNoCert   = "no_client_cert" // no certificate (not enrolled, expired)
	reasonKeyAcc   = "key_access"     // the certificate is there but exmon may not use its key (exmon-helper grants it)
	reasonDNS      = "dns"            // the broker's name doesn't resolve
	reasonTCP      = "tcp"            // can't reach port 443 (firewall, no network)
	reasonProxy    = "proxy"          // the proxy refused or failed
	reasonTLS      = "tls"            // TLS failed: often a TLS-inspecting proxy, or the broker rejected the certificate
	reasonAuth     = "auth_rejected"  // the broker refused this device (unknown, disabled or not authorised)
	reasonBroker   = "broker_error"   // the broker refused for another reason
	reasonWebSock  = "websocket"      // the WebSocket upgrade failed (wrong path, or a proxy blocking WebSockets)
	reasonUnknown  = "network"
	reasonTimeout  = "timeout"
	reasonShutdown = "shutdown"
)

// classify turns a connection error into one of the reasons above. errors.As
// looks through wrapped errors for one of a given type.
func classify(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, winsys.ErrKeyAccess) {
		return reasonKeyAcc
	}
	if errors.Is(err, errNoClientCert) {
		return reasonNoCert
	}
	var connack *autopaho.ConnackError
	if errors.As(err, &connack) {
		switch connack.ReasonCode {
		case 0x86, 0x87, 0x8A, 0x8C: // bad credentials, not authorised, banned, bad auth method
			return reasonAuth
		}
		return reasonBroker
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return reasonDNS
	}
	var unknownCA x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var certErr x509.CertificateInvalidError
	var recordErr tls.RecordHeaderError
	var alert tls.AlertError
	if errors.As(err, &unknownCA) || errors.As(err, &hostErr) || errors.As(err, &certErr) ||
		errors.As(err, &recordErr) || errors.As(err, &alert) {
		return reasonTLS
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "proxy"):
		return reasonProxy
	case strings.Contains(msg, "tls:") || strings.Contains(msg, "x509:") || strings.Contains(msg, "certificate"):
		return reasonTLS
	// The WebSocket dialer wraps a failed TCP connect as "websocket connection
	// failed: dial tcp …", so look for the dial before the word "websocket".
	case strings.Contains(msg, "dial tcp") && (strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded")):
		return reasonTimeout
	case strings.Contains(msg, "dial tcp"):
		return reasonTCP
	case strings.Contains(msg, "bad handshake") || strings.Contains(msg, "websocket"):
		return reasonWebSock
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return reasonTimeout
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return reasonTCP
	}
	return reasonUnknown
}
