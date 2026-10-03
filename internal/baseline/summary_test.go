package baseline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"exmon/internal/event"
)

func TestPercentile(t *testing.T) {
	var data []float64
	for i := 1; i <= 100; i++ {
		data = append(data, float64(i))
	}
	for _, tt := range []struct{ p, want float64 }{{5, 5}, {50, 50}, {95, 95}, {99, 99}, {100, 100}} {
		if got := percentile(data, tt.p); got != tt.want {
			t.Errorf("p%g = %g, want %g", tt.p, got, tt.want)
		}
	}
}

func TestReservoirCapsMemory(t *testing.T) {
	var s stat
	for i := 0; i < reservoirSize*3; i++ {
		s.add(float64(i))
	}
	if len(s.sample) != reservoirSize || s.count != reservoirSize*3 || s.min != 0 || s.max != reservoirSize*3-1 {
		t.Errorf("len=%d count=%d min=%g max=%g", len(s.sample), s.count, s.min, s.max)
	}
}

func TestSummaryAndSuggestions(t *testing.T) {
	bus := event.NewBus()
	cfg := DefaultConfig()
	cfg.Path = filepath.Join(t.TempDir(), "baseline.jsonl")
	sum := StartSummarizer(cfg, bus.Subscribe("summary", 1000, event.Blocking), bus)

	wifi := bus.For("wifi")
	wifi.DeclareMetrics(map[string]event.Direction{"rssi_dbm": event.LowIsBad, "retry_pct": event.HighIsBad})
	for i := 1; i <= 100; i++ {
		wifi.Metrics("nic", map[string]float64{"rssi_dbm": float64(-40 - i), "retry_pct": float64(i), "other": 1})
	}
	wifi.Log("ACM/disconnected", "")
	wifi.Log("ACM/disconnected", "")
	bus.Close()
	sum.Wait()

	b, err := os.ReadFile(sum.Path())
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{
		`"metric": "rssi_dbm", "op": "<", "threshold": -136`, // p5 of -41..-140
		`"metric": "retry_pct", "op": ">", "threshold": 99`,
		"ACM/disconnected  2",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("summary missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, `"metric": "other"`) {
		t.Error("no suggestion expected for a metric without a direction")
	}
}
