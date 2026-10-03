package incident

import "fmt"

// Config controls the rolling window and incident triggers.
type Config struct {
	Enabled            bool   `json:"enabled"`
	Dir                string `json:"dir"`                // where incident .log.gz files go; %VAR% expanded by the caller
	WindowMinutes      int    `json:"windowMinutes"`      // how much history before the trigger to keep
	PostTriggerSeconds int    `json:"postTriggerSeconds"` // keep recording this long after the trigger
	CooldownSeconds    int    `json:"cooldownSeconds"`    // a rule can't fire again within this time
	MaxIncidents       int    `json:"maxIncidents"`       // keep at most this many files; 0 = unlimited
	Triggers           []Rule `json:"triggers"`
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
	}
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
