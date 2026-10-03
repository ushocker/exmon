package remote

import (
	"errors"
	"path"
	"strings"

	"exmon/internal/event"
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
	Include             []Selector `json:"include"`             // which events to forward
}

// Selector picks events by source and kind. Kinds are path.Match patterns such as
// "ACM/*". An empty Source or empty Kinds matches everything.
type Selector struct {
	Source string   `json:"source,omitempty"`
	Kinds  []string `json:"kinds,omitempty"`
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
	case c.BatchSize < 1:
		return errors.New("remote batchSize must be >= 1")
	case c.RetryInitialSeconds < 1 || c.RetryMaxMinutes < 1:
		return errors.New("remote retryInitialSeconds and retryMaxMinutes must be >= 1")
	}
	for _, s := range c.Include {
		for _, k := range s.Kinds {
			if _, err := path.Match(k, ""); err != nil {
				return errors.New("remote include: bad pattern " + k)
			}
		}
	}
	return nil
}

// wanted reports whether an event should be forwarded. Measurements and the queue's
// own REMOTE/* status lines are never forwarded.
func (c Config) wanted(e event.Event) bool {
	if e.IsMetrics() || strings.HasPrefix(e.Kind, "REMOTE/") {
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
