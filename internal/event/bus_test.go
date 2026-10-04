package event

import "testing"

func TestFanOut(t *testing.T) {
	b := NewBus()
	a := b.Subscribe("a", 10, Blocking)
	c := b.Subscribe("c", 10, Blocking)
	b.For("src").Log("K", "d")
	b.Close()

	for name, ch := range map[string]<-chan Event{"a": a, "c": c} {
		var got []Event
		for e := range ch { // range over a channel reads until it is closed
			got = append(got, e)
		}
		if len(got) != 1 || got[0].Source != "src" || got[0].Kind != "K" || got[0].Time.IsZero() {
			t.Errorf("%s got %+v", name, got)
		}
	}
}

func TestDropWhenFull(t *testing.T) {
	b := NewBus()
	b.Subscribe("slow", 1, DropWhenFull)
	l := b.For("src")
	for i := 0; i < 5; i++ {
		l.Log("K", "d") // must not block even though nobody reads
	}
	if got := b.Dropped("slow"); got != 4 {
		t.Errorf("dropped = %d, want 4", got)
	}
}

func TestPublishAfterClose(t *testing.T) {
	b := NewBus()
	ch := b.Subscribe("a", 1, Blocking)
	b.Close()
	b.For("src").Log("K", "d") // must not panic
	if _, ok := <-ch; ok {
		t.Error("expected closed, empty channel")
	}
}

func TestMetricsAndDirections(t *testing.T) {
	b := NewBus()
	ch := b.Subscribe("a", 1, Blocking)
	l := b.For("wifi")
	l.DeclareMetrics(map[string]Direction{"rssi_dbm": LowIsBad})
	m := map[string]float64{"rssi_dbm": -50}
	l.Metrics("nic", m)
	m["rssi_dbm"] = 0 // the published copy must not change

	e := <-ch
	if !e.IsMetrics() || e.Subject != "nic" || e.Metrics["rssi_dbm"] != -50 {
		t.Errorf("got %+v", e)
	}
	if b.Direction("wifi", "rssi_dbm") != LowIsBad || b.Direction("wifi", "x") != DirectionUnknown {
		t.Error("wrong direction lookup")
	}
}

func TestHold(t *testing.T) {
	b := NewBus()
	ch := b.Subscribe("a", 10, Blocking)
	mon, life := b.For("wifi"), b.ForUnheld("power")

	b.Hold()
	if !mon.Held() || life.Held() {
		t.Error("monitor loggers report held; unheld loggers never do")
	}
	mon.Log("POLL", "during")
	mon.Metrics("nic", map[string]float64{"x": 1})
	life.Log("POWER/resume", "")
	if n := b.Release(); n != 2 {
		t.Errorf("Release counted %d held events, want 2", n)
	}
	mon.Log("POLL", "after")
	b.Close()

	var got []Event
	for e := range ch {
		got = append(got, e)
	}
	want := []bool{true, true, false, false} // POLL, METRICS, POWER, POLL after release
	for i, e := range got {
		if e.Held != want[i] {
			t.Errorf("event %d (%s) Held=%v, want %v", i, e.Kind, e.Held, want[i])
		}
	}
}
