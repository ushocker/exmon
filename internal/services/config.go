package services

import (
	"fmt"
	"strings"

	"exmon/internal/limits"
)

// Config lists the Windows services to watch.
type Config struct {
	Enabled          bool    `json:"enabled"`
	PollSeconds      int     `json:"pollSeconds"`      // how often the services are checked
	HeartbeatMinutes int     `json:"heartbeatMinutes"` // SERVICE/status for each service at least this often; 0 = only at start
	Watch            []Watch `json:"watch"`
}

// Watch is one service to check.
type Watch struct {
	// Name is the service's key name (what `sc query` and Get-Service -Name use,
	// e.g. "Spooler"), not its display name ("Print Spooler").
	Name string `json:"name"`
	// Expect is the state it should be in: "running" (the default), "stopped",
	// or "any" (just report it, never SERVICE/down).
	Expect string `json:"expect,omitempty"`
}

// The states Expect may name.
const (
	ExpectRunning = "running"
	ExpectStopped = "stopped"
	ExpectAny     = "any"
)

// MaxWatch caps the list; every entry is a few service-manager calls per poll.
const MaxWatch = 50

// DefaultConfig returns the settings used when nothing else is configured: on,
// but with nothing to watch until the config lists services.
func DefaultConfig() Config {
	return Config{Enabled: true, PollSeconds: 30, HeartbeatMinutes: 60}
}

// expect returns the entry's expected state, defaulting to running.
func (w Watch) expect() string {
	if w.Expect == "" {
		return ExpectRunning
	}
	return w.Expect
}

// Validate checks the settings and the list.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	errs := []error{
		limits.Range("services.pollSeconds", c.PollSeconds, 5, 3600),
		limits.Range("services.heartbeatMinutes", c.HeartbeatMinutes, 0, 1440),
		limits.Count("services.watch", len(c.Watch), MaxWatch),
	}
	seen := map[string]bool{}
	for i, w := range c.Watch {
		name := strings.TrimSpace(w.Name)
		switch {
		case name == "":
			errs = append(errs, fmt.Errorf("services.watch[%d]: name is empty", i))
		case name != w.Name:
			// The name is used as written; with spaces around it the service would
			// never be found and always look "not installed".
			errs = append(errs, fmt.Errorf("services.watch[%d]: %q has spaces around it", i, w.Name))
		case len(name) > 256 || strings.ContainsAny(name, `/\`):
			errs = append(errs, fmt.Errorf("services.watch[%d]: %q is not a service name", i, w.Name))
		case seen[strings.ToLower(name)]:
			errs = append(errs, fmt.Errorf("services.watch[%d]: %q is listed twice", i, w.Name))
		}
		seen[strings.ToLower(name)] = true
		switch w.expect() {
		case ExpectRunning, ExpectStopped, ExpectAny:
		default:
			errs = append(errs, fmt.Errorf("services.watch[%d] (%s): expect %q must be running, stopped or any", i, w.Name, w.Expect))
		}
	}
	return limits.First(errs...)
}
