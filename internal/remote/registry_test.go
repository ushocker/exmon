package remote

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"exmon/internal/event"
)

func outboxRemote(name, dir string) Config {
	settings, _ := json.Marshal(map[string]string{"path": filepath.Join(dir, name+".jsonl")})
	c := Config{Name: name, Type: "outbox", Settings: settings, SpoolDir: filepath.Join(dir, "spool", name)}
	c.ApplyDefaults()
	return c
}

func TestRemotesValidate(t *testing.T) {
	dir := t.TempDir()
	good := Remotes{outboxRemote("a", dir), outboxRemote("b", dir)}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid list: %v", err)
	}

	dup := Remotes{outboxRemote("a", dir), outboxRemote("a", t.TempDir())}
	unknown := outboxRemote("c", dir)
	unknown.Type = "carrier-pigeon"
	badName := outboxRemote("Bad_Name", dir)
	overlap := outboxRemote("d", dir)
	overlap.SpoolDir = filepath.Join(dir, "spool") // contains a's and b's spools
	noSettings := outboxRemote("e", dir)
	noSettings.Settings = nil
	misspelt := outboxRemote("f", dir)
	misspelt.Settings = json.RawMessage(`{"paht": "x"}`)
	tooBig := outboxRemote("g", dir)
	tooBig.BatchSize = 5000

	tests := []struct {
		name string
		list Remotes
		want string
	}{
		{"duplicate name", dup, "more than one"},
		{"unknown type", Remotes{unknown}, "unknown type"},
		{"bad name", Remotes{badName}, "lowercase"},
		{"overlapping spools", Remotes{outboxRemote("a", dir), overlap}, "overlap"},
		{"missing settings", Remotes{noSettings}, "settings"},
		{"misspelt setting", Remotes{misspelt}, "unknown field"},
		{"out of range", Remotes{tooBig}, "remotes[g].batchSize"},
		{"too many", make(Remotes, 9), "remotes"},
	}
	for _, tt := range tests {
		err := tt.list.Validate()
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: got %v, want an error containing %q", tt.name, err, tt.want)
		}
	}
}

func TestApplyDefaults(t *testing.T) {
	c := Config{Name: "law", Type: "outbox"}
	c.ApplyDefaults()
	if c.BatchSize != defaultBatchSize || c.RetryMaxMinutes != defaultRetryMaxMinutes ||
		c.SpoolDir != `%ProgramData%\exmon\remote-spool\law` {
		t.Errorf("defaults not applied: %+v", c)
	}
	if !c.IsEnabled() {
		t.Error("a remote without 'enabled' should be on")
	}
	off := false
	c.Enabled = &off
	if c.IsEnabled() {
		t.Error("enabled=false should turn it off")
	}
}

func TestOutboxOutputs(t *testing.T) {
	c := outboxRemote("a", `C:\x`)
	out, err := c.Outputs()
	if err != nil || len(out) != 2 || out[0] != `C:\x\spool\a` || out[1] != `C:\x\a.jsonl` {
		t.Errorf("outputs = %v, %v", out, err)
	}
}

// TestTwoRemotesSeparate runs two queues off one bus, each with its own selectors
// and spool, and checks each got only its own events.
func TestTwoRemotesSeparate(t *testing.T) {
	dir := t.TempDir()
	a, b := outboxRemote("a", dir), outboxRemote("b", dir)
	a.Include = []Selector{{Kinds: []string{"ACM/*"}}}
	b.Include = []Selector{{Kinds: []string{"INCIDENT/*"}}}
	a.FlushIntervalMs, b.FlushIntervalMs = 100, 100

	bus := event.NewBus()
	sa, sb := &fakeSender{}, &fakeSender{}
	qa, err := Start(a, bus.Subscribe("remote:a", 100, event.Blocking), sa, bus.For("remote"))
	if err != nil {
		t.Fatal(err)
	}
	qb, err := Start(b, bus.Subscribe("remote:b", 100, event.Blocking), sb, bus.For("remote"))
	if err != nil {
		t.Fatal(err)
	}
	bus.For("wifi").Log("ACM/disconnected", "1")
	bus.For("incident").Log("INCIDENT/triggered", "2")
	bus.For("wifi").Log("POLL", "neither")
	time.Sleep(300 * time.Millisecond)
	bus.Close()
	qa.Wait()
	qb.Wait()

	if sa.count() != 1 || sa.got[0].Event.Kind != "ACM/disconnected" {
		t.Errorf("remote a got %+v", sa.got)
	}
	if sb.count() != 1 || sb.got[0].Event.Kind != "INCIDENT/triggered" {
		t.Errorf("remote b got %+v", sb.got)
	}
}

// rejectingSender fails the first batch permanently, then accepts.
type rejectingSender struct {
	mu    sync.Mutex
	calls int
	got   []Record
}

func (r *rejectingSender) Send(_ context.Context, b []Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.calls == 1 {
		return Permanent(errors.New("HTTP 400: bad schema"))
	}
	r.got = append(r.got, b...)
	return nil
}

// captureLogger records log lines.
type captureLogger struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureLogger) Log(kind, detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, kind+" "+detail)
}
func (c *captureLogger) Metrics(string, map[string]float64)        {}
func (c *captureLogger) DeclareMetrics(map[string]event.Direction) {}
func (c *captureLogger) Held() bool                                { return false }
func (c *captureLogger) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

func TestPermanentErrorDropsBatch(t *testing.T) {
	cfg := testCfg(t.TempDir())
	cfg.BatchSize = 1
	bus := event.NewBus()
	s := &rejectingSender{}
	log := &captureLogger{}
	q, err := Start(cfg, bus.Subscribe("remote", 100, event.Blocking), s, log)
	if err != nil {
		t.Fatal(err)
	}
	l := bus.For("wifi")
	l.Log("ACM/disconnected", "poison")
	time.Sleep(200 * time.Millisecond)
	l.Log("ACM/disconnected", "fine")
	time.Sleep(300 * time.Millisecond)
	bus.Close()
	q.Wait()

	if len(s.got) != 1 || s.got[0].Event.Detail != "fine" {
		t.Errorf("after a permanent failure the next batch should go through; got %+v", s.got)
	}
	if !strings.Contains(log.text(), "REMOTE/dropped remote=test count=1") {
		t.Errorf("want a REMOTE/dropped line naming the remote, got:\n%s", log.text())
	}
}

type waitErr struct{ d time.Duration }

func (w waitErr) Error() string             { return "busy" }
func (w waitErr) RetryAfter() time.Duration { return w.d }

func TestRetryHint(t *testing.T) {
	if got := retryHint(waitErr{42 * time.Second}); got != 42*time.Second {
		t.Errorf("hint = %v", got)
	}
	if got := retryHint(errors.New("plain")); got != 0 {
		t.Errorf("plain error hint = %v", got)
	}
	if !IsPermanent(Permanent(errors.New("x"))) || IsPermanent(errors.New("x")) {
		t.Error("IsPermanent")
	}
}

func TestDecodeSettingsStrict(t *testing.T) {
	var s outboxSettings
	if err := DecodeSettings(json.RawMessage(`{"path": "x.jsonl"}`), &s); err != nil || s.Path != "x.jsonl" {
		t.Errorf("valid: %v %+v", err, s)
	}
	for _, bad := range []string{`{"path": "x.jsonl", "pth": "y"}`, `{"path": "x.jsonl"} {}`, ``} {
		if err := DecodeSettings(json.RawMessage(bad), &s); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}
