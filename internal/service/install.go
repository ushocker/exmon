// Package service runs exmon as a Windows Service: install, uninstall, status, and
// the handler the Service Control Manager talks to while the service runs.
//
// The service runs as LocalService with its own service SID (NT SERVICE\exmon). The
// installer copies exmon.exe to Program Files and locks down exmon's folders, so a
// standard user can neither replace the program nor plant a config.
package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"exmon/internal/config"
	"exmon/internal/winsys"
)

// Service identity.
const (
	Name        = "exmon"
	displayName = "exmon network monitor"
	description = "Monitors Wi-Fi, network adapters, VPN tunnels and connectivity, and logs events and incidents."
	account     = `NT AUTHORITY\LocalService`
	scmTimeout  = 30 * time.Second
)

// Rights in the folder permissions below (SDDL hex masks).
const (
	readExecute = "0x1200a9" // read, list, run
	modify      = "0x1301bf" // read, write, delete, but not change permissions or owner
)

// InstallDir is where install copies exmon.exe: Program Files, which only
// administrators can change.
func InstallDir() string {
	return filepath.Join(os.Getenv("ProgramFiles"), Name)
}

// DataDir is exmon's data folder: %ProgramData%\exmon.
func DataDir() string {
	return filepath.Join(os.Getenv("ProgramData"), Name)
}

// Install installs (or updates) and starts the service. args are exmon flags to
// bake into the service's command line. Messages for the user go to out.
func Install(args []string, out io.Writer) error {
	if !winsys.IsElevated() {
		return errors.New("install must be run from an elevated (Run as administrator) prompt")
	}
	// Check the flags now, so a typo fails here rather than inside the service.
	if _, err := config.Load(args); err != nil {
		return fmt.Errorf("invalid flags for the service: %w", err)
	}
	for _, a := range args {
		if strings.Contains(a, "write-default-config") {
			return errors.New("--write-default-config can't be baked into the service")
		}
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the Service Control Manager: %w", err)
	}
	defer m.Disconnect()

	// If it is already installed, stop it so the exe can be replaced.
	existing, err := m.OpenService(Name)
	if err == nil {
		fmt.Fprintln(out, "exmon is already installed; updating it")
		if err := stopAndWait(existing); err != nil {
			existing.Close()
			return err
		}
	}

	exe, err := copySelf(out)
	if err != nil {
		if existing != nil {
			existing.Close()
		}
		return err
	}

	cfg := mgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
		ErrorControl:     mgr.ErrorNormal,
		DisplayName:      displayName,
		Description:      description,
		ServiceStartName: account,
		SidType:          windows.SERVICE_SID_TYPE_UNRESTRICTED, // gives it the NT SERVICE\exmon identity
	}
	var s *mgr.Service
	if existing != nil {
		s = existing
		cfg.BinaryPathName = commandLine(exe, args)
		if err := s.UpdateConfig(cfg); err != nil {
			s.Close()
			return fmt.Errorf("update service: %w", err)
		}
	} else {
		s, err = m.CreateService(Name, exe, cfg, args...)
		if err != nil {
			return fmt.Errorf("create service: %w", err)
		}
	}
	defer s.Close()

	// Restart after 5s, 10s and 30s if it fails; count failures afresh after a day.
	// "Non-crash failures" makes a startup error (exit code) count too, not just crashes.
	recovery := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	if err := s.SetRecoveryActions(recovery, 86400); err != nil {
		return fmt.Errorf("set recovery actions: %w", err)
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("set recovery actions: %w", err)
	}

	if err := lockDownFolders(out); err != nil {
		return err
	}

	// Register the Application event log source; "already exists" is fine.
	if err := eventlog.InstallAsEventCreate(Name, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil &&
		!strings.Contains(err.Error(), "exists") {
		return fmt.Errorf("register event log source: %w", err)
	}

	if err := s.Start(); err != nil {
		return fmt.Errorf("installed, but the service did not start: %w (see the Application event log, source %q)", err, Name)
	}
	if err := waitFor(s, svc.Running); err != nil {
		return fmt.Errorf("installed, but %w (see the Application event log, source %q)", err, Name)
	}
	fmt.Fprintf(out, "exmon service installed and running\n  program: %s\n  command: %s\n  data:    %s\n  config:  %s (or exmon.json next to the program)\n",
		exe, commandLine(exe, args), DataDir(), config.SearchPaths()[len(config.SearchPaths())-1])
	return nil
}

// Uninstall stops and removes the service. Files are left in place.
func Uninstall(out io.Writer) error {
	if !winsys.IsElevated() {
		return errors.New("uninstall must be run from an elevated (Run as administrator) prompt")
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the Service Control Manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(Name)
	if err != nil {
		fmt.Fprintln(out, "exmon is not installed")
		return nil
	}
	defer s.Close()
	if err := stopAndWait(s); err != nil {
		return err
	}
	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	eventlog.Remove(Name) // best effort
	fmt.Fprintf(out, "exmon service removed. Files were left in place:\n  program: %s\n  data:    %s\n", InstallDir(), DataDir())
	return nil
}

// Status describes the installed service. It works without elevation.
func Status(out io.Writer) error {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return fmt.Errorf("connect to the Service Control Manager: %w", err)
	}
	defer windows.CloseServiceHandle(scm)
	name, _ := syscall.UTF16PtrFromString(Name)
	h, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		fmt.Fprintln(out, "exmon service: not installed")
		return nil
	}
	s := &mgr.Service{Name: Name, Handle: h} // mgr.Service wraps any handle with the right access
	defer s.Close()

	st, err := s.Query()
	if err != nil {
		return err
	}
	c, err := s.Config()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "exmon service: %s\n  account: %s\n  start:   %s\n  command: %s\n  data:    %s\n",
		stateName(st.State), c.ServiceStartName, startName(c), c.BinaryPathName, DataDir())
	return nil
}

// copySelf copies the running exe to InstallDir (unless it is already running from
// there) and checks that the folder is admin-only.
func copySelf(out io.Writer) (string, error) {
	src, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := InstallDir()
	dst := filepath.Join(dir, Name+".exe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if !strings.EqualFold(filepath.Clean(src), filepath.Clean(dst)) {
		if err := copyFile(src, dst); err != nil {
			return "", fmt.Errorf("copy exmon.exe to %s: %w", dir, err)
		}
		fmt.Fprintln(out, "copied", src, "->", dst)
	}
	if err := winsys.ApplyDACL(dir, "O:BAD:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;"+readExecute+";;;BU)(A;OICI;"+readExecute+";;;OW)"); err != nil {
		return "", err
	}
	if ok, who, err := winsys.AdminOnlyWritable(dir); err != nil || !ok {
		return "", fmt.Errorf("%s must be writable only by administrators (%s can write; %v)", dir, who, err)
	}
	return dst, nil
}

// lockDownFolders creates exmon's data folders and sets their permissions:
//
//	%ProgramData%\exmon         SYSTEM, Administrators: full; NT SERVICE\exmon: modify; Users: read
//	%ProgramData%\exmon\config  SYSTEM, Administrators: full; NT SERVICE\exmon, Users: read
//
// Both are protected (no inheritance from ProgramData, which lets any user create
// files), owned by Administrators, and carry a read-only OWNER RIGHTS entry so whoever
// creates a file inside can't change its permissions later.
func lockDownFolders(out io.Writer) error {
	svcSID, err := winsys.ServiceSID(Name)
	if err != nil {
		return err
	}
	data := DataDir()
	cfgDir := filepath.Join(data, "config")
	for _, d := range []string{data, cfgDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	base := "O:BAD:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;" + readExecute + ";;;BU)(A;OICI;" + readExecute + ";;;OW)"
	if err := winsys.ApplyDACL(data, base+"(A;OICI;"+modify+";;;"+svcSID+")"); err != nil {
		return err
	}
	if err := winsys.ApplyDACL(cfgDir, base+"(A;OICI;"+readExecute+";;;"+svcSID+")"); err != nil {
		return err
	}
	fmt.Fprintln(out, "locked down", data, "and", cfgDir)
	return nil
}

// commandLine is the service command line: the quoted exe followed by the flags,
// each quoted the way Windows programs expect (the same as mgr.CreateService does).
func commandLine(exe string, args []string) string {
	var b strings.Builder
	b.WriteString(`"` + exe + `"`)
	for _, a := range args {
		b.WriteString(" " + syscall.EscapeArg(a))
	}
	return b.String()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst) // replaces the old exe; the service is stopped
}

// stopAndWait stops a service if it is running and waits until it has stopped.
func stopAndWait(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return nil
	}
	if st.State != svc.StopPending {
		if _, err := s.Control(svc.Stop); err != nil {
			return fmt.Errorf("stop service: %w", err)
		}
	}
	return waitFor(s, svc.Stopped)
}

// waitFor polls until the service reaches state, or times out.
func waitFor(s *mgr.Service, state svc.State) error {
	deadline := time.Now().Add(scmTimeout)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == state {
			return nil
		}
		if state == svc.Running && st.State == svc.Stopped {
			return errors.New("the service stopped during startup")
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("the service did not reach %s within %v", stateName(state), scmTimeout)
}

func stateName(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	case svc.Running:
		return "running"
	}
	return fmt.Sprintf("state %d", s)
}

func startName(c mgr.Config) string {
	switch c.StartType {
	case mgr.StartAutomatic:
		if c.DelayedAutoStart {
			return "automatic (delayed)"
		}
		return "automatic"
	case mgr.StartManual:
		return "manual"
	case mgr.StartDisabled:
		return "disabled"
	}
	return fmt.Sprintf("type %d", c.StartType)
}

// Trust is the config.TrustFunc used when running as a service: a config file is
// only used if it and its folder can be changed by administrators alone.
func Trust(path string) error {
	for _, p := range []string{path, filepath.Dir(path)} {
		ok, who, err := winsys.AdminOnlyWritable(p)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s can be changed by %s, not only administrators", p, who)
		}
	}
	return nil
}
