package wifi

import (
	"errors"

	"exmon/internal/limits"
)

// Config controls the Wi-Fi source.
type Config struct {
	Enabled           bool `json:"enabled"`
	PollSeconds       int  `json:"pollSeconds"`       // seconds between samples
	Neighbors         bool `json:"neighbors"`         // also log other BSSIDs with the same SSID
	DebugPoll         bool `json:"debugPoll"`         // log every poll, even when nothing changed
	RSSIThreshold     int  `json:"rssiThreshold"`     // dBm move that counts as a change
	RateChangePercent int  `json:"rateChangePercent"` // rx/tx swing (% of last logged) that counts; 0 ignores rates
	ErrorPctThreshold int  `json:"errorPctThreshold"` // retry%/FCS% move (percentage points) that counts; 0 ignores them
	MinStatsFrames    int  `json:"minStatsFrames"`    // frames needed before a retry%/FCS% is trusted
	HeartbeatSeconds  int  `json:"heartbeatSeconds"`  // write a POLL line at least this often even with no change; 0 = off

	DisconnectWindowMinutes int `json:"disconnectWindowMinutes"` // recent_disconnects counts disconnects this far back
}

// DefaultConfig returns the settings used when nothing else is configured.
func DefaultConfig() Config {
	return Config{
		Enabled:           true,
		PollSeconds:       5,
		RSSIThreshold:     3,
		RateChangePercent: 50,
		ErrorPctThreshold: 10,
		MinStatsFrames:    500,
		HeartbeatSeconds:  15,

		DisconnectWindowMinutes: 30,
	}
}

// Validate checks for settings that can't work.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.DisconnectWindowMinutes < 1 {
		return errors.New("wifi disconnectWindowMinutes must be >= 1")
	}
	return limits.Range("wifi.pollSeconds", c.PollSeconds, 1, 3600)
}
