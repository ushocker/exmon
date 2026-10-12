package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/windows/registry"

	"exmon/internal/event"
	"exmon/internal/limits"
	"exmon/internal/safepath"
)

// Config is one remote: where a selection of events is sent, with its own on-disk
// queue. exmon can have several (Remotes), for example a local outbox file and
// Azure Monitor, each with different selectors.
type Config struct {
	Name     string          `json:"name"`               // unique, [a-z0-9-]; used in REMOTE/* lines and the default spool folder
	Type     string          `json:"type"`               // sender type: "outbox", "azureMonitor", ... (see registry.go)
	Enabled  *bool           `json:"enabled,omitempty"`  // nil (left out) = enabled
	Settings json.RawMessage `json:"settings,omitempty"` // type-specific settings, checked by the sender type

	SpoolDir            string `json:"spoolDir,omitempty"`            // on-disk queue; default %ProgramData%\exmon\remote-spool\<name>
	SegmentKB           int    `json:"segmentKB,omitempty"`           // start a new spool file at this size
	MaxSpoolMB          int    `json:"maxSpoolMB,omitempty"`          // drop the oldest pending events beyond this
	BatchSize           int    `json:"batchSize,omitempty"`           // events per send
	FlushIntervalMs     int    `json:"flushIntervalMs,omitempty"`     // check the spool at least this often
	RetryInitialSeconds int    `json:"retryInitialSeconds,omitempty"` // first retry delay after a failed send
	RetryMaxMinutes     int    `json:"retryMaxMinutes,omitempty"`     // retry delay doubles up to this, then stays
	SimulateOffline     bool   `json:"simulateOffline,omitempty"`     // testing: every send fails

	Include []Selector `json:"include"` // which events to forward

	Host string `json:"-"` // computer name stamped on each record (set by main)
}

// Remotes is the list of configured remotes (the "remotes" config key).
type Remotes []Config

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

// Built-in defaults for a remote's queue settings. A remote that leaves a setting
// out (or sets it to 0) gets these; see ApplyDefaults.
const (
	defaultSegmentKB           = 1024
	defaultMaxSpoolMB          = 100
	defaultBatchSize           = 50
	defaultFlushIntervalMs     = 5000
	defaultRetryInitialSeconds = 5
	defaultRetryMaxMinutes     = 15
	defaultSpoolRoot           = `%ProgramData%\exmon\remote-spool`
	maxRemotes                 = 8
)

// DefaultRemotes is what runs when the config has no "remotes" key: the local
// outbox file, incidents only.
func DefaultRemotes() Remotes {
	c := Config{
		Name:     "outbox",
		Type:     "outbox",
		Settings: json.RawMessage(`{"path": "%ProgramData%\\exmon\\remote-outbox.jsonl"}`),
		Include:  []Selector{{Kinds: []string{"INCIDENT/*"}}},
	}
	c.ApplyDefaults()
	return Remotes{c}
}

// IsEnabled reports whether this remote runs. Enabled is a pointer (*bool) so that
// "left out" (nil) can mean "on", which a plain bool can't tell apart from false.
func (c Config) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// ApplyDefaults fills in settings left out of the config file. Defaults have to be
// applied here: json.Unmarshal creates list items from scratch, so a list item
// can't inherit pre-set defaults the way the other config sections do.
func (c *Config) ApplyDefaults() {
	// set replaces a zero value with its default; p points at the field to fill.
	set := func(p *int, def int) {
		if *p == 0 {
			*p = def
		}
	}
	set(&c.SegmentKB, defaultSegmentKB)
	set(&c.MaxSpoolMB, defaultMaxSpoolMB)
	set(&c.BatchSize, defaultBatchSize)
	set(&c.FlushIntervalMs, defaultFlushIntervalMs)
	set(&c.RetryInitialSeconds, defaultRetryInitialSeconds)
	set(&c.RetryMaxMinutes, defaultRetryMaxMinutes)
	if c.SpoolDir == "" && c.Name != "" {
		c.SpoolDir = defaultSpoolRoot + `\` + c.Name
	}
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Validate checks every remote and the list as a whole: unique names, at most
// maxRemotes, known types, and spool folders that don't overlap.
func (rs Remotes) Validate() error {
	if err := limits.Count("remotes", len(rs), maxRemotes); err != nil {
		return err
	}
	names := map[string]bool{}
	for i, c := range rs {
		label := fmt.Sprintf("remotes[%d]", i)
		if c.Name != "" {
			label = fmt.Sprintf("remotes[%s]", c.Name)
		}
		if !nameRE.MatchString(c.Name) {
			return fmt.Errorf("%s: name %q must be 1-32 lowercase letters, digits or '-'", label, c.Name)
		}
		if names[c.Name] {
			return fmt.Errorf("%s: name is used by more than one remote", label)
		}
		names[c.Name] = true
		if err := c.validate(label); err != nil {
			return err
		}
	}
	// Two remotes sharing a spool would read each other's events.
	for i := range rs {
		for j := i + 1; j < len(rs); j++ {
			a, b := rs[i].SpoolDir, rs[j].SpoolDir
			if safepath.Within(a, b) || safepath.Within(b, a) {
				return fmt.Errorf("remotes[%s] and remotes[%s]: spoolDir folders overlap (%s, %s)", rs[i].Name, rs[j].Name, a, b)
			}
		}
	}
	return nil
}

func (c Config) validate(label string) error {
	t, ok := Lookup(c.Type)
	if !ok {
		return fmt.Errorf("%s: unknown type %q (known: %s)", label, c.Type, strings.Join(TypeNames(), ", "))
	}
	switch {
	case c.SpoolDir == "":
		return fmt.Errorf("%s: spoolDir must be set", label)
	case c.RetryInitialSeconds < 1 || c.RetryMaxMinutes < 1:
		return fmt.Errorf("%s: retryInitialSeconds and retryMaxMinutes must be >= 1", label)
	}
	if err := limits.First(
		limits.Range(label+".batchSize", c.BatchSize, 1, 1000),
		limits.Range(label+".maxSpoolMB", c.MaxSpoolMB, 1, 1024),
		limits.Range(label+".segmentKB", c.SegmentKB, 64, 16384),
		limits.Range(label+".flushIntervalMs", c.FlushIntervalMs, 100, 60000),
		limits.Range(label+".retryInitialSeconds", c.RetryInitialSeconds, 1, 3600),
		limits.Range(label+".retryMaxMinutes", c.RetryMaxMinutes, 1, 1440),
		limits.Count(label+".include", len(c.Include), 50),
	); err != nil {
		return err
	}
	for _, s := range c.Include {
		if s.SeverityAtLeast < 0 || s.SeverityAtLeast > 5 {
			return fmt.Errorf("%s include: severityAtLeast %d must be 1 (most serious) to 5, or 0 for none", label, s.SeverityAtLeast)
		}
		for _, k := range s.Kinds {
			if _, err := path.Match(k, ""); err != nil {
				return fmt.Errorf("%s include: bad pattern %s", label, k)
			}
		}
	}
	if err := t.Validate(c.Settings); err != nil {
		return fmt.Errorf("%s.settings: %w", label, err)
	}
	return nil
}

// Outputs lists the files and folders this remote writes, for the service's
// output containment check: its spool folder plus whatever its type writes.
func (c Config) Outputs() ([]string, error) {
	out := []string{c.SpoolDir}
	if t, ok := Lookup(c.Type); ok && t.Outputs != nil {
		more, err := t.Outputs(c.Settings)
		if err != nil {
			return nil, err
		}
		out = append(out, more...)
	}
	return out, nil
}

// ExpandPath replaces %VAR% in a path (e.g. %ProgramData%) and cleans it.
func ExpandPath(p string) (string, error) {
	e, err := registry.ExpandString(p)
	if err != nil {
		return "", fmt.Errorf("expanding %q: %w", p, err)
	}
	return filepath.Clean(e), nil
}

// wanted reports whether an event should be forwarded. Measurements, held events
// (from while the machine was starting, waking or shutting down), the queues' own
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

// errSettings is returned by sender types whose settings are missing.
var errSettings = errors.New("settings are required for this type")
