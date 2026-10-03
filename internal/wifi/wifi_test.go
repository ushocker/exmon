package wifi

import (
	"strings"
	"testing"
	"time"

	"exmon/internal/event"
	"exmon/internal/wlan"
)

func TestRateChanged(t *testing.T) {
	tests := []struct {
		prev, cur, pct int
		want           bool
	}{
		{100, 100, 50, false},
		{100, 149, 50, false},
		{100, 150, 50, true},
		{100, 50, 50, true},
		{100, 51, 50, false},
		{0, 10, 50, true},
		{100, 1000, 0, false}, // 0 disables rate checks
	}
	for _, tt := range tests {
		if got := rateChanged(tt.prev, tt.cur, tt.pct); got != tt.want {
			t.Errorf("rateChanged(%d, %d, %d) = %v, want %v", tt.prev, tt.cur, tt.pct, got, tt.want)
		}
	}
}

func TestHasChanged(t *testing.T) {
	cfg := DefaultConfig()
	base := ifaceSample{Key: "a", Sig: "connected|x", RSSI: -60, HasRSSI: true, Rx: 100, Tx: 100}
	ref := base
	ref.RefRetryPct, ref.HasRefRetry = 0, true // last valid retry% was 0
	last := map[string]ifaceSample{"a": ref}

	tests := []struct {
		name   string
		modify func(s *ifaceSample)
		want   bool
	}{
		{"identical", func(s *ifaceSample) {}, false},
		{"signature", func(s *ifaceSample) { s.Sig = "connected|y" }, true},
		{"rssi below threshold", func(s *ifaceSample) { s.RSSI = -62 }, false},
		{"rssi at threshold", func(s *ifaceSample) { s.RSSI = -63 }, true},
		{"rx swing", func(s *ifaceSample) { s.Rx = 200 }, true},
		{"handshake failure", func(s *ifaceSample) { s.HandshakeFails = 1 }, true},
		{"retry pct jump", func(s *ifaceSample) { s.RetryValid = true; s.RetryPct = 15 }, true},
		{"retry pct small", func(s *ifaceSample) { s.RetryValid = true; s.RetryPct = 5 }, false},
		{"retry pct jump, too few frames", func(s *ifaceSample) { s.RetryValid = false; s.RetryPct = 570 }, false},
		{"new interface key", func(s *ifaceSample) { s.Key = "b" }, true},
	}
	for _, tt := range tests {
		s := base
		tt.modify(&s)
		if got := hasChanged([]ifaceSample{s}, last, cfg); got != tt.want {
			t.Errorf("%s: hasChanged = %v, want %v", tt.name, got, tt.want)
		}
	}

	if !hasChanged([]ifaceSample{base, {Key: "b"}}, last, cfg) {
		t.Error("an extra interface should count as a change")
	}
}

func TestStatDeltasWithCounterReset(t *testing.T) {
	prev := ifaceSample{Key: "a", Stats: &wlan.Counters{TxFrames: 1000, Retry: 100, RxFrames: 500, FCSErr: 0}}
	// TxFrames went down: the adapter reset, so the delta is the new value itself.
	cur := ifaceSample{Key: "a", Desc: "nic", Stats: &wlan.Counters{TxFrames: 200, Retry: 150, RxFrames: 600, FCSErr: 100}}

	samples := []ifaceSample{cur}
	computeStatDeltas(samples, map[string]ifaceSample{"a": prev}, 200, nil)
	got := samples[0]
	if !got.RetryValid || got.FCSValid != true { // tx=200 and rx+fcs=200 both meet the minimum
		t.Errorf("RetryValid=%v FCSValid=%v, want both true", got.RetryValid, got.FCSValid)
	}

	// tx delta = 200 (reset), retry delta = 50 -> 25%
	if got.RetryPct != 25 {
		t.Errorf("RetryPct = %v, want 25", got.RetryPct)
	}
	// rx delta = 100, fcs delta = 100 -> 100 / 200 = 50%
	if got.FCSPct != 50 {
		t.Errorf("FCSPct = %v, want 50", got.FCSPct)
	}
	want := "[nic] tx=200 rx=100 retry=50 (25.0%) multi_retry=0 tx_fail=0 ack_fail=0 rts_fail=0 fcs_err=100 (50.0%) handshake_fail=0 decrypt_fail=0"
	if got.StatsText != want {
		t.Errorf("StatsText =\n %q\nwant\n %q", got.StatsText, want)
	}
}

func TestStatDeltasNeedBaseline(t *testing.T) {
	samples := []ifaceSample{{Key: "a", Stats: &wlan.Counters{TxFrames: 5}}}
	computeStatDeltas(samples, map[string]ifaceSample{}, 500, nil)
	if samples[0].StatsText != "" {
		t.Error("first sample should not produce a STATS line")
	}
}

// fakeLogger records metrics for tests.
type fakeLogger struct {
	metrics []map[string]float64
	lines   []string
}

func (f *fakeLogger) Log(kind, detail string)                   { f.lines = append(f.lines, kind+" "+detail) }
func (f *fakeLogger) DeclareMetrics(map[string]event.Direction) {}
func (f *fakeLogger) Metrics(_ string, m map[string]float64)    { f.metrics = append(f.metrics, m) }

func TestReportMetricsPerPollDeltas(t *testing.T) {
	p := newPoller(DefaultConfig())
	log := &fakeLogger{}
	s := ifaceSample{Key: "a", Connected: true, HasRSSI: true, RSSI: -60, Rx: 100, Tx: 200,
		Stats: &wlan.Counters{TxFrames: 100, Retry: 10}}
	p.reportMetrics([]ifaceSample{s, {Key: "b"}}, log)   // b is not connected: no metrics
	s.Stats = &wlan.Counters{TxFrames: 1100, Retry: 510} // 1000 frames, 500 retried
	p.reportMetrics([]ifaceSample{s}, log)
	s.Stats = &wlan.Counters{TxFrames: 1200, Retry: 520} // only 100 frames: below minStatsFrames
	p.reportMetrics([]ifaceSample{s}, log)

	if len(log.metrics) != 3 {
		t.Fatalf("got %d metric events, want 3", len(log.metrics))
	}
	if _, ok := log.metrics[0]["retry_pct"]; ok {
		t.Error("first poll has no previous counters, so no retry_pct")
	}
	if got := log.metrics[1]["retry_pct"]; got != 50 { // 500 retries / 1000 frames since the previous poll
		t.Errorf("retry_pct = %v, want 50", got)
	}
	if log.metrics[1]["rssi_dbm"] != -60 {
		t.Error("rssi_dbm missing")
	}
	if _, ok := log.metrics[2]["retry_pct"]; ok {
		t.Error("retry_pct should be omitted below minStatsFrames")
	}
	if _, ok := log.metrics[2]["handshake_fail"]; !ok {
		t.Error("handshake_fail is a count, not a percentage, so it is always reported")
	}
}

func TestErrorPctThresholdZeroDisables(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ErrorPctThreshold = 0
	prev := ifaceSample{Key: "a", HasRefRetry: true, RefRetryPct: 0}
	cur := ifaceSample{Key: "a", RetryValid: true, RetryPct: 90}
	if hasChanged([]ifaceSample{cur}, map[string]ifaceSample{"a": prev}, cfg) {
		t.Error("threshold 0 should ignore retry/FCS entirely")
	}
}

func TestInconsistentCountersDisableRetry(t *testing.T) {
	p := newPoller(DefaultConfig())
	log := &fakeLogger{}
	s := ifaceSample{Key: "a", Desc: "nic", Connected: true, Stats: &wlan.Counters{TxFrames: 1000, Retry: 0}}
	p.checkConsistency([]ifaceSample{s}, log)
	p.reportMetrics([]ifaceSample{s}, log)

	s.Stats = &wlan.Counters{TxFrames: 1020, Retry: 57} // 57 retried frames out of 20 sent: impossible
	p.checkConsistency([]ifaceSample{s}, log)
	if !p.inconsistent["a"] || len(log.lines) != 1 || !strings.Contains(log.lines[0], "inconsistent") {
		t.Fatalf("expected one inconsistency warning, got %q", log.lines)
	}
	p.checkConsistency([]ifaceSample{s}, log)
	if len(log.lines) != 1 {
		t.Error("the warning should only be logged once")
	}

	// From now on, even a large, plausible interval is not trusted.
	samples := []ifaceSample{{Key: "a", Desc: "nic", Stats: &wlan.Counters{TxFrames: 50000, Retry: 1000}}}
	computeStatDeltas(samples, map[string]ifaceSample{"a": {Stats: &wlan.Counters{}}}, 500, p.inconsistent)
	if samples[0].RetryValid || !strings.Contains(samples[0].StatsText, "counters inconsistent") {
		t.Errorf("RetryValid=%v text=%q", samples[0].RetryValid, samples[0].StatsText)
	}
}

func TestReferenceCarriedForward(t *testing.T) {
	// A valid sample sets the reference; a later noisy one keeps it.
	valid := withReference(ifaceSample{RetryValid: true, RetryPct: 20}, ifaceSample{})
	noisy := withReference(ifaceSample{RetryValid: false, RetryPct: 570}, valid)
	if !noisy.HasRefRetry || noisy.RefRetryPct != 20 {
		t.Errorf("reference = %v (has %v), want 20", noisy.RefRetryPct, noisy.HasRefRetry)
	}
	// So the next valid sample is compared with 20, not 570.
	cfg := DefaultConfig()
	next := ifaceSample{Key: "a", RetryValid: true, RetryPct: 25}
	noisy.Key = "a"
	if hasChanged([]ifaceSample{next}, map[string]ifaceSample{"a": noisy}, cfg) {
		t.Error("25% vs reference 20% is under the 10-point threshold")
	}
}

func TestLowFrameStatsNote(t *testing.T) {
	samples := []ifaceSample{{Key: "a", Desc: "nic", Stats: &wlan.Counters{TxFrames: 20, Retry: 5}}}
	computeStatDeltas(samples, map[string]ifaceSample{"a": {Stats: &wlan.Counters{}}}, 500, nil)
	if samples[0].RetryValid || !strings.Contains(samples[0].StatsText, "retry% ignored: tx < 500 frames") {
		t.Errorf("RetryValid=%v text=%q", samples[0].RetryValid, samples[0].StatsText)
	}
}

func TestHeartbeatDue(t *testing.T) {
	cfg := DefaultConfig() // 5s polls, 15s heartbeat
	p := newPoller(cfg)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p.lastLogged = t0
	tests := []struct {
		after time.Duration
		want  bool
	}{
		{5 * time.Second, false},
		{10 * time.Second, false},
		{14998 * time.Millisecond, true}, // the third poll, a hair early, still counts
		{15 * time.Second, true},
	}
	for _, tt := range tests {
		if got := p.heartbeatDue(t0.Add(tt.after)); got != tt.want {
			t.Errorf("after %v: due = %v, want %v", tt.after, got, tt.want)
		}
	}
	p.cfg.HeartbeatSeconds = 0
	if p.heartbeatDue(t0.Add(time.Hour)) {
		t.Error("heartbeat 0 should be off")
	}
}

func TestHeartbeatKeepsChangeReference(t *testing.T) {
	prev := ifaceSample{Key: "a", RSSI: -60, HasRSSI: true, Rx: 100,
		Stats: &wlan.Counters{TxFrames: 1}, RefRetryPct: 20, HasRefRetry: true}
	cur := ifaceSample{Key: "a", RSSI: -62, HasRSSI: true, Rx: 100,
		Stats: &wlan.Counters{TxFrames: 900}, RetryValid: true, RetryPct: 28}

	got := heartbeatBaseline(cur, prev)
	if got.RSSI != -60 || got.RefRetryPct != 20 {
		t.Errorf("references moved: RSSI=%d ref=%v, want -60 and 20", got.RSSI, got.RefRetryPct)
	}
	if got.Stats.TxFrames != 900 {
		t.Error("frame counters should advance so the next STATS covers the time since the heartbeat")
	}
	// A further 1 dB drift (-63 vs -60) is now a 3 dB change and gets logged.
	next := ifaceSample{Key: "a", RSSI: -63, HasRSSI: true, Rx: 100}
	if !hasChanged([]ifaceSample{next}, map[string]ifaceSample{"a": got}, DefaultConfig()) {
		t.Error("slow drift across a heartbeat should still trip the RSSI threshold")
	}
}
