package winsys

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"exmon/internal/safepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ServiceInfo is what the services monitor reports about one Windows service.
type ServiceInfo struct {
	Name        string // the service's key name, as Windows spells it
	DisplayName string
	State       string // "running", "stopped", "start_pending", "stop_pending", "paused", ...
	StartType   string // "auto", "delayed_auto", "manual", "disabled", "boot", "system"
	PID         uint32 // 0 when not running
	ExitCode    uint32 // Win32 exit code from the last stop (0 = clean)
	ServiceExit uint32 // service-specific exit code, when ExitCode is ERROR_SERVICE_SPECIFIC_ERROR
	Shared      bool   // runs inside a shared process (svchost)
	Driver      bool   // a kernel or file-system driver
	BinaryPath  string // the configured command line
	ServiceDLL  string // for svchost services: the DLL that is the service (Parameters\ServiceDll)
}

// ErrServiceNotFound means no service by that name is installed.
var ErrServiceNotFound = errors.New("not installed")

// QueryService reads a service's state and configuration. It asks only for
// SERVICE_QUERY_STATUS and SERVICE_QUERY_CONFIG, which ordinary users (and the
// LocalService account exmon runs as) have on almost every service.
func QueryService(name string) (ServiceInfo, error) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return ServiceInfo{}, fmt.Errorf("open the service manager: %w", err)
	}
	defer windows.CloseServiceHandle(scm)
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return ServiceInfo{}, err
	}
	h, err := windows.OpenService(scm, n, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return ServiceInfo{}, ErrServiceNotFound
	}
	if err != nil {
		return ServiceInfo{}, err
	}
	defer windows.CloseServiceHandle(h)

	st, err := serviceStatus(h)
	if err != nil {
		return ServiceInfo{}, fmt.Errorf("query status: %w", err)
	}
	cfg, err := serviceConfig(h)
	if err != nil {
		return ServiceInfo{}, fmt.Errorf("query config: %w", err)
	}
	info := ServiceInfo{
		Name:        name,
		DisplayName: windows.UTF16PtrToString(cfg.DisplayName),
		State:       stateName(st.CurrentState),
		StartType:   startTypeName(cfg.StartType),
		PID:         st.ProcessId,
		ExitCode:    st.Win32ExitCode,
		ServiceExit: st.ServiceSpecificExitCode,
		Shared:      cfg.ServiceType&windows.SERVICE_WIN32_SHARE_PROCESS != 0,
		Driver:      cfg.ServiceType&(windows.SERVICE_KERNEL_DRIVER|windows.SERVICE_FILE_SYSTEM_DRIVER) != 0,
		BinaryPath:  windows.UTF16PtrToString(cfg.BinaryPathName),
	}
	if info.StartType == "auto" && delayedAutoStart(h) {
		info.StartType = "delayed_auto"
	}
	if !info.Driver {
		// Not only shared services: on current Windows most svchost services run
		// in a process of their own, but the code is still their ServiceDll.
		info.ServiceDLL = serviceDLL(name)
	}
	return info, nil
}

// serviceStatus calls QueryServiceStatusEx for the process-level status.
func serviceStatus(h windows.Handle) (windows.SERVICE_STATUS_PROCESS, error) {
	var st windows.SERVICE_STATUS_PROCESS
	var needed uint32
	err := windows.QueryServiceStatusEx(h, windows.SC_STATUS_PROCESS_INFO,
		(*byte)(unsafe.Pointer(&st)), uint32(unsafe.Sizeof(st)), &needed)
	return st, err
}

// serviceConfig calls QueryServiceConfig twice: once for the size, then for the
// data. The strings it points to live in the same buffer, so they are copied out
// (UTF16PtrToString) before the buffer goes.
func serviceConfig(h windows.Handle) (*windows.QUERY_SERVICE_CONFIG, error) {
	var needed uint32
	err := windows.QueryServiceConfig(h, nil, 0, &needed)
	if err != nil && !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		return nil, err
	}
	buf := make([]byte, needed)
	cfg := (*windows.QUERY_SERVICE_CONFIG)(unsafe.Pointer(&buf[0]))
	if err := windows.QueryServiceConfig(h, cfg, needed, &needed); err != nil {
		return nil, err
	}
	return cfg, nil
}

// delayedAutoStart reports whether an automatic service starts delayed.
func delayedAutoStart(h windows.Handle) bool {
	var info windows.SERVICE_DELAYED_AUTO_START_INFO
	var needed uint32
	err := windows.QueryServiceConfig2(h, windows.SERVICE_CONFIG_DELAYED_AUTO_START_INFO,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), &needed)
	return err == nil && info.IsDelayedAutoStartUp != 0
}

// serviceDLL reads Parameters\ServiceDll, the DLL a svchost service really is.
func serviceDLL(name string) string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\`+name+`\Parameters`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("ServiceDll")
	if err != nil {
		return ""
	}
	return expandPath(v)
}

func stateName(s uint32) string {
	switch s {
	case windows.SERVICE_STOPPED:
		return "stopped"
	case windows.SERVICE_START_PENDING:
		return "start_pending"
	case windows.SERVICE_STOP_PENDING:
		return "stop_pending"
	case windows.SERVICE_RUNNING:
		return "running"
	case windows.SERVICE_CONTINUE_PENDING:
		return "continue_pending"
	case windows.SERVICE_PAUSE_PENDING:
		return "pause_pending"
	case windows.SERVICE_PAUSED:
		return "paused"
	}
	return fmt.Sprintf("state_%d", s)
}

func startTypeName(t uint32) string {
	switch t {
	case windows.SERVICE_BOOT_START:
		return "boot"
	case windows.SERVICE_SYSTEM_START:
		return "system"
	case windows.SERVICE_AUTO_START:
		return "auto"
	case windows.SERVICE_DEMAND_START:
		return "manual"
	case windows.SERVICE_DISABLED:
		return "disabled"
	}
	return fmt.Sprintf("start_%d", t)
}

// ProcessImagePath returns the full path of a running process's exe. Opening
// another account's process can be refused (e.g. SYSTEM processes, from
// LocalService); callers fall back to the configured path then.
func ProcessImagePath(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}

// ExePathFromCommandLine finds the program in a service's configured command
// line: "C:\Program Files\X\x.exe" -k, C:\Windows\system32\svchost.exe -k netsvcs,
// an unquoted path with spaces (Windows tries each space in turn), or a driver
// path such as \SystemRoot\System32\drivers\x.sys or system32\drivers\x.sys.
func ExePathFromCommandLine(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	if cmd[0] == '"' {
		if end := strings.IndexByte(cmd[1:], '"'); end >= 0 {
			return expandPath(cmd[1 : end+1])
		}
		return expandPath(strings.Trim(cmd, `"`))
	}
	// Unquoted: the shortest prefix (ending at a space) that names an existing
	// file, the way CreateProcess resolves it; else up to the first ".exe ".
	// Only local paths are looked at: a stat of \\server\... connects to it.
	for i := 0; i <= len(cmd); i++ {
		if i < len(cmd) && cmd[i] != ' ' {
			continue
		}
		p := expandPath(cmd[:i])
		if !safepath.LocalDrive(p) {
			continue
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
		if !strings.HasSuffix(strings.ToLower(p), ".exe") {
			if _, err := os.Stat(p + ".exe"); err == nil {
				return p + ".exe"
			}
		}
	}
	if i := strings.Index(strings.ToLower(cmd), ".exe"); i >= 0 {
		return expandPath(cmd[:i+4])
	}
	return expandPath(strings.Fields(cmd)[0])
}

// expandPath expands %VARS% and the NT-style prefixes service and driver paths use.
func expandPath(p string) string {
	if e, err := registry.ExpandString(p); err == nil {
		p = e
	}
	win := os.Getenv("SystemRoot")
	lower := strings.ToLower(p)
	switch {
	case strings.HasPrefix(p, `\??\`):
		p = p[4:]
	case strings.HasPrefix(lower, `\systemroot\`):
		p = filepath.Join(win, p[len(`\systemroot\`):])
	case strings.HasPrefix(lower, `system32\`) || strings.HasPrefix(lower, `syswow64\`):
		p = filepath.Join(win, p)
	}
	return p
}
