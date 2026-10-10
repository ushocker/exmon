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
	"strings"

	"golang.org/x/sys/windows/registry"

	"exmon/internal/baseline"
	"exmon/internal/control"
	"exmon/internal/eventlog"
	"exmon/internal/incident"
	"exmon/internal/nic"
	"exmon/internal/power"
	"exmon/internal/probe"
	"exmon/internal/remote"
	"exmon/internal/remote/senders"
	"exmon/internal/safepath"
	"exmon/internal/vpn"
	"exmon/internal/wifi"
)

// Options is the whole configuration. Each package owns its own section's struct
// and defaults; Options just puts them together.
// To add a monitor: add its Config here, set its default in Defaults, and add flags.
type Options struct {
	Log      eventlog.Config `json:"log"`
	WiFi     wifi.Config     `json:"wifi"`
	Incident incident.Config `json:"incident"`
	Remotes  remote.Remotes  `json:"remotes"`
	Baseline baseline.Config `json:"baseline"`
	NIC      nic.Config      `json:"nic"`
	VPN      vpn.Config      `json:"vpn"`
	Probe    probe.Config    `json:"probe"`
	Power    power.Config    `json:"power"`
	Control  control.Config  `json:"control"`

	// OutputRoots are extra folders the service may write its outputs to, besides
	// %ProgramData%\exmon (see CheckOutputs). Console mode doesn't use them.
	OutputRoots []string `json:"outputRoots,omitempty"`

	// Not settings: what Load found or was asked to do.
	ConfigFile         string   `json:"-"` // the config file that was loaded; "" = built-in defaults
	CloudVersion       int64    `json:"-"` // version of the cloud config layer in use; 0 = none
	CloudSummary       string   `json:"-"` // e.g. "version 2026… (probe, remotes)", for the startup log
	ConfigWarnings     []string `json:"-"` // config files skipped because they failed the trust check
	WriteDefaultConfig string   `json:"-"` // set by --write-default-config: where to write a starter config
}

// Defaults returns the built-in settings.
func Defaults() Options {
	return Options{
		Log:      eventlog.DefaultConfig(),
		WiFi:     wifi.DefaultConfig(),
		Incident: incident.DefaultConfig(),
		Remotes:  remote.DefaultRemotes(),
		Baseline: baseline.DefaultConfig(),
		NIC:      nic.DefaultConfig(),
		VPN:      vpn.DefaultConfig(),
		Probe:    probe.DefaultConfig(),
		Power:    power.DefaultConfig(),
		Control:  control.DefaultConfig(),
	}
}

// Load builds the configuration from defaults < JSON file < command-line flags.
// args are the command-line arguments without the program name (os.Args[1:]).
func Load(args []string) (Options, error) {
	return LoadWith(args, nil)
}

// TrustFunc decides whether a config file may be used; it returns nil to accept it.
// The service uses one that only accepts files administrators control.
type TrustFunc func(path string) error

// LoadWith is Load with a trust check on the config file (nil = trust everything,
// which is right for console mode: you run exmon as yourself).
//
//   - An untrusted --config file is an error.
//   - In the search, an untrusted exmon.json next to the exe is skipped with a
//     warning (recorded in Options.ConfigWarnings) and the search continues.
//   - An untrusted machine-wide config is an error: running a config someone may
//     have tampered with is worse than not starting.
func LoadWith(args []string, trust TrustFunc) (Options, error) {
	return loadWith(args, trust, nil)
}

// loadWith is LoadWith; cloud, if not nil, replaces the active cloud config
// (TestCloud uses it to try a newly received one).
func loadWith(args []string, trust TrustFunc, cloud *CloudPayload) (Options, error) {
	senders.RegisterAll() // remote target types must be known before "remotes" is validated
	opts := Defaults()

	// The flags write into a separate copy. Only flags the user actually typed are
	// copied onto opts later, so an untyped flag never overwrites the JSON value.
	ff := Defaults()
	flags := flag.NewFlagSet("exmon", flag.ContinueOnError)
	configPath := flags.String("config", "",
		"JSON config file (default: exmon.json next to exmon.exe if present, else "+DefaultConfigPath+")")
	writeDefault := flags.Bool("write-default-config", false,
		"write a starter config to --config (or "+DefaultConfigPath+") and exit; never overwrites")

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

	// These two apply to every remote in the "remotes" list.
	remotesEnabled := flags.Bool("remotes-enabled", true, "forward events to the configured remotes; false turns every remote off")
	remotesOffline := flags.Bool("remote-simulate-offline", false, "testing: make every remote's sends fail")

	flags.BoolVar(&ff.Baseline.Enabled, "baseline", ff.Baseline.Enabled, "learning mode: record every event and metric to the baseline file")
	flags.StringVar(&ff.Baseline.Path, "baseline-path", ff.Baseline.Path, "baseline .jsonl file")
	flags.IntVar(&ff.Baseline.SummaryMinutes, "baseline-summary-minutes", ff.Baseline.SummaryMinutes, "how often baseline-summary.txt is rewritten")

	flags.BoolVar(&ff.NIC.Enabled, "nic-enabled", ff.NIC.Enabled, "monitor network adapters")
	flags.BoolVar(&ff.VPN.Enabled, "vpn-enabled", ff.VPN.Enabled, "detect VPN tunnels")
	flags.BoolVar(&ff.Probe.Enabled, "probe-enabled", ff.Probe.Enabled, "run connectivity probes (DNS, gateway, endpoints, HTTP)")
	flags.IntVar(&ff.Probe.IntervalSeconds, "probe-interval-seconds", ff.Probe.IntervalSeconds, "seconds between probe rounds")
	flags.IntVar(&ff.Probe.PathTrace.IntervalMinutes, "probe-path-trace-minutes", ff.Probe.PathTrace.IntervalMinutes, "minutes between periodic path traces; 0 = off")
	flags.BoolVar(&ff.Power.Enabled, "power-enabled", ff.Power.Enabled, "hold events while starting, waking from sleep or shutting down, until the network is ready")

	flags.BoolVar(&ff.Control.Enabled, "control-enabled", ff.Control.Enabled, "connect to the control plane (MQTT over WebSockets to Event Grid)")
	flags.StringVar(&ff.Control.Host, "control-host", ff.Control.Host, "control plane host, e.g. exmon-prod.eastus-1.ts.eventgrid.azure.net (also turns the control plane on)")
	flags.StringVar(&ff.Control.Path, "control-path", ff.Control.Path, "control plane WebSocket path")
	flags.StringVar(&ff.Control.Cert.SubjectCN, "control-cert-subject", ff.Control.Cert.SubjectCN, "use the client certificate with this common name")
	flags.StringVar(&ff.Control.Cert.IssuerCN, "control-cert-issuer", ff.Control.Cert.IssuerCN, "use a client certificate issued by the CA with this common name")
	updateHosts := flags.String("update-hosts", "", "comma-separated hosts updates may be downloaded from (also turns updates on)")
	flags.StringVar(&ff.Control.Update.PublisherCN, "update-publisher", ff.Control.Update.PublisherCN, "common name of the code-signing certificate updates must carry")
	flags.BoolVar(&ff.Control.Update.RequireAuthenticode, "update-require-authenticode", ff.Control.Update.RequireAuthenticode, "require a valid code signature on updates (false only for unsigned test builds)")

	if err := flags.Parse(args); err != nil {
		return opts, err
	}

	// --write-default-config: just report where to write; main writes the file and exits.
	if *writeDefault {
		target := *configPath
		if target == "" {
			target = expand(DefaultConfigPath)
		}
		opts.WriteDefaultConfig = target
		return opts, nil
	}

	// An explicit --config must exist. Otherwise use the first file found in the
	// search order; with none, the built-in defaults apply.
	if *configPath != "" {
		if trust != nil {
			if err := trust(*configPath); err != nil {
				return opts, fmt.Errorf("refusing config %s: %w", *configPath, err)
			}
		}
		if err := loadFile(*configPath, &opts); err != nil {
			return opts, err
		}
		opts.ConfigFile = *configPath
	} else {
		found, warnings, err := searchTrusted(SearchPaths(), trust)
		opts.ConfigWarnings = warnings
		if err != nil {
			return opts, err
		}
		if found != "" {
			if err := loadFile(found, &opts); err != nil {
				return opts, err
			}
			opts.ConfigFile = found
		}
	}

	// The cloud config layer (between the local file and the flags) is used only
	// when the control plane is on, locally or by flag.
	cloudOn := opts.Control.Enabled
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "control-host" || f.Name == "control-enabled" {
			cloudOn = ff.Control.Enabled || ff.Control.Host != ""
		}
	})
	if cloudOn {
		explicit := cloud != nil
		if cloud == nil {
			keys, err := cloudKeys()
			if err == nil {
				cloud, err = readCloud(keys)
			}
			if err != nil {
				// A stored cloud config that no longer verifies (changed on disk, or its
				// key was removed in this build) is ignored, not fatal: the local config
				// still runs, and the control plane can send a new one.
				opts.ConfigWarnings = append(opts.ConfigWarnings, "ignored the cloud config: "+err.Error())
				cloud = nil
			}
		}
		if cloud != nil {
			err := checkCloudLayer(cloud.Config)
			if err == nil {
				err = applyCloud(cloud.Config, &opts)
			}
			switch {
			case err != nil && explicit:
				return opts, err
			case err != nil:
				opts.ConfigWarnings = append(opts.ConfigWarnings, "ignored the cloud config: "+err.Error())
			default:
				opts.CloudVersion, opts.CloudSummary = cloud.Version, describeCloud(cloud)
			}
		}
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
		"remotes-enabled": func() {
			for i := range opts.Remotes {
				on := *remotesEnabled // a new variable each time, so each remote gets its own pointer
				opts.Remotes[i].Enabled = &on
			}
		},
		"remote-simulate-offline": func() {
			for i := range opts.Remotes {
				opts.Remotes[i].SimulateOffline = *remotesOffline
			}
		},
		"baseline":                 func() { opts.Baseline.Enabled = ff.Baseline.Enabled },
		"baseline-path":            func() { opts.Baseline.Path = ff.Baseline.Path },
		"baseline-summary-minutes": func() { opts.Baseline.SummaryMinutes = ff.Baseline.SummaryMinutes },
		"nic-enabled":              func() { opts.NIC.Enabled = ff.NIC.Enabled },
		"vpn-enabled":              func() { opts.VPN.Enabled = ff.VPN.Enabled },
		"probe-enabled":            func() { opts.Probe.Enabled = ff.Probe.Enabled },
		"probe-interval-seconds":   func() { opts.Probe.IntervalSeconds = ff.Probe.IntervalSeconds },
		"probe-path-trace-minutes": func() { opts.Probe.PathTrace.IntervalMinutes = ff.Probe.PathTrace.IntervalMinutes },
		"power-enabled":            func() { opts.Power.Enabled = ff.Power.Enabled },
		"control-enabled":          func() { opts.Control.Enabled = ff.Control.Enabled },
		"control-host": func() {
			opts.Control.Host = ff.Control.Host
			opts.Control.Enabled = ff.Control.Host != ""
		},
		"control-path":         func() { opts.Control.Path = ff.Control.Path },
		"control-cert-subject": func() { opts.Control.Cert.SubjectCN = ff.Control.Cert.SubjectCN },
		"control-cert-issuer":  func() { opts.Control.Cert.IssuerCN = ff.Control.Cert.IssuerCN },
		"update-hosts": func() {
			opts.Control.Update.AllowedHosts = splitList(*updateHosts)
			opts.Control.Update.Enabled = len(opts.Control.Update.AllowedHosts) > 0
		},
		"update-publisher":            func() { opts.Control.Update.PublisherCN = ff.Control.Update.PublisherCN },
		"update-require-authenticode": func() { opts.Control.Update.RequireAuthenticode = ff.Control.Update.RequireAuthenticode },
	}
	flags.Visit(func(f *flag.Flag) {
		if set, ok := overrides[f.Name]; ok {
			set()
		}
	})

	// Fill in each remote's unset settings (list items can't inherit defaults).
	for i := range opts.Remotes {
		opts.Remotes[i].ApplyDefaults()
	}

	// Expand %VAR% in every path. A slice of pointers lets one loop update them all.
	paths := []*string{&opts.Log.Path, &opts.Incident.Dir, &opts.Baseline.Path}
	for i := range opts.Remotes {
		paths = append(paths, &opts.Remotes[i].SpoolDir)
	}
	for i := range opts.OutputRoots {
		paths = append(paths, &opts.OutputRoots[i])
	}
	for _, p := range paths {
		expanded, err := registry.ExpandString(*p)
		if err != nil {
			return opts, fmt.Errorf("expanding %q: %w", *p, err)
		}
		*p = filepath.Clean(expanded) // also turns any "/" into "\"
	}

	return opts, opts.Validate()
}

// splitList splits a comma-separated flag value, dropping empty items.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// output is one configured output location, for CheckOutputs.
type output struct{ setting, path string }

// outputs lists the output paths that are in use with these settings.
func (o Options) outputs() []output {
	out := []output{{"log.path", o.Log.Path}}
	if o.Incident.Enabled {
		out = append(out, output{"incident.dir", o.Incident.Dir})
	}
	for _, r := range o.Remotes {
		if !r.IsEnabled() {
			continue
		}
		paths, err := r.Outputs()
		if err != nil {
			continue // Validate reports bad settings
		}
		for _, p := range paths {
			out = append(out, output{"remotes[" + r.Name + "]", p})
		}
	}
	if o.Baseline.Enabled {
		out = append(out, output{"baseline.path", o.Baseline.Path})
	}
	return out
}

// CheckOutputs is the service's containment check. Every output path in use must
// be inside dataDir (%ProgramData%\exmon) or one of OutputRoots, and no folder or
// file on the way from that root may be a junction or symbolic link. Otherwise a
// mistaken path could make the service rename, delete or write files elsewhere.
func (o Options) CheckOutputs(dataDir string) error {
	roots := append([]string{dataDir}, o.OutputRoots...)
	for _, r := range o.OutputRoots {
		if !filepath.IsAbs(r) {
			return fmt.Errorf("outputRoots entry %q must be a full path such as D:\\Logs\\exmon", r)
		}
	}
	for _, out := range o.outputs() {
		root := ""
		for _, r := range roots {
			if safepath.Within(r, out.path) {
				root = r
				break
			}
		}
		if root == "" {
			return fmt.Errorf("%s %s is outside the allowed output folders (%s); add its folder to outputRoots",
				out.setting, out.path, strings.Join(roots, ", "))
		}
		if err := safepath.CheckNoLinks(root, out.path); err != nil {
			return fmt.Errorf("%s: %w", out.setting, err)
		}
	}
	return nil
}

// Validate checks for settings that can't work.
func (o Options) Validate() error {
	// A renamed metric would otherwise just never fire, so catch the old name.
	for _, r := range o.Incident.Triggers {
		if r.Source == "wifi" && r.Metric == "disconnects_30m" {
			return fmt.Errorf("trigger %q: metric disconnects_30m was renamed to recent_disconnects (window set by wifi.disconnectWindowMinutes)", r.Name)
		}
	}
	// errors.Join combines several errors into one (nil ones are ignored).
	return errors.Join(o.Log.Validate(), o.WiFi.Validate(), o.NIC.Validate(), o.VPN.Validate(),
		o.Probe.Validate(), o.Power.Validate(), o.Incident.Validate(), o.Remotes.Validate(), o.Baseline.Validate(), o.Control.Validate())
}

// DefaultConfigPath is the machine-wide config location (%VAR% not yet expanded).
const DefaultConfigPath = `%ProgramData%\exmon\config\exmon.json`

// SearchPaths is where exmon looks for its config when --config isn't given, in
// order: exmon.json next to the executable (handy for a portable copy), then the
// machine-wide DefaultConfigPath. The working directory is not used: services
// start in System32.
func SearchPaths() []string {
	var paths []string
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "exmon.json"))
	}
	return append(paths, expand(DefaultConfigPath))
}

// findConfig returns the first path that exists, or "".
func findConfig(paths []string) string {
	found, _, _ := searchTrusted(paths, nil)
	return found
}

// searchTrusted returns the first existing path that passes trust. Every path but
// the last is optional: if it fails the check it is skipped with a warning. The last
// (the machine-wide config) failing the check is an error.
func searchTrusted(paths []string, trust TrustFunc) (found string, warnings []string, err error) {
	for i, p := range paths {
		if st, statErr := os.Stat(p); statErr != nil || st.IsDir() {
			continue
		}
		if trust == nil {
			return p, warnings, nil
		}
		terr := trust(p)
		if terr == nil {
			return p, warnings, nil
		}
		if i == len(paths)-1 {
			return "", warnings, fmt.Errorf("refusing config %s: %w", p, terr)
		}
		warnings = append(warnings, fmt.Sprintf("ignored config %s: %v", p, terr))
	}
	return "", warnings, nil
}

// WriteDefault writes content to path, creating its folder. It refuses to replace
// an existing file, so an edited config is never lost.
func WriteDefault(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// O_EXCL makes the open fail if the file already exists.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s already exists; delete or rename it first", path)
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// expand replaces %VAR% in s, leaving s as-is if that fails.
func expand(s string) string {
	if e, err := registry.ExpandString(s); err == nil {
		return filepath.Clean(e)
	}
	return s
}

// loadFile reads the JSON file over opts. Keys missing from the file keep their
// current (default) values, because json.Unmarshal only sets the fields it finds.
func loadFile(path string, opts *Options) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// First look at the top-level keys only. json.RawMessage keeps each value as
	// undecoded bytes, which is all we need to see which keys are present.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	// The old single "remote" section would otherwise be silently ignored.
	if _, old := top["remote"]; old {
		return fmt.Errorf(`%s: "remote" was replaced by "remotes", a list of remotes each with a name and type; see docs/remote.md#migrating-from-remote`, path)
	}
	// A "remotes" list in the file replaces the default list rather than merging
	// item by item with it.
	if _, ok := top["remotes"]; ok {
		opts.Remotes = nil
	}
	if err := json.Unmarshal(b, opts); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
