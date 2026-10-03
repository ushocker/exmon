package remote

import (
	"testing"

	"exmon/internal/event"
)

func TestSeverityAtLeast(t *testing.T) {
	cfg := Config{Include: []Selector{{Kinds: []string{"INCIDENT/*"}, SeverityAtLeast: 2}}}
	tests := []struct {
		sev  int
		want bool
	}{
		{1, true},  // Sev1 is more serious than 2
		{2, true},  // exactly 2
		{3, false}, // less serious
		{0, false}, // no severity at all
	}
	for _, tt := range tests {
		e := event.Event{Source: "incident", Kind: "INCIDENT/triggered", Severity: tt.sev}
		if got := cfg.wanted(e); got != tt.want {
			t.Errorf("severity %d: wanted=%v, want %v", tt.sev, got, tt.want)
		}
	}

	// A selector without the field is unchanged: severity doesn't matter.
	plain := Config{Include: []Selector{{Kinds: []string{"INCIDENT/*"}}}}
	if !plain.wanted(event.Event{Kind: "INCIDENT/saved", Severity: 5}) || !plain.wanted(event.Event{Kind: "INCIDENT/saved"}) {
		t.Error("selector without severityAtLeast should match any severity")
	}
}

func TestSeverityAtLeastValidation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SpoolDir = "x"
	cfg.Include = []Selector{{SeverityAtLeast: 6}}
	if cfg.Validate() == nil {
		t.Error("severityAtLeast 6 should fail")
	}
}
