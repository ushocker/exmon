// Package event is the common currency of exmon: sources produce Events, and the
// Bus hands a copy of each one to every consumer (log file, incident engine,
// remote queue, baseline recorder).
package event

import "time"

// KindMetrics marks a measurement-only event. These are never written to the normal
// log; the incident engine checks them against triggers and the baseline records them.
const KindMetrics = "METRICS"

// Event is one thing that happened. The `json` tags are used by the baseline file
// and the remote queue; `omitempty` leaves a field out when it is empty.
type Event struct {
	Time    time.Time          `json:"time"`
	Source  string             `json:"source"`            // which monitor: "wifi", "incident", ...
	Kind    string             `json:"kind"`              // "POLL", "ACM/disconnected", "METRICS", ...
	Subject string             `json:"subject,omitempty"` // instance within the source, e.g. the adapter name
	Detail  string             `json:"detail,omitempty"`
	Metrics map[string]float64 `json:"metrics,omitempty"`

	// Severity is set on incident events: 1 = most serious ... 5 = least. 0 = none.
	Severity int `json:"severity,omitempty"`

	// Held marks an event from a monitor while the machine is starting up, waking
	// from sleep or shutting down, before the network is known to work. Held events
	// are still written to the log (marked [held]) but don't fire triggers, aren't
	// forwarded remotely and don't count in baseline statistics.
	Held bool `json:"held,omitempty"`
}

// IsMetrics reports whether e is a measurement rather than a log line.
func (e Event) IsMetrics() bool { return e.Kind == KindMetrics }

// Direction says which way a metric goes when things get worse. The baseline
// summary uses it to suggest trigger thresholds.
type Direction int

const (
	DirectionUnknown Direction = iota // iota counts up from 0 in a const block: 0, 1, 2
	LowIsBad                          // e.g. signal strength
	HighIsBad                         // e.g. retry percentage
)

// Logger is what a source uses to report. Get one from Bus.For(sourceName).
type Logger interface {
	// Log writes a line to the log file (and to any other consumer that wants it).
	Log(kind, detail string)

	// Metrics reports measurements for one subject. They are not written to the
	// normal log; triggers and the baseline use them.
	Metrics(subject string, m map[string]float64)

	// DeclareMetrics tells consumers which direction is "bad" for each metric this
	// source reports. Call it once, before reporting metrics.
	DeclareMetrics(dirs map[string]Direction)

	// Held reports whether events are currently being held back (the machine is
	// starting, waking from sleep or shutting down; see Bus.Hold). Sources that
	// report state changes use it to re-announce their state once events are live.
	Held() bool
}
