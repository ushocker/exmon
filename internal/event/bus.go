package event

import (
	"maps"
	"sync"
	"sync/atomic"
	"time"
)

// Mode says what Publish does when a subscriber's channel is full.
type Mode int

const (
	Blocking     Mode = iota // wait until there is room: nothing is ever lost
	DropWhenFull             // throw the event away and count it
)

type subscriber struct {
	name    string
	ch      chan Event
	mode    Mode
	dropped atomic.Uint64
}

// Bus fans every published event out to all subscribers.
//
// Rule for consumers: never call Publish from the goroutine that reads your own
// subscription channel. If your channel is full, Publish waits for you to read it,
// and you never would (a deadlock). Use PublishAsync there instead.
type Bus struct {
	// A sync.RWMutex lets many goroutines hold the read lock at once (RLock), but the
	// write lock (Lock) only alone. Publish takes the read lock; Close takes the write
	// lock, so Close waits for any Publish in progress to finish.
	mu     sync.RWMutex
	closed bool
	subs   []*subscriber
	dirs   map[string]map[string]Direction // source -> metric -> direction
}

// NewBus creates an empty bus.
func NewBus() *Bus {
	return &Bus{dirs: map[string]map[string]Direction{}}
}

// Subscribe adds a consumer. Call it before anything is published. The returned
// channel is closed by Close once every event has been delivered.
func (b *Bus) Subscribe(name string, buffer int, mode Mode) <-chan Event {
	s := &subscriber{name: name, ch: make(chan Event, buffer), mode: mode}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs = append(b.subs, s)
	return s.ch
}

// Publish delivers e to every subscriber, stamping the time if it isn't set.
// After Close it does nothing.
func (b *Bus) Publish(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	for _, s := range b.subs {
		if s.mode == Blocking {
			s.ch <- e
			continue
		}
		select {
		case s.ch <- e:
		default:
			s.dropped.Add(1)
		}
	}
}

// PublishAsync stamps the time now and publishes from a new goroutine, so the
// caller never waits. Use it from inside a consumer's receive loop.
func (b *Bus) PublishAsync(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	go b.Publish(e)
}

// Close stops publishing and closes every subscriber channel. Consumers finish
// reading what is already queued, then see the channel closed.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for _, s := range b.subs {
		close(s.ch)
	}
}

// Dropped returns how many events a DropWhenFull subscriber has lost.
func (b *Bus) Dropped(name string) uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, s := range b.subs {
		if s.name == name {
			return s.dropped.Load()
		}
	}
	return 0
}

// Direction returns the declared direction of a source's metric.
func (b *Bus) Direction(source, metric string) Direction {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.dirs[source][metric] // reading a missing map key gives the zero value: DirectionUnknown
}

// For returns a Logger that stamps every event with the given source name.
func (b *Bus) For(source string) Logger {
	return sourceLogger{bus: b, source: source}
}

type sourceLogger struct {
	bus    *Bus
	source string
}

func (l sourceLogger) Log(kind, detail string) {
	l.bus.Publish(Event{Source: l.source, Kind: kind, Detail: detail})
}

func (l sourceLogger) Metrics(subject string, m map[string]float64) {
	// Copy the map: the caller may reuse theirs, and consumers read ours later.
	l.bus.Publish(Event{Source: l.source, Kind: KindMetrics, Subject: subject, Metrics: maps.Clone(m)})
}

func (l sourceLogger) DeclareMetrics(dirs map[string]Direction) {
	l.bus.mu.Lock()
	defer l.bus.mu.Unlock()
	l.bus.dirs[l.source] = maps.Clone(dirs)
}
