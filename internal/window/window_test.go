package window

import (
	"testing"
	"time"

	"exmon/internal/event"
)

func TestPrune(t *testing.T) {
	w := New(time.Minute)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i <= 120; i += 10 { // one event every 10s for 2 minutes
		w.Add(event.Event{Time: t0.Add(time.Duration(i) * time.Second), Detail: "x"})
	}
	snap := w.Snapshot()
	if len(snap) != 7 { // 60s..120s inclusive
		t.Fatalf("len = %d, want 7", len(snap))
	}
	if !snap[0].Time.Equal(t0.Add(60 * time.Second)) {
		t.Errorf("oldest = %v, want +60s", snap[0].Time)
	}

	snap[0].Detail = "changed" // a snapshot is a copy
	if w.Snapshot()[0].Detail != "x" {
		t.Error("snapshot shares memory with the window")
	}
}
