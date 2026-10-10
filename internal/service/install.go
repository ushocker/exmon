// Package service runs exmon as a Windows Service: install, uninstall, status, and
// the handler the Service Control Manager talks to while the service runs.
//
// The service runs as LocalService with its own service SID (NT SERVICE\exmon).
// The two things that need more rights, using the TPM-held client certificate key
// and replacing exmon.exe during an update, are done by a second, small service,
// exmon-helper (LocalSystem), which exmon may only start. The installer copies both
// programs to Program Files and locks down exmon's folders, so a standard user can
// neither replace a program, plant a config nor read the config (which may hold
// the Azure Monitor secret).
package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
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
	helperName  = "exmon-helper"
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
	// --proxy is for install only: it sets the service's proxy environment and is
	// not passed on to the service's command line.
	args, proxy, proxySet, err := takeProxyFlag(args)
	if err != nil {
		return err
	}
	// Check the flags now, so a typo fails here rather than inside the service.
	opts, err := config.Load(args)
	if err != nil {
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
		SidType:          windows.SERVICE_SID_TYPE_UNRESTRICTED, // also gives it the NT SERVICE\exmon identity
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
	if err := installHelper(m, opts, out); err != nil {
		return err
	}
	if proxySet {
		if err := setProxy(proxy, out); err != nil {
			return err
		}
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

// helperSDDL is who may do what with the exmon-helper service: SYSTEM and
// Administrators everything; NT SERVICE\exmon only query its status and START it
// (LC, RP); interactive users only look at it.
func helperSDDL(svcSID string) string {
	return "D:(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)" +
		"(A;;LCRP;;;" + svcSID + ")(A;;CCLCRC;;;IU)"
}

// installHelper installs (or updates) exmon-helper: the small LocalSystem
// service that applies updates and grants exmon use of its certificate key. Its
// settings are taken from exmon's control settings now and baked into its
// command line; run install again after changing them.
func installHelper(m *mgr.Mgr, opts config.Options, out io.Writer) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	src := filepath.Join(filepath.Dir(self), helperName+".exe")
	if _, err := os.Stat(src); err != nil {
		if opts.Control.Enabled {
			return fmt.Errorf("%s must be next to exmon.exe: the control plane needs it", src)
		}
		fmt.Fprintf(out, "note: %s not found; updates and key access for the control plane won't work until it is installed\n", src)
		return nil
	}
	dst := filepath.Join(InstallDir(), helperName+".exe")
	if !strings.EqualFold(filepath.Clean(src), filepath.Clean(dst)) {
		if s, err := m.OpenService(helperName); err == nil {
			stopAndWait(s) // it may be mid-run
			s.Close()
		}
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copy %s: %w", helperName, err)
		}
	}
	u := opts.Control.Update
	args := []string{
		"--cert-subject=" + opts.Control.Cert.SubjectCN,
		"--cert-issuer=" + opts.Control.Cert.IssuerCN,
		"--publisher=" + u.PublisherCN,
		fmt.Sprintf("--require-authenticode=%v", u.RequireAuthenticode),
		fmt.Sprintf("--health-minutes=%d", u.HealthMinutes),
		fmt.Sprintf("--max-size-mb=%d", u.MaxSizeMB),
	}
	cfg := mgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        mgr.StartAutomatic, // once at every boot (key access), and on demand from exmon
		DelayedAutoStart: true,
		ErrorControl:     mgr.ErrorNormal,
		DisplayName:      "exmon helper",
		Description:      "Applies exmon updates and lets exmon use its client certificate. Runs briefly when needed.",
		ServiceStartName: "LocalSystem",
	}
	s, err := m.OpenService(helperName)
	if err == nil {
		cfg.BinaryPathName = commandLine(dst, args)
		err = s.UpdateConfig(cfg)
	} else {
		s, err = m.CreateService(helperName, dst, cfg, args...)
	}
	if err != nil {
		return fmt.Errorf("install %s: %w", helperName, err)
	}
	defer s.Close()
	sid, err := winsys.ServiceSID(Name)
	if err != nil {
		return err
	}
	if err := winsys.SetServiceDACL(s.Handle, helperSDDL(sid)); err != nil {
		return fmt.Errorf("set %s permissions: %w", helperName, err)
	}
	if err := eventlog.InstallAsEventCreate(helperName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil &&
		!strings.Contains(err.Error(), "exists") {
		return fmt.Errorf("register event log source %s: %w", helperName, err)
	}
	if err := s.Start(); err != nil && !strings.Contains(err.Error(), "already") {
		fmt.Fprintf(out, "note: %s didn't start: %v\n", helperName, err)
	}
	fmt.Fprintf(out, "%s installed (LocalSystem; exmon may only start it)\n", helperName)
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
	if h, err := m.OpenService(helperName); err == nil {
		stopAndWait(h)
		if err := h.Delete(); err != nil {
			fmt.Fprintf(out, "note: couldn't remove %s: %v\n", helperName, err)
		}
		h.Close()
		eventlog.Remove(helperName)
	}
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

	// The helper normally shows "stopped": it runs only while it has work.
	hn, _ := syscall.UTF16PtrFromString(helperName)
	if hh, err := windows.OpenService(scm, hn, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG); err == nil {
		hs := &mgr.Service{Name: helperName, Handle: hh}
		defer hs.Close()
		if hst, err := hs.Query(); err == nil {
			hc, _ := hs.Config()
			fmt.Fprintf(out, "%s: %s (runs when needed)\n  account: %s\n", helperName, stateName(hst.State), hc.ServiceStartName)
		}
	} else {
		fmt.Fprintf(out, "%s: not installed\n", helperName)
	}
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

// Folder permissions, built as SDDL. Every folder is protected (no inheritance
// from ProgramData, which lets any user create files), owned by Administrators,
// and carries a read-only OWNER RIGHTS (OW) entry, so whoever creates a file inside
// can't change its permissions later. svc is NT SERVICE\exmon's SID ("" before
// the service exists: then the folders are admin-only until install runs).
func folderSDDL(usersRead bool, svc, svcRights string) string {
	d := "O:BAD:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;" + readExecute + ";;;OW)"
	if usersRead {
		d += "(A;OICI;" + readExecute + ";;;BU)"
	}
	if svc != "" && svcRights != "" {
		d += "(A;OICI;" + svcRights + ";;;" + svc + ")"
	}
	return d
}

// LockedFolders lists exmon's folders under DataDir and their permissions:
//
//	folder                              holds                         exmon service   Users
//	%ProgramData%\exmon                 logs, incidents, spools       modify          read
//	  config                            exmon.json (holds secrets)    read            -
//	  config\cloud                      signed cloud config           modify          -
//	  state                             control-plane state           modify          -
//	  update                            (parent of the next four)     -               -
//	  update\incoming                   downloads + update request    modify          -
//	  update\status                     the helper's trial record     read            -
//	  update\verified, helper-state     the helper's own files        -               -
//
// SYSTEM and Administrators have full control everywhere. exmon-helper (SYSTEM)
// never trusts what exmon can write: see internal/helper.
func LockedFolders(svc string) map[string]string {
	data := DataDir()
	j := func(p ...string) string { return filepath.Join(append([]string{data}, p...)...) }
	return map[string]string{
		data:                        folderSDDL(true, svc, modify),
		j("config"):                 folderSDDL(false, svc, readExecute),
		j("config", "cloud"):        folderSDDL(false, svc, modify),
		j("state"):                  folderSDDL(false, svc, modify),
		j("update"):                 folderSDDL(false, "", ""),
		j("update", "incoming"):     folderSDDL(false, svc, modify),
		j("update", "status"):       folderSDDL(false, svc, readExecute),
		j("update", "verified"):     folderSDDL(false, "", ""),
		j("update", "helper-state"): folderSDDL(false, "", ""),
	}
}

// LockDown creates exmon's data folders with their permissions (see
// LockedFolders), for commands that write there before the service is installed,
// such as `exmon enroll`. It needs elevation.
func LockDown(out io.Writer) error { return lockDownFolders(out) }

// lockDownFolders creates exmon's data folders and sets their permissions (see
// LockedFolders). Parents are done before children, because setting a protected
// DACL on a parent re-applies inherited entries below it.
func lockDownFolders(out io.Writer) error {
	svc, err := winsys.ServiceSID(Name)
	if err != nil {
		svc = "" // the service doesn't exist yet; install sets the service's access later
	}
	folders := LockedFolders(svc)
	paths := make([]string, 0, len(folders))
	for p := range folders {
		paths = append(paths, p)
	}
	slices.Sort(paths) // a parent sorts before its children
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return err
		}
		if err := winsys.ApplyDACL(p, folders[p]); err != nil {
			return fmt.Errorf("set permissions on %s: %w", p, err)
		}
	}
	fmt.Fprintln(out, "locked down", DataDir())
	return nil
}

// takeProxyFlag removes --proxy <url> (or --proxy=<url>) from args. set reports
// whether it was given at all; an empty url removes a previously set proxy.
func takeProxyFlag(args []string) (rest []string, proxy string, set bool, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "proxy" {
			rest = append(rest, a)
			continue
		}
		if !hasValue {
			if i+1 >= len(args) {
				return nil, "", false, errors.New("--proxy needs a value, e.g. --proxy http://proxy.corp:8080 (or --proxy= to remove it)")
			}
			i++
			value = args[i]
		}
		if value != "" && !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
			return nil, "", false, fmt.Errorf("--proxy %q must start with http:// or https://", value)
		}
		proxy, set = value, true
	}
	return rest, proxy, set, nil
}

// setProxy writes HTTPS_PROXY and HTTP_PROXY into the service's environment (the
// "Environment" value of its registry key), or removes them for "". The service
// account has no per-user proxy settings, so this is how the service's HTTPS and
// MQTT-over-WebSocket connections find a proxy.
func setProxy(proxy string, out io.Writer) error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\`+Name, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("set service proxy: %w", err)
	}
	defer k.Close()
	if proxy == "" {
		if err := k.DeleteValue("Environment"); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("remove service proxy: %w", err)
		}
		fmt.Fprintln(out, "removed the service's proxy setting")
		return nil
	}
	env := []string{"HTTPS_PROXY=" + proxy, "HTTP_PROXY=" + proxy}
	if err := k.SetStringsValue("Environment", env); err != nil {
		return fmt.Errorf("set service proxy: %w", err)
	}
	fmt.Fprintln(out, "service proxy set to", proxy)
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
