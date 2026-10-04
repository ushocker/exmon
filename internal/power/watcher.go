// Package power holds monitor events back while the machine is starting up, waking
// from sleep or hibernation, or shutting down, so the reconnecting that naturally
// happens then isn't reported as a problem.
//
// While held, events still reach the log (marked [held]) but don't fire triggers,
// aren't forwarded and don't count in baseline statistics (see event.Event.Held).
// Events go live again once the network is ready (a default route plus a passing
// connection test), or after MaxWaitSeconds at the latest, so a real outage after
// waking is still reported.
package power

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"exmon/internal/event"
	"exmon/internal/winsys"
)

// Gate is the part of event.Bus the watcher controls.
type Gate interface {
	Hold()
	Release() uint64
}

// clockJump is how far the wall clock may run ahead of a timer tick before we
// assume the machine was asleep (no notification is sent on some systems).
const clockJump = 30 * time.Second

// Watcher decides when events are held. Create one with New, call Start before the
// monitors start, Run it in a goroutine, and call Shutdown when exmon stops.
type Watcher struct {
	cfg   Config
	gate  Gate
	log   event.Logger // must be an unheld logger (bus.ForUnheld)
	check Checker

	// For tests: the clock, the check interval and the power events.
	now    func() time.Time
	tick   time.Duration
	events <-chan winsys.PowerEvent

	// State, used only by the Run goroutine (and Start before it).
	asleep    bool      // a suspend notification arrived and no resume yet
	waiting   bool      // held, waiting for the network
	reason    string    // why we are waiting: "startup" or "resume"
	since     time.Time // when the wait began
	readyAt   time.Time // network became ready (for the settle time)
	lastCheck string    // last readiness detail, for POWER/not_ready

	// stopping is set by Shutdown (any goroutine), so Run never releases events
	// once exmon is shutting down. atomic.Bool is safe to share between goroutines.
	stopping atomic.Bool
}

// New creates a watcher.
func New(cfg Config, gate Gate, log event.Logger, check Checker) *Watcher {
	return &Watcher{
		cfg: cfg, gate: gate, log: log, check: check,
		now:  time.Now,
		tick: time.Duration(max(1, cfg.CheckSeconds)) * time.Second,
	}
}

// Start holds events until the network is ready. Call it before the monitors
// start, so even their first events are held.
func (w *Watcher) Start() {
	w.beginWait("startup", fmt.Sprintf("exmon starting: holding events until the network is ready (up to %v)", w.maxWait()))
}

// Shutdown holds events from here on: exmon (or Windows) is shutting down, and the
// network going away is expected. It may be called from any goroutine.
func (w *Watcher) Shutdown() {
	w.stopping.Store(true)
	w.gate.Hold()
	w.log.Log("POWER/shutdown", "exmon stopping: holding events")
}

// Run reacts to sleep/wake notifications and checks readiness until ctx ends.
func (w *Watcher) Run(ctx context.Context) {
	if w.events == nil {
		ch := make(chan winsys.PowerEvent, 4)
		pw, err := winsys.WatchPower(ch)
		if err != nil {
			w.log.Log("ERROR", "power: no sleep/wake notifications ("+err.Error()+"); using clock-jump detection only")
		} else {
			defer pw.Stop()
			w.events = ch
		}
	}

	ticker := time.NewTicker(w.tick)
	defer ticker.Stop()
	last := w.now().Round(0) // Round(0) drops Go's monotonic clock reading: we want wall time
	if w.waiting && !w.asleep {
		w.checkReady(ctx) // check straight away: at startup the network is usually already up
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-w.events:
			w.onPowerEvent(ev)
		case <-ticker.C:
			now := w.now().Round(0)
			if gap := now.Sub(last); gap > w.tick+clockJump && !w.asleep && !w.waiting {
				// The clock jumped: the machine slept without telling us.
				w.beginWait("resume", fmt.Sprintf("clock jumped %v: the machine was probably asleep; holding events until the network is ready", gap.Round(time.Second)))
			}
			last = now
			if w.waiting && !w.asleep {
				w.checkReady(ctx)
			}
		}
	}
}

func (w *Watcher) onPowerEvent(ev winsys.PowerEvent) {
	switch ev {
	case winsys.PowerSuspend:
		w.gate.Hold()
		w.asleep, w.waiting = true, false
		w.log.Log("POWER/suspend", "going to sleep or hibernate: holding events")
	case winsys.PowerResume:
		if !w.asleep && w.waiting {
			return // a repeat notification for the same wake
		}
		w.asleep = false
		w.beginWait("resume", fmt.Sprintf("woke up: holding events until the network is ready (up to %v)", w.maxWait()))
	}
}

// beginWait holds events and starts waiting for the network.
func (w *Watcher) beginWait(reason, msg string) {
	w.gate.Hold()
	w.waiting, w.reason, w.since = true, reason, w.now()
	w.readyAt, w.lastCheck = time.Time{}, ""
	kind := "POWER/starting"
	if reason == "resume" {
		kind = "POWER/resume"
	}
	w.log.Log(kind, msg)
}

// checkReady runs one readiness check and goes live if the network is ready (and
// has settled), or if we've waited too long.
func (w *Watcher) checkReady(ctx context.Context) {
	now := w.now()
	ready, detail := w.check(ctx)
	w.lastCheck = detail
	switch {
	case ready:
		if w.readyAt.IsZero() {
			w.readyAt = now
		}
		if now.Sub(w.readyAt) >= time.Duration(w.cfg.SettleSeconds)*time.Second {
			w.goLive("POWER/ready", fmt.Sprintf("network ready %v after %s (%s)", now.Sub(w.since).Round(time.Second), w.reason, detail))
		}
	case now.Sub(w.since) >= w.maxWait():
		w.goLive("POWER/not_ready", fmt.Sprintf("no working network within %v of %s (last check: %s); events are live again", w.maxWait(), w.reason, detail))
	default:
		w.readyAt = time.Time{} // readiness must be continuous through the settle time
	}
}

func (w *Watcher) goLive(kind, msg string) {
	if w.stopping.Load() {
		return // shutting down: stay held
	}
	held := w.gate.Release()
	w.waiting = false
	w.log.Log(kind, fmt.Sprintf("%s; %d events were held", msg, held))
}

func (w *Watcher) maxWait() time.Duration {
	return time.Duration(w.cfg.MaxWaitSeconds) * time.Second
}
