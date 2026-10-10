// exmon runs monitors (Wi-Fi, network adapters, VPN, connectivity probes) and fans
// their events out to the log file, the incident engine, the remote queue and the
// baseline recorder.
package main

import (
	"context"
	_ "embed" // enables the //go:embed directive below
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"exmon/internal/baseline"
	"exmon/internal/config"
	"exmon/internal/device"
	"exmon/internal/event"
	"exmon/internal/eventlog"
	"exmon/internal/incident"
	"exmon/internal/nic"
	"exmon/internal/power"
	"exmon/internal/probe"
	"exmon/internal/remote"
	"exmon/internal/service"
	"exmon/internal/services"
	"exmon/internal/signdoc"
	"exmon/internal/source"
	"exmon/internal/version"
	"exmon/internal/vpn"
	"exmon/internal/wifi"
	"exmon/internal/winsys"
)

// busBuffer is how many events each consumer can fall behind before Publish waits.
const busBuffer = 4096

// starterConfig is exmon.example.json, copied into the executable at build time by
// the go:embed directive, so --write-default-config works with just the .exe.
//
//go:embed exmon.example.json
var starterConfig []byte

func main() {
	// Service management subcommands come first: exmon.exe install|uninstall|status.
	if len(os.Args) > 1 {
		if cmd, ok := subcommands[os.Args[1]]; ok {
			if err := cmd(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}

	// Started by the Service Control Manager: run as the exmon service.
	if service.IsService() {
		if err := service.Run(runService); err != nil {
			os.Exit(1)
		}
		return
	}

	// Otherwise: console mode.
	opts, err := config.LoadWith(os.Args[1:], consoleTrust(winsys.IsElevated()))
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	for _, w := range opts.ConfigWarnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	if opts.WriteDefaultConfig != "" {
		if err := config.WriteDefault(opts.WriteDefaultConfig, starterConfig); err != nil {
			fmt.Fprintln(os.Stderr, "write default config:", err)
			os.Exit(1)
		}
		fmt.Println("wrote starter config to", opts.WriteDefaultConfig)
		fmt.Println("edit it (at least the probe endpoints and httpTests), then run exmon")
		return
	}
	// ctx is cancelled when Ctrl+C is pressed.
	// Go delivers Windows shutdown, log-off and console-close as SIGTERM, so those
	// stop exmon cleanly too.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sess := &session{args: os.Args[1:], trust: consoleTrust(winsys.IsElevated())}
	if err := supervise(ctx, sess, opts, func() {}, func(string) {}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// consoleTrust picks the config trust check for console mode. A normal console runs
// with the user's own rights, so any config is fine. An elevated (administrator)
// console must not run a config a standard user could have planted, so it gets the
// same admin-only check as the service.
func consoleTrust(elevated bool) config.TrustFunc {
	if elevated {
		return service.Trust
	}
	return nil
}

// subcommands are the service management commands, chosen by the first argument.
var subcommands = map[string]func(args []string) error{
	"install":   func(args []string) error { return service.Install(args, os.Stdout) },
	"uninstall": func([]string) error { return service.Uninstall(os.Stdout) },
	"status":    func([]string) error { return service.Status(os.Stdout) },
	"enroll":    enrollCmd, // test-device certificates; see enrollcmd.go
	// Prints the version; exmon-helper asks the installed exe this before an update.
	"version": func([]string) error { fmt.Println(version.Version); return nil },
}

// runService is exmon as the Windows Service runs it. The flags come from the
// service's command line (baked in at install). Config files are only used if only
// administrators can change them, and problems before the log file opens are written
// to the Windows event log, since a service has no console.
func runService(ctx context.Context, ready func(), elog service.EventLog) error {
	opts, err := config.LoadWith(os.Args[1:], service.Trust)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// Outputs must stay inside %ProgramData%\exmon or an outputRoots folder, with no
	// junctions or links on the way, so a wrong path can't touch other files.
	if err := opts.CheckOutputs(service.DataDir()); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	opts.Log.Echo = false // nobody sees the console of a service
	warn := func(msg string) { elog.Warning(service.EventWarning, msg) }
	for _, w := range opts.ConfigWarnings {
		warn(w)
	}
	if exe, err := os.Executable(); err == nil {
		if err := service.Trust(exe); err != nil {
			warn("exmon.exe could be replaced by non-administrators: " + err.Error())
		}
	}
	started := func() {
		cfg := opts.ConfigFile
		if cfg == "" {
			cfg = "none (built-in defaults)"
		}
		elog.Info(service.EventStarted, "exmon started; config: "+cfg+"; log: "+opts.Log.Path)
		ready()
	}
	sess := &session{args: os.Args[1:], trust: service.Trust, asService: true}
	return supervise(ctx, sess, opts, started, warn)
}

// run is exmon itself, in console or service mode. It calls ready once the
// monitors have started and returns after ctx is cancelled and everything has shut
// down. warn reports problems somewhere visible (the event log for a service).
// sess (nil in tests) lets the control plane restart exmon after a new cloud config.
func run(ctx context.Context, opts config.Options, asService bool, ready func(), warn func(string), sess *session) error {
	bus := event.NewBus()
	dev := device.Get(asService)
	opts.Incident.Device = func() string { return dev.Refresh(asService).String() }
	for i := range opts.Remotes {
		opts.Remotes[i].Host = dev.Computer
	}

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

	// Path traces: periodic, and on incidents (the tracer holds the incident open).
	if opts.Probe.Enabled && (opts.Probe.PathTrace.IntervalMinutes > 0 || (opts.Probe.PathTrace.OnIncident && opts.Incident.Enabled)) {
		tracer := probe.StartTracer(opts.Probe, bus.Subscribe("pathtrace", busBuffer, event.Blocking), bus.For("pathtrace"))
		stops = append(stops, tracer.Wait)
	}

	// One queue per enabled remote, each with its own subscription and spool, so a
	// slow or offline remote never holds up another.
	for _, rc := range opts.Remotes {
		if !rc.IsEnabled() {
			continue
		}
		sender, err := remote.NewSender(rc, bus.For("remote"))
		if err != nil {
			return fail("remote "+rc.Name, err)
		}
		queue, err := remote.Start(rc, bus.Subscribe("remote:"+rc.Name, busBuffer, event.Blocking), sender, bus.For("remote"))
		if err != nil {
			return fail("remote "+rc.Name, err)
		}
		stops = append(stops, queue.Wait)
	}

	// The list of monitors. Add new sources here.
	var sources []source.Source
	if opts.WiFi.Enabled {
		sources = append(sources, wifi.New(opts.WiFi))
	}
	if opts.NIC.Enabled {
		sources = append(sources, nic.New(opts.NIC))
	}
	if opts.VPN.Enabled {
		sources = append(sources, vpn.New(opts.VPN))
	}
	if opts.Services.Enabled && len(opts.Services.Watch) > 0 {
		sources = append(sources, services.New(opts.Services))
	}
	var prober *probe.Source
	if opts.Probe.Enabled {
		prober = probe.New(opts.Probe)
		sources = append(sources, prober)
	}
	if opts.Control.Enabled {
		ctl, err := newControl(ctx, opts, bus, prober, sess)
		if err != nil {
			return fail("control", err)
		}
		sources = append(sources, ctl)
	}

	// exmon's own lines are never held: they explain what is going on.
	log := bus.ForUnheld("exmon")
	if asService {
		log.Log("MONITOR", fmt.Sprintf("exmon service started, logging to %s", fileSink.Path()))
	} else {
		log.Log("MONITOR", fmt.Sprintf("exmon started, logging to %s (Ctrl+C to stop)", fileSink.Path()))
	}
	for _, w := range opts.ConfigWarnings {
		log.Log("WARNING", w)
	}
	if opts.ConfigFile != "" {
		log.Log("MONITOR", "config: "+opts.ConfigFile)
	} else {
		log.Log("MONITOR", fmt.Sprintf("config: none found (looked in %s); using built-in defaults",
			strings.Join(config.SearchPaths(), ", ")))
	}
	if opts.CloudVersion > 0 {
		log.Log("MONITOR", "cloud config: "+opts.CloudSummary)
	}
	log.Log("DEVICE", dev.String())
	describe(log, opts)
	if len(sources) == 0 {
		log.Log("MONITOR", "no sources enabled")
	}

	// Startup, sleep/wake and shutdown: hold monitor events until the network is
	// ready. Start holds right away, before the monitors produce anything.
	var pw *power.Watcher
	if opts.Power.Enabled {
		pw = power.New(opts.Power, bus, bus.ForUnheld("power"), power.NetworkCheck(opts.Power))
		pw.Start()
		go pw.Run(ctx)
	}

	// A WaitGroup counts running goroutines; Wait blocks until the count is back to 0.
	var wg sync.WaitGroup
	for _, src := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := src.Run(ctx, bus.For(src.Name())); err != nil {
				log.Log("ERROR", fmt.Sprintf("%s: %v", src.Name(), err))
				warn(fmt.Sprintf("%s: %v", src.Name(), err))
			}
		}()
	}
	ready() // monitors are running (each retries on its own if Windows isn't ready yet)
	if sess != nil {
		go commitPending(ctx, bus.ForUnheld("control")) // a new cloud config becomes final after running healthily
	}

	// Wait for Ctrl+C, a service Stop, or Windows shutting down. From then on, hold
	// monitor events: networks going away during shutdown is expected.
	<-ctx.Done()
	if pw != nil {
		pw.Shutdown()
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
	if o.Power.Enabled {
		test := o.Power.TestURL
		if test == "" {
			test = "none (a default route is enough)"
		}
		log.Log("MONITOR", fmt.Sprintf("power: holding events at startup, on wake and at shutdown until the network is ready; test %s; max wait %ds",
			test, o.Power.MaxWaitSeconds))
	}
	if o.Probe.Enabled {
		trace := "off"
		if pt := o.Probe.PathTrace; pt.IntervalMinutes > 0 || pt.OnIncident {
			trace = fmt.Sprintf("every %dm, on incident %v", pt.IntervalMinutes, pt.OnIncident)
		}
		log.Log("MONITOR", fmt.Sprintf("probe: every %ds, %d dns name(s), %d endpoint(s), %d http test(s); path trace %s",
			o.Probe.IntervalSeconds, len(o.Probe.DNSNames), len(o.Probe.Endpoints), len(o.Probe.HTTPTests), trace))
	}
	if o.Services.Enabled && len(o.Services.Watch) > 0 {
		names := make([]string, len(o.Services.Watch))
		for i, w := range o.Services.Watch {
			names[i] = w.Name
		}
		log.Log("MONITOR", fmt.Sprintf("services: %s; every %ds, status every %dm",
			strings.Join(names, ", "), o.Services.PollSeconds, o.Services.HeartbeatMinutes))
	}
	if o.Incident.Enabled {
		log.Log("MONITOR", fmt.Sprintf("incidents: %d trigger(s), window %dm + %ds, saving to %s",
			len(o.Incident.Triggers), o.Incident.WindowMinutes, o.Incident.PostTriggerSeconds, o.Incident.Dir))
	}
	for _, r := range o.Remotes {
		if !r.IsEnabled() {
			continue
		}
		// Describe never prints secrets: each sender type decides what is safe to show.
		target := r.Type + " " + remote.Describe(r)
		if r.SimulateOffline {
			target = r.Type + " SIMULATED OFFLINE"
		}
		log.Log("MONITOR", fmt.Sprintf("remote %s: %s, %d selector(s), spool %s, retry up to every %dm",
			r.Name, target, len(r.Include), r.SpoolDir, r.RetryMaxMinutes))
	}
	if o.Control.Enabled {
		cert := o.Control.Cert.SubjectCN
		if cert == "" {
			cert = "any from " + o.Control.Cert.IssuerCN
		}
		log.Log("MONITOR", fmt.Sprintf("control: wss://%s:%d%s, certificate %s, signing keys %s, chirp every %dm",
			o.Control.Host, o.Control.Port, o.Control.Path, cert, signdoc.KeySetName(), o.Control.ChirpMinutes))
	}
	if o.Baseline.Enabled {
		log.Log("MONITOR", fmt.Sprintf("baseline: recording to %s, summary every %dm in %s",
			o.Baseline.Path, o.Baseline.SummaryMinutes, baseline.SummaryPath(o.Baseline.Path)))
	}
}
