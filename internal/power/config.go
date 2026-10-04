package power

import (
	"fmt"
	"net/url"

	"exmon/internal/limits"
)

// Config controls how exmon behaves around startup, sleep/hibernate and shutdown.
type Config struct {
	Enabled        bool   `json:"enabled"`
	TestURL        string `json:"testURL"`        // connection test; "" = a default route is enough
	Expect         string `json:"expect"`         // text the test page must contain
	MaxWaitSeconds int    `json:"maxWaitSeconds"` // go live anyway after this long (and log POWER/not_ready)
	SettleSeconds  int    `json:"settleSeconds"`  // extra wait after the network is ready
	CheckSeconds   int    `json:"checkSeconds"`   // how often readiness is checked while waiting
}

// DefaultConfig returns the settings used when nothing else is configured. The
// test page is the one Windows itself uses to decide whether it has internet access.
func DefaultConfig() Config {
	return Config{
		Enabled:        true,
		TestURL:        "http://www.msftconnecttest.com/connecttest.txt",
		Expect:         "Microsoft Connect Test",
		MaxWaitSeconds: 180,
		SettleSeconds:  0,
		CheckSeconds:   5,
	}
}

// Validate checks the settings.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.TestURL != "" {
		if u, err := url.Parse(c.TestURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("power.testURL %q must be an http(s) URL, or empty to skip the test", c.TestURL)
		}
	}
	return limits.First(
		limits.Range("power.maxWaitSeconds", c.MaxWaitSeconds, 10, 3600),
		limits.Range("power.settleSeconds", c.SettleSeconds, 0, 600),
		limits.Range("power.checkSeconds", c.CheckSeconds, 1, 60),
	)
}
