package remote

import (
	"math/rand/v2"
	"time"
)

// Backoff produces retry delays that double after each failure up to Max:
// 5s, 10s, 20s, 40s, ... Max, Max, Max. Each delay is jittered by ±10% so many
// machines that lost the network at the same moment don't all retry in lockstep.
type Backoff struct {
	Initial time.Duration
	Max     time.Duration
	current time.Duration // 0 means "no failures yet"
}

// Next returns the delay to wait before the next attempt.
func (b *Backoff) Next() time.Duration {
	if b.current == 0 {
		b.current = b.Initial
	} else {
		b.current = min(b.current*2, b.Max)
	}
	jitter := 0.9 + 0.2*rand.Float64() // 0.9 .. 1.1
	return time.Duration(float64(b.current) * jitter)
}

// Reset starts over after a success.
func (b *Backoff) Reset() { b.current = 0 }
