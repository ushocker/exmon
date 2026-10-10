package services

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"exmon/internal/event"
	"exmon/internal/winsys"
)

// recLog records lines and metrics.
type recLog struct {
	lines   []string
	metrics map[string]map[string]float64
}

func (l *recLog) Log(kind, detail string)                      { l.lines = append(l.lines, kind+" "+detail) }
func (l *recLog) Metrics(subject string, m map[string]float64) { l.metrics[subject] = m }
func (l *recLog) DeclareMetrics(map[string]event.Direction)    {}
func (l *recLog) Held() bool                                   { return false }

func (l *recLog) take() []string { out := l.lines; l.lines = nil; return out }

// world is a fake set of services and files.
type world struct {
	svc  map[string]winsys.ServiceInfo
	err  map[string]error
	vers map[string]winsys.FileVersion
	now  time.Time
}

func newEnv(t *testing.T, cfg Config) (*Source, *world, *recLog) {
	t.Helper()
	w := &world{svc: map[string]winsys.ServiceInfo{}, err: map[string]error{}, vers: map[string]winsys.FileVersion{}, now: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	s := newWith(cfg, Deps{
		Query: func(name string) (winsys.ServiceInfo, error) {
			if err := w.err[name]; err != nil {
				return winsys.ServiceInfo{}, err
			}
			info, ok := w.svc[name]
			if !ok {
				return winsys.ServiceInfo{}, winsys.ErrServiceNotFound
			}
			return info, nil
		},
		ProcessPath: func(uint32) (string, error) { return "", errors.New("access denied") },
		FileVersion: func(p string) (winsys.FileVersion, error) { return w.vers[p], nil },
		Now:         func() time.Time { return w.now },
	})
	return s, w, &recLog{metrics: map[string]map[string]float64{}}
}

// exe makes a real file (the source stats it) and gives it a version.
func exe(t *testing.T, w *world, name, version string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(version), 0o644); err != nil {
		t.Fatal(err)
	}
	w.vers[p] = winsys.FileVersion{FileVersion: version, Company: "Contoso"}
	return p
}

func has(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func TestServicesLifecycle(t *testing.T) {
	s, w, log := newEnv(t, Config{Enabled: true, PollSeconds: 30, HeartbeatMinutes: 60, Watch: []Watch{
		{Name: "Agent"}, {Name: "Gone"}, {Name: "Optional", Expect: ExpectAny},
	}})
	agent := exe(t, w, "agent.exe", "1.2.0.0")
	w.svc["Agent"] = winsys.ServiceInfo{Name: "Agent", DisplayName: "The Agent", State: "running", StartType: "auto", PID: 42, BinaryPath: `"` + agent + `" --run`}
	w.svc["Optional"] = winsys.ServiceInfo{Name: "Optional", State: "stopped", StartType: "manual", BinaryPath: agent}

	// First poll: a status line each, and SERVICE/down for the missing one.
	s.poll(log)
	got := log.take()
	if !has(got, `SERVICE/status [Agent] running "The Agent" start=auto pid=42 exe=`) || !strings.Contains(strings.Join(got, "\n"), "version=1.2.0.0") {
		t.Errorf("agent status: %q", got)
	}
	if !has(got, "SERVICE/down [Gone] not_installed (expected running)") || has(got, "SERVICE/down [Agent]") || has(got, "SERVICE/down [Optional]") {
		t.Errorf("first poll downs: %q", got)
	}
	if log.metrics["Agent"]["running"] != 1 || log.metrics["Gone"]["as_expected"] != 0 || log.metrics["Optional"]["as_expected"] != 1 {
		t.Errorf("metrics: %v", log.metrics)
	}

	// Nothing changes: no lines (heartbeat not due).
	w.now = w.now.Add(30 * time.Second)
	s.poll(log)
	if got := log.take(); len(got) != 0 {
		t.Errorf("quiet poll: %q", got)
	}

	// The agent crashes: changed + down, once.
	w.svc["Agent"] = winsys.ServiceInfo{Name: "Agent", State: "stopped", StartType: "auto", ExitCode: 1067, BinaryPath: agent}
	s.poll(log)
	got = log.take()
	if !has(got, "SERVICE/changed [Agent] running -> stopped exit=1067") || !has(got, "SERVICE/down [Agent] stopped (expected running) exit=1067") {
		t.Errorf("crash: %q", got)
	}
	s.poll(log)
	if got := log.take(); len(got) != 0 {
		t.Errorf("down is reported once: %q", got)
	}

	// It comes back on a new version: changed, up, version_changed.
	agent2 := exe(t, w, "agent.exe", "1.3.0.0")
	w.svc["Agent"] = winsys.ServiceInfo{Name: "Agent", State: "running", StartType: "auto", PID: 77, BinaryPath: agent2}
	s.poll(log)
	got = log.take()
	if !has(got, "SERVICE/changed [Agent] stopped -> running") || !has(got, "SERVICE/up [Agent] running again") ||
		!has(got, "SERVICE/version_changed [Agent] agent.exe 1.2.0.0 -> agent.exe 1.3.0.0") {
		t.Errorf("recovery: %q", got)
	}

	// Heartbeat: a status line for every service after heartbeatMinutes.
	w.now = w.now.Add(61 * time.Minute)
	s.poll(log)
	if got := log.take(); len(got) != 3 || !has(got, "SERVICE/status [Gone] not installed") {
		t.Errorf("heartbeat: %q", got)
	}

	// Query errors are reported once.
	w.err["Optional"] = errors.New("Access is denied.")
	s.poll(log)
	if got := log.take(); !has(got, "SERVICE/error [Optional] Access is denied.") {
		t.Errorf("error: %q", got)
	}
}

func TestSvchostServiceDLL(t *testing.T) {
	s, w, log := newEnv(t, Config{Enabled: true, PollSeconds: 30, Watch: []Watch{{Name: "Dnscache"}}})
	host := exe(t, w, "svchost.exe", "10.0.26100.1")
	dll := exe(t, w, "dnsrslvr.dll", "10.0.26100.7")
	w.svc["Dnscache"] = winsys.ServiceInfo{Name: "Dnscache", State: "running", StartType: "auto", PID: 9, BinaryPath: host + " -k NetworkService -p", ServiceDLL: dll}
	s.poll(log)
	line := strings.Join(log.take(), "\n")
	if !strings.Contains(line, `dll="`+dll+`" dll_version=10.0.26100.7`) {
		t.Errorf("svchost dll: %s", line)
	}
}

func TestConfigValidate(t *testing.T) {
	ok := Config{Enabled: true, PollSeconds: 30, HeartbeatMinutes: 60, Watch: []Watch{{Name: "Spooler"}, {Name: "wuauserv", Expect: "any"}}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []Config{
		{Enabled: true, PollSeconds: 1},
		{Enabled: true, PollSeconds: 30, HeartbeatMinutes: 2000},
		{Enabled: true, PollSeconds: 30, Watch: []Watch{{Name: ""}}},
		{Enabled: true, PollSeconds: 30, Watch: []Watch{{Name: "A"}, {Name: "a"}}},
		{Enabled: true, PollSeconds: 30, Watch: []Watch{{Name: "A", Expect: "up"}}},
		{Enabled: true, PollSeconds: 30, Watch: []Watch{{Name: `..\x`}}},
		{Enabled: true, PollSeconds: 30, Watch: make([]Watch, MaxWatch+1)},
	}
	for i, c := range bad {
		if c.Validate() == nil {
			t.Errorf("bad config %d accepted: %+v", i, c)
		}
	}
}

// TestRealServices runs one real poll against services every Windows PC has.
func TestRealServices(t *testing.T) {
	s := New(Config{Enabled: true, PollSeconds: 30, Watch: []Watch{{Name: "EventLog"}, {Name: "Dnscache"}}})
	log := &recLog{metrics: map[string]map[string]float64{}}
	s.poll(log)
	for _, l := range log.lines {
		t.Log(l)
	}
	if log.metrics["EventLog"]["running"] != 1 || !has(log.lines, "SERVICE/status [EventLog] running") {
		t.Errorf("EventLog: %q %v", log.lines, log.metrics)
	}
}
