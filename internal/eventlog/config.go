package eventlog

import (
	"errors"

	"exmon/internal/limits"
)

// Config controls where and how log lines are written.
//
// The `json:"..."` text after each field is a struct tag: encoding/json reads it to
// know which JSON key fills the field. `json:"-"` means "never read from JSON".
type Config struct {
	Path            string `json:"path"`            // log file; %VAR% already expanded by the caller
	MaxSizeMB       int    `json:"maxSizeMB"`       // rotate when the file reaches this size; 0 disables rotation
	MaxFiles        int    `json:"maxFiles"`        // rotated copies to keep: name.1.log ... name.N.log
	FlushIntervalMs int    `json:"flushIntervalMs"` // how often buffered lines are forced to disk
	Echo            bool   `json:"-"`               // also print each line to the console
	Format          string `json:"-"`               // "text" (default) or "jsonl" (one JSON event per line)
	IncludeMetrics  bool   `json:"-"`               // also write METRICS events (used by the baseline file)
}

// DefaultConfig returns the settings used when nothing else is configured.
func DefaultConfig() Config {
	return Config{
		Path:            `%ProgramData%\exmon\exmon.log`,
		MaxSizeMB:       50,
		MaxFiles:        5,
		FlushIntervalMs: 1000,
		Echo:            true,
	}
}

// Validate checks the log settings are in a sensible range.
func (c Config) Validate() error {
	if c.Path == "" {
		return errors.New("log path must be set")
	}
	return limits.First(
		limits.Range("log.maxSizeMB", c.MaxSizeMB, 0, 1024),
		limits.Range("log.maxFiles", c.MaxFiles, 1, 50),
		limits.Range("log.flushIntervalMs", c.FlushIntervalMs, 100, 60000),
	)
}
