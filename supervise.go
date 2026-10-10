package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"exmon/internal/config"
	"exmon/internal/event"
)

// session is what a running exmon needs to reload itself: how it was started
// (args, trust check) and a way to ask for a restart, which the control plane
// uses after installing a new cloud config.
type session struct {
	args      []string
	trust     config.TrustFunc
	asService bool
	restart   func(reason string) // set by supervise for each run
}

// healthDelay is how long exmon must run with a new cloud config before it is
// considered good. If it crashes or fails to start before then, the previous
// cloud config is put back.
const healthDelay = 2 * time.Minute

// supervise runs exmon, restarting it in-process when asked (a new cloud
// config) without the service ever leaving the Running state. ready is passed
// on only once. A pending cloud config that keeps failing is reverted.
func supervise(ctx context.Context, sess *session, opts config.Options, ready func(), warn func(string)) error {
	var once sync.Once
	readyOnce := func() { once.Do(ready) }
	pendingPath := config.PendingPath()

	for {
		// A cloud config still on trial gets MaxStarts starts and until its
		// deadline; after that the last-known-good one comes back.
		if note, changed := checkPending(pendingPath); note != "" {
			warn(note)
			opts.ConfigWarnings = append(opts.ConfigWarnings, note)
			if changed {
				reloaded, err := config.LoadWith(sess.args, sess.trust)
				if err != nil {
					return fmt.Errorf("config after reverting the cloud config: %w", err)
				}
				reloaded.ConfigWarnings = append(reloaded.ConfigWarnings, note)
				opts = reloaded
			}
		}

		runCtx, cancel := context.WithCancel(ctx)
		var restart atomic.Bool // atomic: set from the control plane's goroutine, read here
		var reason atomic.Value
		sess.restart = func(why string) {
			reason.Store(why)
			restart.Store(true)
			cancel()
		}
		err := run(runCtx, opts, sess.asService, readyOnce, warn, sess)
		cancel()

		if ctx.Err() != nil { // a real stop: Ctrl+C, service Stop or shutdown
			return err
		}
		if err != nil {
			// Startup failed. If a cloud config is on trial, it is the likely cause:
			// put the previous one back and try again; otherwise give up.
			if p, _ := config.ReadPending(pendingPath); p != nil && p.Kind == "config" {
				warn(fmt.Sprintf("exmon failed to start with cloud config version %d (%v); reverting to the previous one", p.Version, err))
				config.RevertCloud()
				config.WritePending(pendingPath, nil)
			} else {
				return err
			}
		} else if !restart.Load() {
			return nil
		}

		newOpts, lerr := config.LoadWith(sess.args, sess.trust)
		if lerr != nil {
			warn("reloading the config failed (" + lerr.Error() + "); reverting the cloud config")
			config.RevertCloud()
			config.WritePending(pendingPath, nil)
			if newOpts, lerr = config.LoadWith(sess.args, sess.trust); lerr != nil {
				return lerr
			}
		}
		if why, _ := reason.Load().(string); why != "" {
			newOpts.ConfigWarnings = append(newOpts.ConfigWarnings, "restarted: "+why)
		}
		opts = newOpts
	}
}

// checkPending counts a start against a pending cloud config and reverts it if
// it has had too many, or ran out of time. It returns a note for the log, and
// whether the config changed.
func checkPending(path string) (string, bool) {
	p, err := config.ReadPending(path)
	if err != nil || p == nil || p.Kind != "config" {
		return "", false
	}
	p.Starts++
	if p.Starts > config.MaxStarts || time.Now().After(p.Deadline) {
		config.RevertCloud()
		config.WritePending(path, nil)
		return fmt.Sprintf("cloud config version %d didn't run healthily (%d starts); reverted to the previous one", p.Version, p.Starts-1), true
	}
	config.WritePending(path, p)
	return "", false
}

// commitPending marks a pending cloud config as good once exmon has run with it
// for healthDelay. It runs in its own goroutine for each run.
func commitPending(ctx context.Context, log event.Logger) {
	path := config.PendingPath()
	p, err := config.ReadPending(path)
	if err != nil || p == nil || p.Kind != "config" {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(healthDelay):
	}
	if err := config.WritePending(path, nil); err != nil {
		log.Log("CONTROL/state_error", err.Error())
		return
	}
	log.Log("CONTROL/config_committed", fmt.Sprintf("cloud config version %d has run healthily for %s; committed", p.Version, healthDelay))
}
