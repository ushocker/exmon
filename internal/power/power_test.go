package power

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"exmon/internal/event"
	"exmon/internal/winsys"
)

// fakeGate records Hold/Release.
type fakeGate struct {
	held  atomic.Bool
	count atomic.Uint64
}

func (g *fakeGate) Hold()           { g.held.Store(true) }
func (g *fakeGate) Release() uint64 { g.held.Store(false); return g.count.Swap(0) }

// fakeLog records lines; safe for the Run goroutine and the test to share.
type fakeLog struct {
	mu    sync.Mutex
	lines []string
}

func (f *fakeLog) Log(kind, detail string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lines = append(f.lines, kind+" "+detail)
}
func (f *fakeLog) Metrics(string, map[string]float64)        {}
func (f *fakeLog) DeclareMetrics(map[string]event.Direction) {}
func (f *fakeLog) Held() bool                                { return false }

func (f *fakeLog) has(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// rig is a watcher with fakes and a fast tick.
type rig struct {
	w      *Watcher
	gate   *fakeGate
	log    *fakeLog
	ready  atomic.Bool
	events chan winsys.PowerEvent
	clock  atomic.Int64 // wall clock offset in ns, added to real time
	cancel context.CancelFunc
}

func newRig(t *testing.T, cfg Config) *rig {
	t.Helper()
	r := &rig{gate: &fakeGate{}, log: &fakeLog{}, events: make(chan winsys.PowerEvent, 4)}
	check := func(context.Context) (bool, string) {
		if r.ready.Load() {
			return true, "fake ready"
		}
		return false, "fake not ready"
	}
	r.w = New(cfg, r.gate, r.log, check)
	r.w.tick = 10 * time.Millisecond
	r.w.events = r.events
	r.w.now = func() time.Time { return time.Now().Add(time.Duration(r.clock.Load())) }
	return r
}

func (r *rig) run() {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go r.w.Run(ctx)
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestStartupHoldsUntilReady(t *testing.T) {
	r := newRig(t, DefaultConfig())
	r.w.Start()
	if !r.gate.held.Load() || !r.log.has("POWER/starting") {
		t.Fatal("Start should hold and log POWER/starting")
	}
	r.run()
	defer r.cancel()
	time.Sleep(50 * time.Millisecond)
	if !r.gate.held.Load() {
		t.Fatal("released before the network was ready")
	}
	r.ready.Store(true)
	waitFor(t, func() bool { return !r.gate.held.Load() }, "release")
	if !r.log.has("POWER/ready") {
		t.Error("want a POWER/ready line")
	}
}

func TestSuspendResume(t *testing.T) {
	r := newRig(t, DefaultConfig())
	r.ready.Store(true)
	r.run()
	defer r.cancel()

	r.events <- winsys.PowerSuspend
	waitFor(t, func() bool { return r.gate.held.Load() && r.log.has("POWER/suspend") }, "suspend")
	r.ready.Store(false)
	time.Sleep(50 * time.Millisecond)
	if !r.gate.held.Load() {
		t.Fatal("must stay held while asleep")
	}

	r.events <- winsys.PowerResume
	r.events <- winsys.PowerResume // Windows often sends two for one wake
	waitFor(t, func() bool { return r.log.has("POWER/resume") }, "resume")
	time.Sleep(50 * time.Millisecond)
	if !r.gate.held.Load() {
		t.Fatal("must stay held until the network is ready")
	}
	r.ready.Store(true)
	waitFor(t, func() bool { return !r.gate.held.Load() }, "release after resume")

	r.log.mu.Lock()
	resumes := 0
	for _, l := range r.log.lines {
		if strings.HasPrefix(l, "POWER/resume") {
			resumes++
		}
	}
	r.log.mu.Unlock()
	if resumes != 1 {
		t.Errorf("logged %d POWER/resume lines for one wake, want 1", resumes)
	}
}

func TestMaxWaitGoesLive(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxWaitSeconds = 1
	r := newRig(t, cfg)
	r.w.Start()
	r.run()
	defer r.cancel()
	waitFor(t, func() bool { return !r.gate.held.Load() }, "release after max wait")
	if !r.log.has("POWER/not_ready") {
		t.Error("want POWER/not_ready when the network never became ready")
	}
}

func TestClockJumpCountsAsResume(t *testing.T) {
	r := newRig(t, DefaultConfig())
	r.ready.Store(false)
	r.run()
	defer r.cancel()
	time.Sleep(30 * time.Millisecond)
	r.clock.Add(int64(10 * time.Minute)) // as if the machine slept 10 minutes unnoticed
	waitFor(t, func() bool { return r.gate.held.Load() && r.log.has("POWER/resume clock jumped") }, "clock-jump resume")
}

func TestSettleTime(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SettleSeconds = 1
	r := newRig(t, cfg)
	r.ready.Store(true)
	r.w.Start()
	start := time.Now()
	r.run()
	defer r.cancel()
	waitFor(t, func() bool { return !r.gate.held.Load() }, "release after settle")
	if time.Since(start) < 900*time.Millisecond {
		t.Errorf("released after %v, want at least the 1s settle time", time.Since(start))
	}
}

func TestShutdownStaysHeld(t *testing.T) {
	r := newRig(t, DefaultConfig())
	r.w.Start()
	r.w.Shutdown()
	r.ready.Store(true)
	r.run()
	defer r.cancel()
	time.Sleep(80 * time.Millisecond)
	if !r.gate.held.Load() || !r.log.has("POWER/shutdown") {
		t.Error("after Shutdown, events must stay held even when the network is ready")
	}
}

func TestConnectTest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/portal" {
			w.Write([]byte("<html>Please sign in</html>")) // a captive portal
			return
		}
		w.Write([]byte("Microsoft Connect Test"))
	}))
	defer srv.Close()
	ctx := context.Background()
	if err := connectTest(ctx, srv.URL+"/connecttest.txt", "Microsoft Connect Test"); err != nil {
		t.Errorf("good reply: %v", err)
	}
	if err := connectTest(ctx, srv.URL+"/portal", "Microsoft Connect Test"); err == nil {
		t.Error("a captive portal page must fail the test")
	}
}

func TestValidate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TestURL = "ftp://x"
	if cfg.Validate() == nil {
		t.Error("ftp test URL should fail")
	}
	cfg = DefaultConfig()
	cfg.TestURL = ""
	if err := cfg.Validate(); err != nil {
		t.Errorf("empty test URL means route-only: %v", err)
	}
	cfg.MaxWaitSeconds = 5
	if cfg.Validate() == nil {
		t.Error("maxWaitSeconds below 10 should fail")
	}
}
