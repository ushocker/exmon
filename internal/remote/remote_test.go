package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"exmon/internal/event"
)

func rec(i int) Record {
	return Record{ID: fmt.Sprint(i), Event: event.Event{Source: "t", Kind: "K", Detail: fmt.Sprint(i)}}
}

func TestSpoolAcrossSegmentsAndReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSpool(dir, 1, 100) // 1 KB segments: forces several files
	if err != nil {
		t.Fatal(err)
	}
	var recs []Record
	for i := 0; i < 50; i++ {
		recs = append(recs, rec(i))
	}
	if _, err := s.Append(recs); err != nil {
		t.Fatal(err)
	}
	if len(s.segments()) < 3 {
		t.Fatalf("expected several segments, got %v", s.segments())
	}

	got, next, _ := s.Peek(20)
	if len(got) != 20 || got[0].ID != "0" || got[19].ID != "19" {
		t.Fatalf("peek returned %d records", len(got))
	}
	if err := s.Commit(next); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// "Restart": a new Spool on the same directory resumes at the cursor.
	s2, err := OpenSpool(dir, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if n, _ := s2.Pending(); n != 30 {
		t.Fatalf("pending after reopen = %d, want 30", n)
	}
	got, next, _ = s2.Peek(100)
	if got[0].ID != "20" || got[len(got)-1].ID != "49" {
		t.Errorf("resumed at %s..%s, want 20..49", got[0].ID, got[len(got)-1].ID)
	}
	s2.Commit(next)
	if segs := s2.segments(); len(segs) != 1 {
		t.Errorf("fully sent segments not deleted: %v", segs)
	}
}

func TestTornTailIsTruncated(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenSpool(dir, 1024, 100)
	s.Append([]Record{rec(1)})
	s.Close()
	// Simulate a crash mid-write.
	f, _ := os.OpenFile(filepath.Join(dir, "00000001.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"id":"2","ev`)
	f.Close()

	s2, _ := OpenSpool(dir, 1024, 100)
	s2.Append([]Record{rec(3)})
	got, _, _ := s2.Peek(10)
	s2.Close()
	if len(got) != 2 || got[0].ID != "1" || got[1].ID != "3" || s2.BadLines() != 0 {
		t.Errorf("got %v (bad lines %d), want records 1 and 3", got, s2.BadLines())
	}
}

func TestOverflowDropsOldest(t *testing.T) {
	s, _ := OpenSpool(t.TempDir(), 64, 1) // 64 KB segments, 1 MB cap
	defer s.Close()
	big := Record{Event: event.Event{Detail: string(make([]byte, 1000))}}
	total := 0
	for i := 0; i < 2000; i++ { // ~2 MB
		big.ID = fmt.Sprint(i)
		d, err := s.Append([]Record{big})
		if err != nil {
			t.Fatal(err)
		}
		total += d
	}
	pending, _ := s.Pending()
	if total == 0 || pending+total != 2000 {
		t.Errorf("dropped %d + pending %d != 2000", total, pending)
	}
	got, _, _ := s.Peek(1)
	if got[0].ID != fmt.Sprint(total) {
		t.Errorf("oldest pending = %s, want %d", got[0].ID, total)
	}
}

func TestBackoff(t *testing.T) {
	b := Backoff{Initial: 5 * time.Second, Max: time.Minute}
	want := []time.Duration{5, 10, 20, 40, 60, 60}
	for i, w := range want {
		w *= time.Second
		got := b.Next()
		if got < w*9/10 || got > w*11/10 {
			t.Errorf("step %d = %v, want %v ±10%%", i, got, w)
		}
	}
	b.Reset()
	if got := b.Next(); got > 6*time.Second {
		t.Errorf("after reset = %v, want ~5s", got)
	}
}

func TestWanted(t *testing.T) {
	cfg := Config{Include: []Selector{{Source: "wifi", Kinds: []string{"ACM/*"}}, {Kinds: []string{"INCIDENT/*"}}}}
	tests := []struct {
		e    event.Event
		want bool
	}{
		{event.Event{Source: "wifi", Kind: "ACM/disconnected"}, true},
		{event.Event{Source: "wifi", Kind: "POLL"}, false},
		{event.Event{Source: "lan", Kind: "ACM/x"}, false},
		{event.Event{Source: "incident", Kind: "INCIDENT/saved"}, true},
		{event.Event{Source: "wifi", Kind: event.KindMetrics}, false},
		{event.Event{Source: "remote", Kind: "REMOTE/send_failed"}, false},
	}
	for _, tt := range tests {
		if got := cfg.wanted(tt.e); got != tt.want {
			t.Errorf("%s/%s: got %v, want %v", tt.e.Source, tt.e.Kind, got, tt.want)
		}
	}
}

// fakeSender records what it is sent and can be switched offline.
type fakeSender struct {
	mu      sync.Mutex
	got     []Record
	offline atomic.Bool
}

func (f *fakeSender) Send(_ context.Context, b []Record) error {
	if f.offline.Load() {
		return errors.New("offline")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, b...)
	return nil
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func testCfg(dir string) Config {
	cfg := DefaultConfig()
	cfg.SpoolDir = dir
	cfg.FlushIntervalMs = 100
	cfg.Include = []Selector{{Kinds: []string{"ACM/*"}}}
	return cfg
}

func run(t *testing.T, cfg Config, sender Sender, publish func(event.Logger)) {
	t.Helper()
	bus := event.NewBus()
	q, err := Start(cfg, bus.Subscribe("remote", 100, event.Blocking), sender, bus.For("remote"))
	if err != nil {
		t.Fatal(err)
	}
	publish(bus.For("wifi"))
	time.Sleep(300 * time.Millisecond)
	bus.Close()
	q.Wait()
}

func TestOfflineThenRestartRedelivers(t *testing.T) {
	dir := t.TempDir()
	cfg := testCfg(dir)

	offline := &fakeSender{}
	offline.offline.Store(true)
	run(t, cfg, offline, func(l event.Logger) {
		l.Log("ACM/disconnected", "1")
		l.Log("POLL", "not forwarded")
		l.Log("ACM/connection_complete", "2")
	})
	if offline.count() != 0 {
		t.Fatal("offline sender should have received nothing")
	}

	// "Restart" with the network back: the two spooled events are delivered.
	online := &fakeSender{}
	run(t, cfg, online, func(l event.Logger) { l.Log("ACM/disconnected", "3") })
	if online.count() != 3 {
		t.Fatalf("delivered %d, want 3", online.count())
	}
	if online.got[0].Event.Detail != "1" || online.got[2].Event.Detail != "3" {
		t.Errorf("wrong order: %+v", online.got)
	}
	if online.got[0].ID == "" || online.got[0].ID == online.got[1].ID {
		t.Error("records need unique IDs")
	}
}

func TestOutboxSender(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "out.jsonl")
	s := OutboxSender{Path: p}
	if err := s.Send(context.Background(), []Record{rec(1), rec(2)}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if n := len(b); n == 0 || b[n-1] != '\n' {
		t.Errorf("outbox content %q", b)
	}
}
