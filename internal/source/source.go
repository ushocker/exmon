// Package source defines what every monitor (Wi-Fi today, more later) looks like.
package source

import (
	"context"

	"exmon/internal/event"
)

// Source is one monitor. main starts each enabled Source in its own goroutine.
type Source interface {
	// Name is a short label used in log lines about the source itself.
	Name() string

	// Run monitors until ctx is cancelled, writing lines to log. It returns nil on a
	// normal shutdown and an error only if it cannot continue at all.
	//
	// A context.Context carries a cancellation signal: when main presses "stop",
	// ctx.Done() is closed and every goroutine watching it can wind down.
	Run(ctx context.Context, log event.Logger) error
}
