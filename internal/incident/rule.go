package incident

import (
	"errors"
	"fmt"
)

// Rule fires when a metric crosses a threshold.
type Rule struct {
	Name       string  `json:"name"`
	Source     string  `json:"source"`            // e.g. "wifi"
	Subject    string  `json:"subject,omitempty"` // only this subject (e.g. one adapter); "" = any
	Metric     string  `json:"metric"`            // e.g. "rssi_dbm"
	Op         string  `json:"op"`                // "<", "<=", ">", ">="
	Threshold  float64 `json:"threshold"`
	ForSamples int     `json:"forSamples,omitempty"` // consecutive breaching samples needed; default 1
}

// Validate checks that the rule can be evaluated.
func (r Rule) Validate() error {
	switch {
	case r.Name == "":
		return errors.New("name must be set")
	case r.Source == "":
		return fmt.Errorf("%s: source must be set", r.Name)
	case r.Metric == "":
		return fmt.Errorf("%s: metric must be set", r.Name)
	}
	switch r.Op {
	case "<", "<=", ">", ">=":
	default:
		return fmt.Errorf("%s: op %q must be one of < <= > >=", r.Name, r.Op)
	}
	return nil
}

// Breached reports whether v crosses the threshold.
func (r Rule) Breached(v float64) bool {
	switch r.Op {
	case "<":
		return v < r.Threshold
	case "<=":
		return v <= r.Threshold
	case ">":
		return v > r.Threshold
	case ">=":
		return v >= r.Threshold
	}
	return false
}

// samplesNeeded is ForSamples, with 0 meaning 1.
func (r Rule) samplesNeeded() int { return max(1, r.ForSamples) }

// matches reports whether the rule applies to this source and subject.
func (r Rule) matches(source, subject string) bool {
	return r.Source == source && (r.Subject == "" || r.Subject == subject)
}

func (r Rule) String() string {
	return fmt.Sprintf("%s %s %s %g", r.Source, r.Metric, r.Op, r.Threshold)
}
