package eventlog

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
