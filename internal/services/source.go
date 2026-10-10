// Package services watches a configured list of Windows services: whether each
// is installed and in the state it should be in (usually running), and the
// version of the program it runs. It reports changes as SERVICE/* lines, a
// SERVICE/status line per service at start and every heartbeatMinutes, and the
// metrics running and as_expected for triggers.
package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"exmon/internal/event"
	"exmon/internal/winsys"
)

var metricDirections = map[string]event.Direction{
	"running":     event.LowIsBad, // 1 = running, 0 = anything else (or not installed)
	"as_expected": event.LowIsBad, // 1 = in the state the config expects
}

// snapshot is one service as seen by one poll.
type snapshot struct {
	State      string // winsys state, or "not_installed" / "unknown" (query failed)
	StartType  string
	PID        uint32
	ExitCode   uint32
	Display    string
	Exe        string
	ExeVer     winsys.FileVersion
	DLL        string // svchost services: the ServiceDll
	DLLVer     winsys.FileVersion
	Err        string // why the query failed, when State is "unknown"
	ServiceErr uint32
}

const (
	stateNotInstalled = "not_installed"
	stateUnknown      = "unknown"
)

// versionKey identifies "the same program at the same version" for change detection.
func (s snapshot) versionKey() string {
	return s.Exe + "|" + s.ExeVer.FileVersion + "|" + s.ExeVer.ProductVersion + "|" + s.DLL + "|" + s.DLLVer.FileVersion
}

// Deps are the Windows calls, replaceable in tests.
type Deps struct {
	Query       func(name string) (winsys.ServiceInfo, error)
	ProcessPath func(pid uint32) (string, error)
	FileVersion func(path string) (winsys.FileVersion, error)
	Now         func() time.Time
}

// Source implements source.Source for the services monitor.
type Source struct {
	cfg     Config
	d       Deps
	prev    map[string]snapshot
	down    map[string]bool // last reported: not as expected
	lastHB  time.Time
	started bool
	vers    map[string]cachedVersion // by path: the version resource, re-read only when the file changes
}

type cachedVersion struct {
	mod  time.Time
	size int64
	v    winsys.FileVersion
	err  error
}

// New creates the services source with the real Windows calls.
func New(cfg Config) *Source {
	return newWith(cfg, Deps{
		Query:       winsys.QueryService,
		ProcessPath: winsys.ProcessImagePath,
		FileVersion: winsys.ReadFileVersion,
		Now:         time.Now,
	})
}

func newWith(cfg Config, d Deps) *Source {
	return &Source{cfg: cfg, d: d, prev: map[string]snapshot{}, down: map[string]bool{}, vers: map[string]cachedVersion{}}
}

// Name implements source.Source.
func (s *Source) Name() string { return "services" }

// Run implements source.Source.
func (s *Source) Run(ctx context.Context, log event.Logger) error {
	log.DeclareMetrics(metricDirections)
	ticker := time.NewTicker(time.Duration(max(1, s.cfg.PollSeconds)) * time.Second)
	defer ticker.Stop()
	for {
		s.poll(log)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// poll checks every watched service once.
func (s *Source) poll(log event.Logger) {
	now := s.d.Now()
	heartbeat := !s.started || (s.cfg.HeartbeatMinutes > 0 && now.Sub(s.lastHB) >= time.Duration(s.cfg.HeartbeatMinutes)*time.Minute)
	for _, w := range s.cfg.Watch {
		cur := s.check(w.Name)
		prev, seen := s.prev[w.Name]
		ok := asExpected(cur.State, w.expect())

		if seen {
			s.changes(log, w.Name, prev, cur)
		}
		if heartbeat {
			log.Log("SERVICE/status", fmt.Sprintf("[%s] %s", w.Name, describe(cur, w.expect())))
		}
		// Report the expectation flipping (and at the first poll, a service that
		// is already not as expected).
		if wasDown, reported := s.down[w.Name]; !ok && (!reported || !wasDown) {
			log.Log("SERVICE/down", fmt.Sprintf("[%s] %s (expected %s)%s", w.Name, cur.State, w.expect(), exitText(cur)))
		} else if ok && reported && wasDown {
			log.Log("SERVICE/up", fmt.Sprintf("[%s] %s again", w.Name, cur.State))
		}
		s.down[w.Name] = !ok

		running := 0.0
		if cur.State == "running" {
			running = 1
		}
		log.Metrics(w.Name, map[string]float64{"running": running, "as_expected": boolf(ok)})
		s.prev[w.Name] = cur
	}
	if heartbeat {
		s.lastHB = now
	}
	s.started = true
}

// changes logs what changed between two polls of one service.
func (s *Source) changes(log event.Logger, name string, prev, cur snapshot) {
	if prev.State != cur.State {
		detail := fmt.Sprintf("[%s] %s -> %s", name, prev.State, cur.State)
		if cur.State == "stopped" {
			detail += exitText(cur)
		}
		log.Log("SERVICE/changed", detail)
	}
	if prev.StartType != cur.StartType && prev.StartType != "" && cur.StartType != "" {
		log.Log("SERVICE/changed", fmt.Sprintf("[%s] start type %s -> %s", name, prev.StartType, cur.StartType))
	}
	if prev.versionKey() != cur.versionKey() && prev.Exe != "" && cur.Exe != "" {
		log.Log("SERVICE/version_changed", fmt.Sprintf("[%s] %s -> %s", name, versionText(prev), versionText(cur)))
	}
	if cur.Err != "" && cur.Err != prev.Err {
		log.Log("SERVICE/error", fmt.Sprintf("[%s] %s", name, cur.Err))
	}
}

// check queries one service and the version of what it runs.
func (s *Source) check(name string) snapshot {
	info, err := s.d.Query(name)
	if errors.Is(err, winsys.ErrServiceNotFound) {
		return snapshot{State: stateNotInstalled}
	}
	if err != nil {
		return snapshot{State: stateUnknown, Err: err.Error()}
	}
	snap := snapshot{State: info.State, StartType: info.StartType, PID: info.PID, Display: info.DisplayName}
	if info.State == "stopped" {
		snap.ExitCode, snap.ServiceErr = info.ExitCode, info.ServiceExit
	}
	// The exe actually running is the truth; LocalService may not open a SYSTEM
	// process, so fall back to the configured command line.
	if info.PID != 0 && !info.Driver {
		if p, err := s.d.ProcessPath(info.PID); err == nil {
			snap.Exe = p
		}
	}
	if snap.Exe == "" {
		snap.Exe = winsys.ExePathFromCommandLine(info.BinaryPath)
	}
	snap.ExeVer = s.version(snap.Exe)
	if info.ServiceDLL != "" && strings.EqualFold(filepath.Base(snap.Exe), "svchost.exe") {
		snap.DLL = info.ServiceDLL
		snap.DLLVer = s.version(info.ServiceDLL)
	}
	return snap
}

// version reads a file's version resource, re-reading only when the file's size
// or time changes (an in-place update), so a poll costs a stat per file.
func (s *Source) version(path string) winsys.FileVersion {
	if path == "" {
		return winsys.FileVersion{}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return winsys.FileVersion{}
	}
	if c, ok := s.vers[path]; ok && c.mod.Equal(fi.ModTime()) && c.size == fi.Size() {
		return c.v
	}
	v, err := s.d.FileVersion(path)
	s.vers[path] = cachedVersion{mod: fi.ModTime(), size: fi.Size(), v: v, err: err}
	return v
}

func asExpected(state, expect string) bool {
	switch expect {
	case ExpectAny:
		return true
	case ExpectStopped:
		return state == "stopped"
	}
	return state == "running"
}

// describe is the SERVICE/status detail.
func describe(s snapshot, expect string) string {
	switch s.State {
	case stateNotInstalled:
		return "not installed (expected " + expect + ")"
	case stateUnknown:
		return "unknown: " + s.Err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s", s.State)
	if s.Display != "" {
		fmt.Fprintf(&b, " %q", s.Display)
	}
	fmt.Fprintf(&b, " start=%s", s.StartType)
	if s.PID != 0 {
		fmt.Fprintf(&b, " pid=%d", s.PID)
	}
	if s.State == "stopped" {
		b.WriteString(exitText(s))
	}
	if s.Exe != "" {
		fmt.Fprintf(&b, ` exe="%s" %s`, s.Exe, fileVersionText("", s.ExeVer)) // plain quotes: %q would double every backslash
	}
	if s.DLL != "" {
		fmt.Fprintf(&b, ` dll="%s" %s`, s.DLL, fileVersionText("dll_", s.DLLVer))
	}
	if expect != ExpectRunning {
		fmt.Fprintf(&b, " expect=%s", expect)
	}
	return b.String()
}

// fileVersionText is "version=… product_version=… company=…" (or "version=-"),
// each key with prefix (e.g. "dll_") so a line's keys stay unique.
func fileVersionText(prefix string, v winsys.FileVersion) string {
	if v.FileVersion == "" && v.ProductVersion == "" {
		return prefix + "version=-"
	}
	s := prefix + "version=" + or(v.FileVersion, v.ProductVersion)
	if v.ProductVersion != "" && v.ProductVersion != v.FileVersion {
		s += " " + prefix + "product_version=" + v.ProductVersion
	}
	if v.Company != "" {
		s += fmt.Sprintf(" %scompany=%q", prefix, v.Company)
	}
	return s
}

// versionText is the short form for SERVICE/version_changed.
func versionText(s snapshot) string {
	t := filepath.Base(s.Exe) + " " + or(s.ExeVer.FileVersion, "-")
	if s.DLL != "" {
		t += ", " + filepath.Base(s.DLL) + " " + or(s.DLLVer.FileVersion, "-")
	}
	return t
}

// exitText explains why a stopped service stopped, when it says.
func exitText(s snapshot) string {
	switch {
	case s.ExitCode == 0:
		return ""
	case s.ExitCode == 1066 && s.ServiceErr != 0: // ERROR_SERVICE_SPECIFIC_ERROR
		return fmt.Sprintf(" exit=service-specific %d", s.ServiceErr)
	}
	return fmt.Sprintf(" exit=%d", s.ExitCode)
}

func boolf(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
