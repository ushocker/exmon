// exmon runs monitors (Wi-Fi today, more later) and fans their events out to the
// log file, the incident engine, the remote queue and the baseline recorder.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"

	"exmon/internal/baseline"
	"exmon/internal/config"
	"exmon/internal/event"
	"exmon/internal/eventlog"
	"exmon/internal/incident"
	"exmon/internal/remote"
	"exmon/internal/source"
	"exmon/internal/wifi"
)

// busBuffer is how many events each consumer can fall behind before Publish waits.
const busBuffer = 4096

func main() {
	opts, err := config.Load(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(opts config.Options) error {
	bus := event.NewBus()

	// Consumers subscribe before anything is published. Each adds a "wait" function
	// to stops; at shutdown they run in reverse order, so the log file goes last.
	var stops []func()
	stopAll := func() {
		for i := len(stops) - 1; i >= 0; i-- {
			stops[i]()
		}
	}

	logCfg := opts.Log
	logCfg.Format = "text"
	fileSink, err := eventlog.StartFileSink(logCfg, bus.Subscribe("log", busBuffer, event.Blocking))
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	stops = append(stops, fileSink.Wait)

	// fail closes the bus and waits for consumers if a later startup step fails.
	fail := func(what string, err error) error {
		bus.Close()
		stopAll()
		return fmt.Errorf("%s: %w", what, err)
	}

	if opts.Baseline.Enabled {
		bsink, err := eventlog.StartFileSink(opts.Baseline.SinkConfig(), bus.Subscribe("baseline", busBuffer, event.Blocking))
		if err != nil {
			return fail("baseline", err)
		}
		stops = append(stops, bsink.Wait)
		summary := baseline.StartSummarizer(opts.Baseline, bus.Subscribe("baseline-summary", busBuffer, event.Blocking), bus)
		stops = append(stops, summary.Wait)
	}

	var engine *incident.Engine
	if opts.Incident.Enabled {
		engine, err = incident.Start(opts.Incident, bus.Subscribe("incident", busBuffer, event.Blocking), bus)
		if err != nil {
			return fail("incident", err)
		}
		stops = append(stops, engine.Wait)
	}

	if opts.Remote.Enabled {
		// TODO: replace OutboxSender with the real remote target (opts.Remote.Endpoint).
		var sender remote.Sender = remote.OutboxSender{Path: opts.Remote.Outbox}
		if opts.Remote.SimulateOffline {
			sender = remote.FailingSender{}
		}
		queue, err := remote.Start(opts.Remote, bus.Subscribe("remote", busBuffer, event.Blocking), sender, bus.For("remote"))
		if err != nil {
			return fail("remote", err)
		}
		stops = append(stops, queue.Wait)
	}

	// The list of monitors. Add new sources here.
	var sources []source.Source
	if opts.WiFi.Enabled {
		sources = append(sources, wifi.New(opts.WiFi))
	}

	log := bus.For("exmon")
	log.Log("MONITOR", fmt.Sprintf("exmon started, logging to %s (Ctrl+C to stop)", fileSink.Path()))
	describe(log, opts)
	if len(sources) == 0 {
		log.Log("MONITOR", "no sources enabled")
	}

	// ctx is cancelled when Ctrl+C is pressed.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// A WaitGroup counts running goroutines; Wait blocks until the count is back to 0.
	var wg sync.WaitGroup
	for _, src := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := src.Run(ctx, bus.For(src.Name())); err != nil {
				log.Log("ERROR", fmt.Sprintf("%s: %v", src.Name(), err))
			}
		}()
	}
	wg.Wait()

	// Shutdown order matters:
	//  1. sources have stopped (above);
	//  2. the incident engine saves open captures while the bus still delivers its
	//     INCIDENT/saved events to the log and the remote spool;
	//  3. the bus closes, and each consumer drains what is queued and stops.
	if engine != nil {
		engine.Flush()
	}
	log.Log("MONITOR", "exmon stopped")
	bus.Close()
	stopAll()
	return nil
}

// describe logs which optional systems are running.
func describe(log event.Logger, o config.Options) {
	if o.Incident.Enabled {
		log.Log("MONITOR", fmt.Sprintf("incidents: %d trigger(s), window %dm + %ds, saving to %s",
			len(o.Incident.Triggers), o.Incident.WindowMinutes, o.Incident.PostTriggerSeconds, o.Incident.Dir))
	}
	if o.Remote.Enabled {
		target := "outbox " + o.Remote.Outbox
		if o.Remote.SimulateOffline {
			target = "SIMULATED OFFLINE"
		}
		log.Log("MONITOR", fmt.Sprintf("remote: %s, spool %s, retry up to every %dm", target, o.Remote.SpoolDir, o.Remote.RetryMaxMinutes))
	}
	if o.Baseline.Enabled {
		log.Log("MONITOR", fmt.Sprintf("baseline: recording to %s, summary every %dm in %s",
			o.Baseline.Path, o.Baseline.SummaryMinutes, baseline.SummaryPath(o.Baseline.Path)))
	}
}
