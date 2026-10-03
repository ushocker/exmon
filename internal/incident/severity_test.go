package incident

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeverityDefaultAndValidation(t *testing.T) {
	r := Rule{Name: "a", Source: "wifi", Metric: "m", Op: "<"}
	if r.severity() != DefaultSeverity {
		t.Errorf("omitted severity = %d, want %d", r.severity(), DefaultSeverity)
	}
	for _, sev := range []int{-1, 6} {
		r.Severity = sev
		if r.Validate() == nil {
			t.Errorf("severity %d should fail validation", sev)
		}
	}
	r.Severity = 1
	if err := r.Validate(); err != nil {
		t.Errorf("severity 1: %v", err)
	}
}

func TestSeverityOnIncident(t *testing.T) {
	cfg := testConfig()
	cfg.Triggers[0].ForSamples = 1
	cfg.Triggers[0].Severity = 1
	cfg.PostTriggerSeconds = 0
	h := newHarness(t, cfg)
	h.bus.For("wifi").Metrics("nic", map[string]float64{"rssi_dbm": -90})
	id := h.triggeredID(t)
	h.waitFor(t, "INCIDENT/saved")
	h.stop()

	if !strings.Contains(id, "-sev1-weak") {
		t.Errorf("id = %q, want ...-sev1-weak", id)
	}
	h.mu.Lock()
	for _, e := range h.seen {
		if e.Kind == "INCIDENT/triggered" || e.Kind == "INCIDENT/saved" {
			if e.Severity != 1 || !strings.Contains(e.Detail, "sev=1") {
				t.Errorf("%s: Severity=%d detail=%q", e.Kind, e.Severity, e.Detail)
			}
		}
	}
	h.mu.Unlock()
	if text := readGz(t, filepath.Join(h.dir, id+".log.gz")); !strings.Contains(text, "# severity:  1") {
		t.Errorf("header missing severity:\n%s", text)
	}
}

func TestFileSeverity(t *testing.T) {
	cases := map[string]int{
		"incident-20261003-104715.253-sev1-weak.log.gz": 1,
		"incident-20261003-104715.253-sev5-x.log.gz":    5,
		"incident-20261002-224001.123-weak.log.gz":      DefaultSeverity, // written before severities
	}
	for name, want := range cases {
		if got := fileSeverity(name); got != want {
			t.Errorf("%s: %d, want %d", name, got, want)
		}
	}
}

// TestRetentionPerSeverity checks that pruning one severity never touches another.
func TestRetentionPerSeverity(t *testing.T) {
	dir := t.TempDir()
	e := &Engine{cfg: Config{Dir: dir, MaxIncidents: 10, MaxIncidentsBySeverity: map[int]int{1: 2, 3: 1}}}
	touch := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	touch("incident-20260101-000001.000-sev1-a.log.gz")
	touch("incident-20260101-000002.000-sev1-a.log.gz")
	touch("incident-20260101-000003.000-sev3-b.log.gz")
	touch("incident-20260101-000004.000-b.log.gz") // legacy name: counts as sev3
	touch("incident-20260101-000005.000-sev3-b.log.gz")
	touch("incident-20260101-000006.000-sev4-c.log.gz")
	touch("incident-20260101-000007.000-sev4-c.log.gz")

	e.prune(3)
	e.prune(4) // falls back to maxIncidents (10): nothing removed

	files, _ := filepath.Glob(filepath.Join(dir, "*.log.gz"))
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	want := []string{
		"incident-20260101-000001.000-sev1-a.log.gz",
		"incident-20260101-000002.000-sev1-a.log.gz",
		"incident-20260101-000005.000-sev3-b.log.gz", // only the newest sev3 kept
		"incident-20260101-000006.000-sev4-c.log.gz",
		"incident-20260101-000007.000-sev4-c.log.gz",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("kept:\n %v\nwant:\n %v", names, want)
	}
}

func TestRetentionValidation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxIncidentsBySeverity = map[int]int{7: 10}
	if cfg.Validate() == nil {
		t.Error("severity key 7 should fail")
	}
	cfg.MaxIncidentsBySeverity = map[int]int{1: -1}
	if cfg.Validate() == nil {
		t.Error("negative limit should fail")
	}
}
