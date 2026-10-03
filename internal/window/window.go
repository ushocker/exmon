// Package window keeps the most recent span of events (e.g. the last 10 minutes).
//
// A Window is not safe for use from several goroutines at once; give it a single owner.
package window

import (
	"time"

	"exmon/internal/event"
)

// Window is a rolling buffer of events, oldest first.
type Window struct {
	span  time.Duration
	items []event.Event
}

// New creates a window that keeps events no older than span behind the newest one.
func New(span time.Duration) *Window {
	return &Window{span: span}
}

// Add appends e and drops events that have fallen out of the window.
// Events are expected to arrive roughly in time order.
func (w *Window) Add(e event.Event) {
	w.items = append(w.items, e)
	cutoff := e.Time.Add(-w.span)
	i := 0
	for i < len(w.items) && w.items[i].Time.Before(cutoff) {
		i++
	}
	// Re-slicing drops the front without copying. The old entries are freed the
	// next time append has to grow the underlying array.
	w.items = w.items[i:]
}

// Snapshot returns a copy of the events currently in the window.
func (w *Window) Snapshot() []event.Event {
	return append([]event.Event(nil), w.items...)
}

// Len returns how many events are in the window.
func (w *Window) Len() int { return len(w.items) }
