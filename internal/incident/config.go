package incident

import "fmt"

// Config controls the rolling window and incident triggers.
type Config struct {
	Enabled            bool   `json:"enabled"`
	Dir                string `json:"dir"`                // where incident .log.gz files go; %VAR% expanded by the caller
	WindowMinutes      int    `json:"windowMinutes"`      // how much history before the trigger to keep
	PostTriggerSeconds int    `json:"postTriggerSeconds"` // keep recording this long after the trigger
	CooldownSeconds    int    `json:"cooldownSeconds"`    // a rule can't fire again within this time
	MaxIncidents       int    `json:"maxIncidents"`       // files kept per severity unless MaxIncidentsBySeverity says otherwise; 0 = unlimited

	// MaxIncidentsBySeverity sets a separate file limit per severity, e.g.
	// {"1": 500, "3": 100}. JSON object keys are always strings; encoding/json turns
	// them into this Go map's int keys for us. 0 = unlimited.
	MaxIncidentsBySeverity map[int]int `json:"maxIncidentsBySeverity,omitempty"`

	MaxHoldSeconds int    `json:"maxHoldSeconds"` // longest an INCIDENT/hold can keep a capture open
	Device         string `json:"-"`              // device description for the file header (set by main)
	Triggers       []Rule `json:"triggers"`
}

// DefaultConfig returns the settings used when nothing else is configured.
// There are no triggers by default, so nothing fires until you add some.
func DefaultConfig() Config {
	return Config{
		Enabled:            true,
		Dir:                `%ProgramData%\exmon\incidents`,
		WindowMinutes:      10,
		PostTriggerSeconds: 30,
		CooldownSeconds:    300,
		MaxIncidents:       100,
		MaxHoldSeconds:     120,
	}
}

// limitFor is how many files of one severity to keep (0 = unlimited).
func (c Config) limitFor(sev int) int {
	if n, ok := c.MaxIncidentsBySeverity[sev]; ok {
		return n
	}
	return c.MaxIncidents
}

// Validate checks the settings and every trigger rule.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Dir == "" {
		return fmt.Errorf("incident dir must be set")
	}
	if c.WindowMinutes < 1 {
		return fmt.Errorf("incident windowMinutes must be >= 1")
	}
	for sev, n := range c.MaxIncidentsBySeverity {
		if sev < 1 || sev > MaxSeverity || n < 0 {
			return fmt.Errorf("incident maxIncidentsBySeverity: severity %d must be 1-%d and its limit >= 0", sev, MaxSeverity)
		}
	}
	names := map[string]bool{}
	for i, r := range c.Triggers {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("trigger %d: %w", i, err)
		}
		if names[r.Name] {
			return fmt.Errorf("trigger name %q used twice", r.Name)
		}
		names[r.Name] = true
	}
	return nil
}
