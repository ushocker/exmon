package remote

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"exmon/internal/event"

	"exmon/internal/limits"
)

// Config controls the remote queue.
type Config struct {
	Enabled             bool       `json:"enabled"`
	Endpoint            string     `json:"endpoint"`            // TODO: where the real sender will deliver to; unused for now
	Outbox              string     `json:"outbox"`              // stand-in sender writes here; %VAR% expanded by the caller
	SpoolDir            string     `json:"spoolDir"`            // on-disk queue; %VAR% expanded by the caller
	SegmentKB           int        `json:"segmentKB"`           // start a new spool file at this size
	MaxSpoolMB          int        `json:"maxSpoolMB"`          // drop the oldest pending events beyond this
	BatchSize           int        `json:"batchSize"`           // events per send
	FlushIntervalMs     int        `json:"flushIntervalMs"`     // check the spool at least this often
	RetryInitialSeconds int        `json:"retryInitialSeconds"` // first retry delay after a failed send
	RetryMaxMinutes     int        `json:"retryMaxMinutes"`     // retry delay doubles up to this, then stays
	SimulateOffline     bool       `json:"simulateOffline"`     // testing: every send fails
	Host                string     `json:"-"`                   // computer name stamped on each record (set by main)
	Include             []Selector `json:"include"`             // which events to forward
}

// Selector picks events by source and kind. Kinds are path.Match patterns such as
// "ACM/*". An empty Source or empty Kinds matches everything.
//
// SeverityAtLeast N (1-5, 1 = most serious) also requires the event to carry a
// severity of N or more serious: 2 matches Sev1 and Sev2. Events without a severity
// (everything except incidents) never match such a selector. 0 = no severity filter.
type Selector struct {
	Source          string   `json:"source,omitempty"`
	Kinds           []string `json:"kinds,omitempty"`
	SeverityAtLeast int      `json:"severityAtLeast,omitempty"`
}

// DefaultConfig returns the settings used when nothing else is configured.
func DefaultConfig() Config {
	return Config{
		Enabled:             true,
		Outbox:              `%ProgramData%\exmon\remote-outbox.jsonl`,
		SpoolDir:            `%ProgramData%\exmon\remote-spool`,
		SegmentKB:           1024,
		MaxSpoolMB:          100,
		BatchSize:           50,
		FlushIntervalMs:     5000,
		RetryInitialSeconds: 5,
		RetryMaxMinutes:     15,
		Include:             []Selector{{Kinds: []string{"INCIDENT/*"}}},
	}
}

// Validate checks for settings that can't work.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	switch {
	case c.SpoolDir == "":
		return errors.New("remote spoolDir must be set")
	case c.RetryInitialSeconds < 1 || c.RetryMaxMinutes < 1:
		return errors.New("remote retryInitialSeconds and retryMaxMinutes must be >= 1")
	}
	if err := limits.First(
		limits.Range("remote.batchSize", c.BatchSize, 1, 1000),
		limits.Range("remote.maxSpoolMB", c.MaxSpoolMB, 1, 1024),
		limits.Range("remote.segmentKB", c.SegmentKB, 64, 16384),
	); err != nil {
		return err
	}
	for _, s := range c.Include {
		if s.SeverityAtLeast < 0 || s.SeverityAtLeast > 5 {
			return fmt.Errorf("remote include: severityAtLeast %d must be 1 (most serious) to 5, or 0 for none", s.SeverityAtLeast)
		}
		for _, k := range s.Kinds {
			if _, err := path.Match(k, ""); err != nil {
				return errors.New("remote include: bad pattern " + k)
			}
		}
	}
	return nil
}

// wanted reports whether an event should be forwarded. Measurements, held events
// (from while the machine was starting, waking or shutting down), the queue's own
// REMOTE/* status lines and the internal INCIDENT/hold and /release bookkeeping are
// never forwarded.
func (c Config) wanted(e event.Event) bool {
	if e.IsMetrics() || e.Held || strings.HasPrefix(e.Kind, "REMOTE/") ||
		e.Kind == "INCIDENT/hold" || e.Kind == "INCIDENT/release" {
		return false
	}
	for _, s := range c.Include {
		if s.matches(e) {
			return true
		}
	}
	return false
}

func (s Selector) matches(e event.Event) bool {
	if s.Source != "" && s.Source != e.Source {
		return false
	}
	if s.SeverityAtLeast > 0 && (e.Severity < 1 || e.Severity > s.SeverityAtLeast) {
		return false
	}
	if len(s.Kinds) == 0 {
		return true
	}
	for _, k := range s.Kinds {
		if ok, _ := path.Match(k, e.Kind); ok {
			return true
		}
	}
	return false
}
