package probe

import (
	"fmt"
	"net/url"

	"exmon/internal/limits"
)

// Endpoint is a host checked with a TCP connect and ICMP pings.
type Endpoint struct {
	Name   string `json:"name"`
	Target string `json:"target"` // host name or IPv4 address
	Port   int    `json:"port"`
}

// HTTPTest is a URL fetched with full phase timing.
type HTTPTest struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// PathTraceConfig controls traceroute-style path traces to the endpoints.
type PathTraceConfig struct {
	IntervalMinutes int  `json:"intervalMinutes"` // periodic trace; 0 = off
	OnIncident      bool `json:"onIncident"`      // also trace when an incident triggers
	MaxHops         int  `json:"maxHops"`
}

// Config controls the connectivity probes.
type Config struct {
	Enabled         bool            `json:"enabled"`
	IntervalSeconds int             `json:"intervalSeconds"` // one round of probes this often
	TimeoutSeconds  int             `json:"timeoutSeconds"`  // per probe
	PingCount       int             `json:"pingCount"`
	DNSNames        []string        `json:"dnsNames"`
	Endpoints       []Endpoint      `json:"endpoints"`
	HTTPTests       []HTTPTest      `json:"httpTests"`
	PathTrace       PathTraceConfig `json:"pathTrace"`
}

// DefaultConfig returns the settings used when nothing else is configured. With no
// endpoints or URLs, only the DNS and default-gateway probes run.
func DefaultConfig() Config {
	return Config{
		Enabled:         true,
		IntervalSeconds: 300,
		TimeoutSeconds:  10,
		PingCount:       4,
		DNSNames:        []string{"www.microsoft.com"},
		PathTrace:       PathTraceConfig{IntervalMinutes: 30, OnIncident: true, MaxHops: 30},
	}
}

// Validate checks for settings that can't work.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if err := limits.First(
		limits.Range("probe.intervalSeconds", c.IntervalSeconds, 10, 86400),
		limits.Range("probe.timeoutSeconds", c.TimeoutSeconds, 1, 60),
		limits.Range("probe.pingCount", c.PingCount, 1, 10),
		limits.Range("probe.pathTrace.maxHops", c.PathTrace.MaxHops, 1, 64),
		limits.Count("probe.endpoints", len(c.Endpoints), 25),
		limits.Count("probe.httpTests", len(c.HTTPTests), 25),
		limits.Count("probe.dnsNames", len(c.DNSNames), 10),
	); err != nil {
		return err
	}
	for _, e := range c.Endpoints {
		if e.Name == "" || e.Target == "" || e.Port < 1 || e.Port > 65535 {
			return fmt.Errorf("probe endpoint %q needs name, target and a port 1-65535", e.Name)
		}
	}
	for _, h := range c.HTTPTests {
		if u, err := url.Parse(h.URL); h.Name == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("probe httpTest %q needs a name and an http(s) url", h.Name)
		}
	}
	return nil
}
