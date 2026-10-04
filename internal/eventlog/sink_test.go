package eventlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"exmon/internal/event"
)

func testConfig(t *testing.T) Config {
	cfg := DefaultConfig()
	cfg.Path = filepath.Join(t.TempDir(), "test.log")
	cfg.Echo = false
	return cfg
}

// start creates a bus with one FileSink subscribed to it.
func start(t *testing.T, cfg Config) (*event.Bus, *FileSink) {
	t.Helper()
	bus := event.NewBus()
	s, err := StartFileSink(cfg, bus.Subscribe("file", 4096, event.Blocking))
	if err != nil {
		t.Fatal(err)
	}
	return bus, s
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\r\n"), "\r\n")
}

func TestConcurrentLogWritesEveryLineOnce(t *testing.T) {
	bus, s := start(t, testConfig(t))
	const workers, perWorker = 20, 500

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := bus.For("test")
			for i := 0; i < perWorker; i++ {
				l.Log("TEST", fmt.Sprintf("w%d-%d", w, i))
			}
		}()
	}
	wg.Wait()
	bus.Close()
	s.Wait()

	seen := map[string]int{}
	for _, line := range readLines(t, s.Path()) {
		fields := strings.Fields(line)
		seen[fields[len(fields)-1]]++
	}
	if len(seen) != workers*perWorker {
		t.Fatalf("got %d distinct lines, want %d", len(seen), workers*perWorker)
	}
	for k, n := range seen {
		if n != 1 {
			t.Fatalf("line %q written %d times", k, n)
		}
	}
}

func TestRotation(t *testing.T) {
	cfg := testConfig(t)
	cfg.MaxSizeMB = 1
	cfg.MaxFiles = 2
	bus, s := start(t, cfg)
	l := bus.For("test")
	detail := strings.Repeat("x", 1000)
	for i := 0; i < 4000; i++ { // ~4 MB, so it rotates several times
		l.Log("FILL", detail)
	}
	bus.Close()
	s.Wait()

	dir := filepath.Dir(s.Path())
	for _, name := range []string{"test.log", "test.1.log", "test.2.log"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s missing: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "test.3.log")); err == nil {
		t.Error("test.3.log should have been deleted (MaxFiles=2)")
	}
}

func TestTimestampTakenAtPublish(t *testing.T) {
	cfg := testConfig(t)
	cfg.FlushIntervalMs = 3_600_000
	bus, s := start(t, cfg)
	before := time.Now()
	bus.For("test").Log("A", "first")
	time.Sleep(50 * time.Millisecond)
	bus.Close()
	s.Wait()

	line := readLines(t, s.Path())[0]
	at, err := time.ParseInLocation("2006-01-02 15:04:05.000", line[:23], time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if d := at.Sub(before.Truncate(time.Millisecond)); d < 0 || d > 20*time.Millisecond {
		t.Errorf("timestamp %v is %v after Log was called; want close to 0", at, d)
	}
}

func TestMetricsOnlyInJSONL(t *testing.T) {
	text := testConfig(t)
	jsonl := testConfig(t)
	jsonl.Format, jsonl.IncludeMetrics = "jsonl", true

	bus := event.NewBus()
	ts, _ := StartFileSink(text, bus.Subscribe("text", 10, event.Blocking))
	js, _ := StartFileSink(jsonl, bus.Subscribe("jsonl", 10, event.Blocking))
	l := bus.For("wifi")
	l.Log("POLL", "hello")
	l.Metrics("nic", map[string]float64{"rssi_dbm": -50})
	bus.Close()
	ts.Wait()
	js.Wait()

	if lines := readLines(t, ts.Path()); len(lines) != 1 {
		t.Errorf("text file has %d lines, want 1 (no metrics): %q", len(lines), lines)
	}
	lines := readLines(t, js.Path())
	if len(lines) != 2 {
		t.Fatalf("jsonl file has %d lines, want 2", len(lines))
	}
	var e event.Event
	if err := json.Unmarshal([]byte(lines[1]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Kind != event.KindMetrics || e.Metrics["rssi_dbm"] != -50 || e.Subject != "nic" {
		t.Errorf("decoded %+v", e)
	}
}

func TestFormatLine(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.Local)
	got := FormatLine(at, "POLL", "hello")
	want := "2026-01-02 03:04:05.006  POLL                         hello"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestRefusesNonRegularLogPath(t *testing.T) {
	cfg := testConfig(t)
	if err := os.Mkdir(cfg.Path, 0o755); err != nil { // the "log file" is a folder
		t.Fatal(err)
	}
	if _, err := StartFileSink(cfg, event.NewBus().Subscribe("x", 1, event.Blocking)); err == nil {
		t.Error("a log path that is a folder should be refused")
	}
}

// TestRotationOnlyTouchesItsOwnFiles checks rotation renames and deletes only
// <base>.N<ext> in the log's own folder, and leaves everything else alone.
func TestRotationOnlyTouchesItsOwnFiles(t *testing.T) {
	cfg := testConfig(t)
	cfg.MaxSizeMB = 1
	cfg.MaxFiles = 2
	dir := filepath.Dir(cfg.Path)
	bystanders := []string{"notes.txt", "test.log.bak", "test.x.log", "test.10.log", "other.1.log"}
	for _, name := range bystanders {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bus, s := start(t, cfg)
	l := bus.For("test")
	detail := strings.Repeat("x", 1000)
	for i := 0; i < 4000; i++ { // several rotations
		l.Log("FILL", detail)
	}
	bus.Close()
	s.Wait()

	for _, name := range bystanders {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(b) != "keep" {
			t.Errorf("%s was touched by rotation (err %v)", name, err)
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		n := e.Name()
		own := n == "test.log" || n == "test.1.log" || n == "test.2.log"
		bystander := false
		for _, b := range bystanders {
			bystander = bystander || n == b
		}
		if !own && !bystander {
			t.Errorf("unexpected file %s after rotation", n)
		}
	}
}

func TestHeldMarker(t *testing.T) {
	bus, s := start(t, testConfig(t))
	bus.Hold()
	bus.For("wifi").Log("POLL", "while waking")
	bus.Release()
	bus.For("wifi").Log("POLL", "live")
	bus.Close()
	s.Wait()
	lines := readLines(t, s.Path())
	if !strings.HasSuffix(lines[0], "while waking [held]") || strings.HasSuffix(lines[1], "[held]") {
		t.Errorf("lines %q", lines)
	}
}
