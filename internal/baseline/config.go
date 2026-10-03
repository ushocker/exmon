// Package baseline is the learning mode: it records every event, including the
// metric samples the normal log never shows, and keeps a running summary with
// percentiles and suggested trigger thresholds.
package baseline

import (
	"errors"

	"exmon/internal/eventlog"
	"exmon/internal/limits"
)

// Config controls baseline mode.
type Config struct {
	Enabled        bool   `json:"enabled"`
	Path           string `json:"path"`           // baseline .jsonl file; %VAR% expanded by the caller
	SummaryMinutes int    `json:"summaryMinutes"` // rewrite baseline-summary.txt this often
	MaxSizeMB      int    `json:"maxSizeMB"`
	MaxFiles       int    `json:"maxFiles"`
}

// DefaultConfig returns the settings used when nothing else is configured.
func DefaultConfig() Config {
	return Config{
		Path:           `%ProgramData%\exmon\baseline.jsonl`,
		SummaryMinutes: 15,
		MaxSizeMB:      100,
		MaxFiles:       5,
	}
}

// Validate checks for settings that can't work.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Path == "" {
		return errors.New("baseline path must be set")
	}
	return limits.First(
		limits.Range("baseline.summaryMinutes", c.SummaryMinutes, 1, 1440),
		limits.Range("baseline.maxSizeMB", c.MaxSizeMB, 1, 1024),
		limits.Range("baseline.maxFiles", c.MaxFiles, 1, 50),
	)
}

// SinkConfig is the eventlog.FileSink setup for the baseline file: JSON lines,
// metrics included, nothing echoed to the console.
func (c Config) SinkConfig() eventlog.Config {
	return eventlog.Config{
		Path:            c.Path,
		MaxSizeMB:       c.MaxSizeMB,
		MaxFiles:        c.MaxFiles,
		FlushIntervalMs: 1000,
		Format:          "jsonl",
		IncludeMetrics:  true,
	}
}
