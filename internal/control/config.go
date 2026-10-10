// Package control is exmon's control-plane client: an MQTT v5 connection over
// WebSockets (port 443) to an Azure Event Grid namespace, authenticated with the
// device's TPM-held client certificate.
//
// Over it a device:
//   - publishes its status (retained) to   exmon/v1/device/<id>/status
//   - publishes command results to         exmon/v1/device/<id>/outbox
//   - receives signed documents on         exmon/v1/device/<id>/inbox/{config,release,cmd}
//
// Nothing arrives except signed documents (internal/signdoc), and there is no
// HTTP fallback. While the broker can't be reached, the client "chirps": every
// chirpMinutes it logs CONTROL/unreachable with the reason, which the remotes
// (e.g. Azure Monitor) forward, so stuck devices can be found centrally.
package control

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"exmon/internal/limits"
)

// Config holds the control-plane settings. They are install-time settings (local
// config or install flags) and can't be changed by a cloud config, except
// ChirpMinutes and StatusMinutes.
type Config struct {
	Enabled bool   `json:"enabled"`
	Host    string `json:"host"` // Event Grid MQTT hostname: <namespace>.<region>-1.ts.eventgrid.azure.net
	Port    int    `json:"port"` // 443 (MQTT over WebSockets)
	Path    string `json:"path"` // WebSocket path; Event Grid uses /mqtt

	Cert CertConfig `json:"cert"`

	KeepAliveSeconds      int    `json:"keepAliveSeconds"`      // MQTT keep-alive
	SessionExpiryMinutes  int    `json:"sessionExpiryMinutes"`  // how long the broker keeps our session (and queued commands) while we're offline
	ConnectTimeoutSeconds int    `json:"connectTimeoutSeconds"` // per connection attempt
	ChirpMinutes          int    `json:"chirpMinutes"`          // while unreachable, log CONTROL/unreachable this often
	StatusMinutes         int    `json:"statusMinutes"`         // republish status this often
	MaxInboundKB          int    `json:"maxInboundKB"`          // largest message we accept
	TLSMaxVersion         string `json:"tlsMaxVersion"`         // "1.2", "1.3" or "" (automatic)

	Update UpdateConfig `json:"update"`
}

// UpdateConfig controls updates delivered as signed release manifests. These are
// install-time settings: a cloud config can't widen where downloads may come from
// or whose code signature is accepted.
type UpdateConfig struct {
	Enabled             bool     `json:"enabled"`
	AllowedHosts        []string `json:"allowedHosts"`        // download hosts, e.g. exmonreleases.blob.core.windows.net
	MaxSizeMB           int      `json:"maxSizeMB"`           // refuse larger downloads
	HealthMinutes       int      `json:"healthMinutes"`       // the new version must be healthy within this, or it is rolled back
	PublisherCN         string   `json:"publisherCN"`         // the code-signing certificate's common name the exe must carry
	RequireAuthenticode bool     `json:"requireAuthenticode"` // false only for testing unsigned builds (the manifest signature and hash still apply)
}

// CertConfig selects the client certificate in the machine's Personal store.
// The certificate's common name is the device's identity on the broker.
type CertConfig struct {
	Store     string `json:"store"`     // "LocalMachine" (default) or "CurrentUser" (testing)
	SubjectCN string `json:"subjectCN"` // exact CN to use; "" = any certificate from IssuerCN
	IssuerCN  string `json:"issuerCN"`  // CN of the issuing CA; "" = any issuer
}

// DefaultConfig returns the built-in settings (control plane off).
func DefaultConfig() Config {
	return Config{
		Enabled:               false,
		Port:                  443,
		Path:                  "/mqtt",
		Cert:                  CertConfig{Store: "LocalMachine"},
		KeepAliveSeconds:      60,
		SessionExpiryMinutes:  480,
		ConnectTimeoutSeconds: 30,
		ChirpMinutes:          15,
		StatusMinutes:         15,
		MaxInboundKB:          256,
		Update:                UpdateConfig{MaxSizeMB: 64, HealthMinutes: 10, RequireAuthenticode: true},
	}
}

// Validate checks the settings.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Host == "" {
		return errors.New("control.host must be set, e.g. exmon-prod.eastus-1.ts.eventgrid.azure.net")
	}
	if strings.ContainsAny(c.Host, "/:@ ") || net.ParseIP(c.Host) != nil {
		return fmt.Errorf("control.host %q must be a plain host name (no scheme, port or path)", c.Host)
	}
	if !strings.HasPrefix(c.Path, "/") {
		return fmt.Errorf("control.path %q must start with /", c.Path)
	}
	if c.Cert.SubjectCN == "" && c.Cert.IssuerCN == "" {
		return errors.New("control.cert needs subjectCN or issuerCN, so exmon picks the right certificate")
	}
	switch strings.ToLower(c.Cert.Store) {
	case "localmachine", "currentuser", "":
	default:
		return fmt.Errorf("control.cert.store %q must be LocalMachine or CurrentUser", c.Cert.Store)
	}
	switch c.TLSMaxVersion {
	case "", "1.2", "1.3":
	default:
		return fmt.Errorf(`control.tlsMaxVersion %q must be "1.2", "1.3" or ""`, c.TLSMaxVersion)
	}
	if err := c.Update.validate(); err != nil {
		return err
	}
	return limits.First(
		limits.Range("control.port", c.Port, 1, 65535),
		limits.Range("control.keepAliveSeconds", c.KeepAliveSeconds, 30, 1160),
		limits.Range("control.sessionExpiryMinutes", c.SessionExpiryMinutes, 1, 480),
		limits.Range("control.connectTimeoutSeconds", c.ConnectTimeoutSeconds, 5, 120),
		limits.Range("control.chirpMinutes", c.ChirpMinutes, 1, 1440),
		limits.Range("control.statusMinutes", c.StatusMinutes, 1, 1440),
		limits.Range("control.maxInboundKB", c.MaxInboundKB, 16, 1024),
	)
}

func (u UpdateConfig) validate() error {
	if !u.Enabled {
		return nil
	}
	if len(u.AllowedHosts) == 0 {
		return errors.New("control.update.allowedHosts must list the download host(s)")
	}
	for _, h := range u.AllowedHosts {
		if h == "" || strings.ContainsAny(h, "/:@ *") {
			return fmt.Errorf("control.update.allowedHosts entry %q must be a plain host name", h)
		}
	}
	if u.RequireAuthenticode && u.PublisherCN == "" {
		return errors.New("control.update.publisherCN must name the code-signing certificate (or set requireAuthenticode false for unsigned test builds)")
	}
	return limits.First(
		limits.Count("control.update.allowedHosts", len(u.AllowedHosts), 5),
		limits.Range("control.update.maxSizeMB", u.MaxSizeMB, 8, 256),
		limits.Range("control.update.healthMinutes", u.HealthMinutes, 5, 60),
	)
}

// Topics for a device.
func statusTopic(id string) string { return "exmon/v1/device/" + id + "/status" }
func outboxTopic(id string) string { return "exmon/v1/device/" + id + "/outbox" }
func inboxFilter(id string) string { return "exmon/v1/device/" + id + "/inbox/#" }

// inboxKind returns what follows ".../inbox/" in a topic: "config", "release", "cmd".
func inboxKind(id, topic string) (string, bool) {
	return strings.CutPrefix(topic, "exmon/v1/device/"+id+"/inbox/")
}
