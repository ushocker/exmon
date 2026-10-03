// Package remote forwards selected events to a remote target through a durable,
// on-disk queue. Events are written to disk before any send is tried, so a network
// outage followed by a reboot loses nothing: pending events are sent on the next start.
package remote

import (
	"context"
	"fmt"
	"time"

	"exmon/internal/event"
)

const (
	sendTimeout     = 30 * time.Second
	shutdownTimeout = 5 * time.Second
	maxAppendBatch  = 500
)

// Queue moves events from the bus to the spool (appender goroutine) and from the
// spool to the Sender (sender goroutine).
type Queue struct {
	cfg    Config
	spool  *Spool
	sender Sender
	log    event.Logger
	events <-chan event.Event

	wake     chan struct{} // appender -> sender: "there is something new"
	appended chan struct{} // closed when the appender has spooled everything and stopped
	done     chan struct{} // closed when the sender has stopped
}

// Start opens the spool and launches both goroutines. Records left over from a
// previous run are sent first.
func Start(cfg Config, events <-chan event.Event, sender Sender, log event.Logger) (*Queue, error) {
	spool, err := OpenSpool(cfg.SpoolDir, cfg.SegmentKB, cfg.MaxSpoolMB)
	if err != nil {
		return nil, err
	}
	pending, err := spool.Pending()
	if err != nil {
		spool.Close()
		return nil, err
	}
	q := &Queue{
		cfg:      cfg,
		spool:    spool,
		sender:   sender,
		log:      log,
		events:   events,
		wake:     make(chan struct{}, 1),
		appended: make(chan struct{}),
		done:     make(chan struct{}),
	}
	go q.appender()
	go q.senderLoop(pending)
	return q, nil
}

// Wait blocks until both goroutines have stopped (after Bus.Close). Anything not
// delivered stays in the spool for the next start.
func (q *Queue) Wait() { <-q.done }

// appender reads the bus subscription and writes wanted events to the spool.
// It must not call q.log.Log directly (it is the reader of its own channel; see the
// rule on event.Bus), so it logs from a new goroutine instead.
func (q *Queue) appender() {
	defer close(q.appended)
	for e := range q.events {
		var recs []Record
		add := func(e event.Event) { // a closure: a small function that can use the variables around it
			if q.cfg.wanted(e) {
				recs = append(recs, Record{ID: newID(), Event: e})
			}
		}
		add(e)
		for len(q.events) > 0 && len(recs) < maxAppendBatch { // grab whatever else is waiting
			add(<-q.events)
		}
		if len(recs) == 0 {
			continue
		}

		dropped, err := q.spool.Append(recs)
		if err != nil {
			go q.log.Log("REMOTE/spool_error", fmt.Sprintf("lost=%d error=%q", len(recs), err.Error()))
		}
		if dropped > 0 {
			go q.log.Log("REMOTE/spool_overflow", fmt.Sprintf("dropped=%d (maxSpoolMB=%d)", dropped, q.cfg.MaxSpoolMB))
		}
		select { // non-blocking: if a wake is already pending, one is enough
		case q.wake <- struct{}{}:
		default:
		}
	}
}

func (q *Queue) senderLoop(pending int) {
	defer close(q.done)
	defer q.spool.Close()

	if pending > 0 {
		q.log.Log("REMOTE/resuming", fmt.Sprintf("pending=%d (from a previous run)", pending))
	}
	backoff := Backoff{
		Initial: time.Duration(q.cfg.RetryInitialSeconds) * time.Second,
		Max:     time.Duration(q.cfg.RetryMaxMinutes) * time.Minute,
	}
	ticker := time.NewTicker(time.Duration(max(100, q.cfg.FlushIntervalMs)) * time.Millisecond)
	defer ticker.Stop()
	attempt := 0

	for {
		recs, next, err := q.spool.Peek(q.cfg.BatchSize)
		if err != nil {
			q.log.Log("REMOTE/spool_error", err.Error())
		}
		if len(recs) == 0 {
			select {
			case <-q.appended:
				q.drainBeforeExit() // the last events may have arrived just after our Peek
				return
			case <-q.wake:
			case <-ticker.C:
			}
			continue
		}

		if err = q.send(recs, sendTimeout); err == nil {
			if cerr := q.spool.Commit(next); cerr != nil {
				q.log.Log("REMOTE/spool_error", cerr.Error())
			}
			if attempt > 0 {
				q.log.Log("REMOTE/recovered", fmt.Sprintf("delivered after %d failed attempts", attempt))
			}
			backoff.Reset()
			attempt = 0
			if q.stopping() {
				q.drainBeforeExit()
				return
			}
			continue
		}

		attempt++
		delay := backoff.Next()
		q.log.Log("REMOTE/send_failed", fmt.Sprintf("attempt=%d retry_in=%v error=%q",
			attempt, delay.Round(time.Second), err.Error()))
		// New events do not cut this wait short; only shutdown does.
		select {
		case <-q.appended:
			q.drainBeforeExit()
			return
		case <-time.After(delay):
		}
	}
}

func (q *Queue) send(recs []Record, timeout time.Duration) error {
	// A context with a timeout tells the Sender to give up after that long.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return q.sender.Send(ctx, recs)
}

func (q *Queue) stopping() bool {
	select {
	case <-q.appended:
		return true
	default:
		return false
	}
}

// drainBeforeExit makes one last, short attempt to deliver what is left. Whatever
// isn't delivered stays on disk.
func (q *Queue) drainBeforeExit() {
	deadline := time.Now().Add(shutdownTimeout)
	for time.Now().Before(deadline) {
		recs, next, err := q.spool.Peek(q.cfg.BatchSize)
		if err != nil || len(recs) == 0 {
			return
		}
		if q.send(recs, time.Until(deadline)) != nil {
			return
		}
		q.spool.Commit(next)
	}
}
