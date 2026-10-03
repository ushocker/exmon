// Package config loads exmon's settings: defaults, then exmon.json, then flags.
package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"

	"exmon/internal/baseline"
	"exmon/internal/eventlog"
	"exmon/internal/incident"
	"exmon/internal/remote"
	"exmon/internal/wifi"
)

// Options is the whole configuration. Each package owns its own section's struct
// and defaults; Options just puts them together.
// To add a monitor: add its Config here, set its default in Defaults, and add flags.
type Options struct {
	Log      eventlog.Config `json:"log"`
	WiFi     wifi.Config     `json:"wifi"`
	Incident incident.Config `json:"incident"`
	Remote   remote.Config   `json:"remote"`
	Baseline baseline.Config `json:"baseline"`
}

// Defaults returns the built-in settings.
func Defaults() Options {
	return Options{
		Log:      eventlog.DefaultConfig(),
		WiFi:     wifi.DefaultConfig(),
		Incident: incident.DefaultConfig(),
		Remote:   remote.DefaultConfig(),
		Baseline: baseline.DefaultConfig(),
	}
}

// Load builds the configuration from defaults < JSON file < command-line flags.
// args are the command-line arguments without the program name (os.Args[1:]).
func Load(args []string) (Options, error) {
	opts := Defaults()

	// The flags write into a separate copy. Only flags the user actually typed are
	// copied onto opts later, so an untyped flag never overwrites the JSON value.
	ff := Defaults()
	flags := flag.NewFlagSet("exmon", flag.ContinueOnError)
	configPath := flags.String("config", defaultConfigPath(), "path to the JSON config file")

	flags.StringVar(&ff.Log.Path, "log-path", ff.Log.Path, "log file (%VAR% is expanded)")
	flags.IntVar(&ff.Log.MaxSizeMB, "log-max-size-mb", ff.Log.MaxSizeMB, "rotate the log at this size; 0 disables rotation")
	flags.IntVar(&ff.Log.MaxFiles, "log-max-files", ff.Log.MaxFiles, "rotated log files to keep")
	flags.IntVar(&ff.Log.FlushIntervalMs, "log-flush-interval-ms", ff.Log.FlushIntervalMs, "how often to flush the log to disk")

	flags.BoolVar(&ff.WiFi.Enabled, "wifi-enabled", ff.WiFi.Enabled, "run the Wi-Fi monitor")
	flags.IntVar(&ff.WiFi.PollSeconds, "wifi-poll-seconds", ff.WiFi.PollSeconds, "seconds between Wi-Fi samples")
	flags.BoolVar(&ff.WiFi.Neighbors, "wifi-neighbors", ff.WiFi.Neighbors, "also log other BSSIDs with the same SSID")
	flags.BoolVar(&ff.WiFi.DebugPoll, "wifi-debug-poll", ff.WiFi.DebugPoll, "log every poll, even when nothing changed")
	flags.IntVar(&ff.WiFi.RSSIThreshold, "wifi-rssi-threshold", ff.WiFi.RSSIThreshold, "dBm move that counts as a change")
	flags.IntVar(&ff.WiFi.RateChangePercent, "wifi-rate-change-percent", ff.WiFi.RateChangePercent, "rx/tx swing in % that counts as a change; 0 ignores rates")
	flags.IntVar(&ff.WiFi.ErrorPctThreshold, "wifi-error-pct-threshold", ff.WiFi.ErrorPctThreshold, "retry/FCS % point move that counts as a change; 0 ignores them")
	flags.IntVar(&ff.WiFi.MinStatsFrames, "wifi-min-stats-frames", ff.WiFi.MinStatsFrames, "frames needed before retry/FCS % is trusted")
	flags.IntVar(&ff.WiFi.HeartbeatSeconds, "wifi-heartbeat-seconds", ff.WiFi.HeartbeatSeconds, "write a POLL line at least this often even with no change; 0 = off")

	flags.BoolVar(&ff.Incident.Enabled, "incident-enabled", ff.Incident.Enabled, "run the rolling window and incident triggers")
	flags.StringVar(&ff.Incident.Dir, "incident-dir", ff.Incident.Dir, "where incident .log.gz files are saved")
	flags.IntVar(&ff.Incident.WindowMinutes, "incident-window-minutes", ff.Incident.WindowMinutes, "minutes of history kept for incidents")
	flags.IntVar(&ff.Incident.PostTriggerSeconds, "incident-post-seconds", ff.Incident.PostTriggerSeconds, "seconds to keep recording after a trigger")

	flags.BoolVar(&ff.Remote.Enabled, "remote-enabled", ff.Remote.Enabled, "forward selected events to the remote target")
	flags.StringVar(&ff.Remote.Outbox, "remote-outbox", ff.Remote.Outbox, "stand-in remote: file that receives sent events")
	flags.StringVar(&ff.Remote.SpoolDir, "remote-spool-dir", ff.Remote.SpoolDir, "on-disk queue for events waiting to be sent")
	flags.IntVar(&ff.Remote.RetryMaxMinutes, "remote-retry-max-minutes", ff.Remote.RetryMaxMinutes, "longest wait between send retries")
	flags.BoolVar(&ff.Remote.SimulateOffline, "remote-simulate-offline", ff.Remote.SimulateOffline, "testing: make every send fail")

	flags.BoolVar(&ff.Baseline.Enabled, "baseline", ff.Baseline.Enabled, "learning mode: record every event and metric to the baseline file")
	flags.StringVar(&ff.Baseline.Path, "baseline-path", ff.Baseline.Path, "baseline .jsonl file")
	flags.IntVar(&ff.Baseline.SummaryMinutes, "baseline-summary-minutes", ff.Baseline.SummaryMinutes, "how often baseline-summary.txt is rewritten")

	if err := flags.Parse(args); err != nil {
		return opts, err
	}

	// Was --config typed? If so, a missing file is an error; otherwise it's optional.
	configSet := false
	flags.Visit(func(f *flag.Flag) { configSet = configSet || f.Name == "config" })
	if err := loadFile(*configPath, &opts, configSet); err != nil {
		return opts, err
	}

	// flags.Visit calls the function once for each flag that was actually set.
	// Each case copies one value from the flag copy (ff) onto opts.
	overrides := map[string]func(){
		"log-path":                 func() { opts.Log.Path = ff.Log.Path },
		"log-max-size-mb":          func() { opts.Log.MaxSizeMB = ff.Log.MaxSizeMB },
		"log-max-files":            func() { opts.Log.MaxFiles = ff.Log.MaxFiles },
		"log-flush-interval-ms":    func() { opts.Log.FlushIntervalMs = ff.Log.FlushIntervalMs },
		"wifi-enabled":             func() { opts.WiFi.Enabled = ff.WiFi.Enabled },
		"wifi-poll-seconds":        func() { opts.WiFi.PollSeconds = ff.WiFi.PollSeconds },
		"wifi-neighbors":           func() { opts.WiFi.Neighbors = ff.WiFi.Neighbors },
		"wifi-debug-poll":          func() { opts.WiFi.DebugPoll = ff.WiFi.DebugPoll },
		"wifi-rssi-threshold":      func() { opts.WiFi.RSSIThreshold = ff.WiFi.RSSIThreshold },
		"wifi-rate-change-percent": func() { opts.WiFi.RateChangePercent = ff.WiFi.RateChangePercent },
		"wifi-error-pct-threshold": func() { opts.WiFi.ErrorPctThreshold = ff.WiFi.ErrorPctThreshold },
		"wifi-min-stats-frames":    func() { opts.WiFi.MinStatsFrames = ff.WiFi.MinStatsFrames },
		"wifi-heartbeat-seconds":   func() { opts.WiFi.HeartbeatSeconds = ff.WiFi.HeartbeatSeconds },
		"incident-enabled":         func() { opts.Incident.Enabled = ff.Incident.Enabled },
		"incident-dir":             func() { opts.Incident.Dir = ff.Incident.Dir },
		"incident-window-minutes":  func() { opts.Incident.WindowMinutes = ff.Incident.WindowMinutes },
		"incident-post-seconds":    func() { opts.Incident.PostTriggerSeconds = ff.Incident.PostTriggerSeconds },
		"remote-enabled":           func() { opts.Remote.Enabled = ff.Remote.Enabled },
		"remote-outbox":            func() { opts.Remote.Outbox = ff.Remote.Outbox },
		"remote-spool-dir":         func() { opts.Remote.SpoolDir = ff.Remote.SpoolDir },
		"remote-retry-max-minutes": func() { opts.Remote.RetryMaxMinutes = ff.Remote.RetryMaxMinutes },
		"remote-simulate-offline":  func() { opts.Remote.SimulateOffline = ff.Remote.SimulateOffline },
		"baseline":                 func() { opts.Baseline.Enabled = ff.Baseline.Enabled },
		"baseline-path":            func() { opts.Baseline.Path = ff.Baseline.Path },
		"baseline-summary-minutes": func() { opts.Baseline.SummaryMinutes = ff.Baseline.SummaryMinutes },
	}
	flags.Visit(func(f *flag.Flag) {
		if set, ok := overrides[f.Name]; ok {
			set()
		}
	})

	// Expand %VAR% in every path. A slice of pointers lets one loop update them all.
	for _, p := range []*string{&opts.Log.Path, &opts.Incident.Dir, &opts.Remote.Outbox, &opts.Remote.SpoolDir, &opts.Baseline.Path} {
		expanded, err := registry.ExpandString(*p)
		if err != nil {
			return opts, fmt.Errorf("expanding %q: %w", *p, err)
		}
		*p = filepath.Clean(expanded) // also turns any "/" into "\"
	}

	return opts, opts.Validate()
}

// Validate checks for settings that can't work.
func (o Options) Validate() error {
	if o.Log.Path == "" {
		return errors.New("log path must be set")
	}
	if o.WiFi.PollSeconds < 1 {
		return errors.New("wifi pollSeconds must be >= 1")
	}
	// errors.Join combines several errors into one (nil ones are ignored).
	return errors.Join(o.Incident.Validate(), o.Remote.Validate(), o.Baseline.Validate())
}

// loadFile reads the JSON file over opts. Keys missing from the file keep their
// current (default) values, because json.Unmarshal only sets the fields it finds.
func loadFile(path string, opts *Options, required bool) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && !required {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, opts); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// defaultConfigPath is exmon.json next to the executable. Services start in
// System32, so the working directory is no good for finding it.
func defaultConfigPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "exmon.json"
	}
	return filepath.Join(filepath.Dir(exe), "exmon.json")
}
