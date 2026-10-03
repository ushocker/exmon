// Package incident watches metrics from every source. When a trigger rule fires, it
// saves the recent log lines (the rolling window) plus the lines that follow the
// trigger to a compressed file, and announces the incident on the bus.
package incident

import (
	"fmt"
	"os"
	"sync"
	"time"

	"exmon/internal/event"
	"exmon/internal/window"
)

// Publisher is the part of event.Bus the engine needs. Using a small interface
// here keeps the engine easy to test with a fake.
type Publisher interface {
	Publish(event.Event)
	PublishAsync(event.Event)
}

// SourceName is the source of INCIDENT/* events.
const SourceName = "incident"

// checkInterval is how often open captures are checked for their deadline.
const checkInterval = 250 * time.Millisecond

// capture is an incident that is still recording its post-trigger lines.
type capture struct {
	rule    Rule
	subject string
	value   float64
	at      time.Time     // when the rule fired
	until   time.Time     // stop recording at this time
	lines   []event.Event // window snapshot, then everything after the trigger
	window  time.Duration // for the file header
}

// Engine runs the rolling window and trigger rules. Start one with Start.
type Engine struct {
	cfg    Config
	events <-chan event.Event
	pub    Publisher

	// Used only by the engine goroutine, so no mutex is needed.
	window    *window.Window
	streak    map[string]int       // rule|subject -> consecutive breaching samples
	lastFired map[string]time.Time // rule name -> last trigger time
	captures  []*capture

	flushReq chan chan struct{} // Flush asks the loop to save open captures now
	writers  sync.WaitGroup     // file-writing goroutines still running
	done     chan struct{}
}

// Start launches the engine goroutine. It reads events until the channel is closed.
func Start(cfg Config, events <-chan event.Event, pub Publisher) (*Engine, error) {
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, err
	}
	e := &Engine{
		cfg:       cfg,
		events:    events,
		pub:       pub,
		window:    window.New(time.Duration(cfg.WindowMinutes) * time.Minute),
		streak:    map[string]int{},
		lastFired: map[string]time.Time{},
		flushReq:  make(chan chan struct{}),
		done:      make(chan struct{}),
	}
	go e.loop()
	return e, nil
}

// Flush saves every open capture now (cutting its post-trigger time short) and
// waits until the files are written and announced. Call it before Bus.Close so the
// INCIDENT/saved events still reach the log and the remote queue.
func (e *Engine) Flush() {
	reply := make(chan struct{})
	select {
	case e.flushReq <- reply:
		<-reply
	case <-e.done:
	}
	e.writers.Wait()
}

// Wait blocks until the engine has stopped (after Bus.Close) and all files are written.
func (e *Engine) Wait() { <-e.done }

func (e *Engine) loop() {
	defer close(e.done)
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case ev, ok := <-e.events:
			if !ok { // bus closed: save whatever is still open and stop
				e.saveDue(time.Time{}, true)
				e.writers.Wait()
				return
			}
			e.handle(ev)
		case now := <-ticker.C:
			e.saveDue(now, false)
		case reply := <-e.flushReq:
			e.saveDue(time.Time{}, true)
			close(reply)
		}
	}
}

func (e *Engine) handle(ev event.Event) {
	if ev.IsMetrics() {
		e.evaluate(ev)
		return
	}
	// A log line: it goes into the rolling window and every open capture.
	e.window.Add(ev)
	for _, c := range e.captures {
		c.lines = append(c.lines, ev)
	}
}

// evaluate checks one metrics event against every rule.
func (e *Engine) evaluate(ev event.Event) {
	for _, r := range e.cfg.Triggers {
		if !r.matches(ev.Source, ev.Subject) {
			continue
		}
		v, ok := ev.Metrics[r.Metric] // "comma ok": ok is false if the key is missing
		if !ok {
			continue
		}
		key := r.Name + "|" + ev.Subject
		if !r.Breached(v) {
			e.streak[key] = 0
			continue
		}
		e.streak[key]++
		if e.streak[key] < r.samplesNeeded() {
			continue
		}
		e.streak[key] = 0

		cooldown := time.Duration(e.cfg.CooldownSeconds) * time.Second
		if last, fired := e.lastFired[r.Name]; fired && ev.Time.Sub(last) < cooldown {
			continue
		}
		e.lastFired[r.Name] = ev.Time
		e.fire(r, ev, v)
	}
}

func (e *Engine) fire(r Rule, ev event.Event, v float64) {
	e.captures = append(e.captures, &capture{
		rule:    r,
		subject: ev.Subject,
		value:   v,
		at:      ev.Time,
		until:   ev.Time.Add(time.Duration(e.cfg.PostTriggerSeconds) * time.Second),
		lines:   e.window.Snapshot(),
		window:  time.Duration(e.cfg.WindowMinutes) * time.Minute,
	})
	// Async: we are inside our own receive loop (see the rule on event.Bus).
	// The event comes back to us through the bus and lands in the window and capture.
	e.pub.PublishAsync(event.Event{
		Source:  SourceName,
		Kind:    "INCIDENT/triggered",
		Subject: ev.Subject,
		Detail: fmt.Sprintf("rule=%s source=%s subject=%q metric=%s value=%g op=%s threshold=%g",
			r.Name, r.Source, ev.Subject, r.Metric, v, r.Op, r.Threshold),
	})
}

// saveDue hands captures whose deadline has passed (or all of them, if all is
// true) to writer goroutines, so slow disk I/O never holds up the loop.
func (e *Engine) saveDue(now time.Time, all bool) {
	keep := e.captures[:0] // reuse the same backing array while filtering
	for _, c := range e.captures {
		if !all && now.Before(c.until) {
			keep = append(keep, c)
			continue
		}
		e.writers.Add(1)
		go func() {
			defer e.writers.Done()
			e.save(c)
		}()
	}
	e.captures = keep
}
