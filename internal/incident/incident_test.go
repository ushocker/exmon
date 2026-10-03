package incident

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"exmon/internal/event"
)

func TestBreached(t *testing.T) {
	tests := []struct {
		op   string
		v    float64
		want bool
	}{
		{"<", -76, true}, {"<", -75, false},
		{"<=", -75, true}, {"<=", -74, false},
		{">", 41, true}, {">", 40, false},
		{">=", 40, true}, {">=", 39, false},
	}
	for _, tt := range tests {
		th := -75.0
		if tt.op[0] == '>' {
			th = 40
		}
		r := Rule{Op: tt.op, Threshold: th}
		if got := r.Breached(tt.v); got != tt.want {
			t.Errorf("%g %s %g = %v, want %v", tt.v, tt.op, th, got, tt.want)
		}
	}
}

func TestValidate(t *testing.T) {
	good := Rule{Name: "a", Source: "wifi", Metric: "rssi_dbm", Op: "<"}
	if err := good.Validate(); err != nil {
		t.Errorf("good rule: %v", err)
	}
	bad := good
	bad.Op = "=="
	if bad.Validate() == nil {
		t.Error("op == should fail")
	}
	cfg := DefaultConfig()
	cfg.Triggers = []Rule{good, good}
	if cfg.Validate() == nil {
		t.Error("duplicate names should fail")
	}
}

// harness wires an engine to a real bus and records everything published.
type harness struct {
	bus  *event.Bus
	eng  *Engine
	dir  string
	mu   sync.Mutex
	seen []event.Event
	rec  sync.WaitGroup
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	h := &harness{bus: event.NewBus(), dir: t.TempDir()}
	cfg.Dir = h.dir
	recorder := h.bus.Subscribe("rec", 1000, event.Blocking)
	eng, err := Start(cfg, h.bus.Subscribe("incident", 1000, event.Blocking), h.bus)
	if err != nil {
		t.Fatal(err)
	}
	h.eng = eng
	h.rec.Add(1)
	go func() {
		defer h.rec.Done()
		for e := range recorder {
			h.mu.Lock()
			h.seen = append(h.seen, e)
			h.mu.Unlock()
		}
	}()
	return h
}

func (h *harness) stop() {
	h.eng.Flush()
	h.bus.Close()
	h.eng.Wait()
	h.rec.Wait()
}

func (h *harness) kinds() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var k []string
	for _, e := range h.seen {
		if !e.IsMetrics() {
			k = append(k, e.Kind)
		}
	}
	return k
}

func (h *harness) waitFor(t *testing.T, kind string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, k := range h.kinds() {
			if k == kind {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; saw %v", kind, h.kinds())
}

func readGz(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func testConfig() Config {
	cfg := DefaultConfig()
	cfg.PostTriggerSeconds = 1
	cfg.Triggers = []Rule{{Name: "weak", Source: "wifi", Metric: "rssi_dbm", Op: "<", Threshold: -75, ForSamples: 2}}
	return cfg
}

func TestTriggerSavesPreAndPostLines(t *testing.T) {
	h := newHarness(t, testConfig())
	wifi := h.bus.For("wifi")
	other := h.bus.For("other")

	wifi.Log("POLL", "before-1")
	other.Log("THING", "before-2") // the window covers all sources
	wifi.Metrics("nic", map[string]float64{"rssi_dbm": -80})
	wifi.Metrics("nic", map[string]float64{"rssi_dbm": -60}) // streak resets
	wifi.Metrics("nic", map[string]float64{"rssi_dbm": -80})
	if len(h.kinds()) > 2 {
		t.Fatal("fired without 2 consecutive samples")
	}
	wifi.Metrics("nic", map[string]float64{"rssi_dbm": -81}) // second in a row: fire
	h.waitFor(t, "INCIDENT/triggered")
	wifi.Log("POLL", "after-1")
	h.waitFor(t, "INCIDENT/saved")
	h.stop()

	files, _ := filepath.Glob(filepath.Join(h.dir, "incident-*-weak.log.gz"))
	if len(files) != 1 {
		t.Fatalf("got %d incident files, want 1", len(files))
	}
	text := readGz(t, files[0])
	for _, want := range []string{"# rule:      weak", "value:     -81", "before-1", "before-2", "INCIDENT/triggered", "after-1"} {
		if !strings.Contains(text, want) {
			t.Errorf("incident file missing %q:\n%s", want, text)
		}
	}
}

func TestCooldown(t *testing.T) {
	cfg := testConfig()
	cfg.Triggers[0].ForSamples = 1
	cfg.PostTriggerSeconds = 0
	h := newHarness(t, cfg)
	wifi := h.bus.For("wifi")
	wifi.Metrics("nic", map[string]float64{"rssi_dbm": -80})
	wifi.Metrics("nic", map[string]float64{"rssi_dbm": -80})
	wifi.Metrics("nic", map[string]float64{"rssi_dbm": -80})
	h.waitFor(t, "INCIDENT/saved")
	h.stop()

	n := 0
	for _, k := range h.kinds() {
		if k == "INCIDENT/triggered" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("triggered %d times, want 1 (cooldown)", n)
	}
}

func TestShutdownFlushesOpenCapture(t *testing.T) {
	cfg := testConfig()
	cfg.Triggers[0].ForSamples = 1
	cfg.PostTriggerSeconds = 3600 // would never finish on its own
	h := newHarness(t, cfg)
	h.bus.For("wifi").Metrics("nic", map[string]float64{"rssi_dbm": -90})
	h.waitFor(t, "INCIDENT/triggered")
	h.stop()

	files, _ := filepath.Glob(filepath.Join(h.dir, "*.log.gz"))
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	found := false
	for _, k := range h.kinds() {
		found = found || k == "INCIDENT/saved"
	}
	if !found {
		t.Error("INCIDENT/saved was not published before the bus closed")
	}
}

func TestSubjectFilter(t *testing.T) {
	r := Rule{Source: "wifi", Subject: "nic1"}
	if !r.matches("wifi", "nic1") || r.matches("wifi", "nic2") || r.matches("lan", "nic1") {
		t.Error("subject/source filter wrong")
	}
	if !(Rule{Source: "wifi"}).matches("wifi", "anything") {
		t.Error("empty subject should match any")
	}
}
