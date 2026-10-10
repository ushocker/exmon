package winsys

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Dnscache and EventLog run on every Windows PC and any user may query them.
func TestQueryService(t *testing.T) {
	for _, name := range []string{"EventLog", "Dnscache"} {
		info, err := QueryService(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.State != "running" || info.BinaryPath == "" || info.DisplayName == "" {
			t.Errorf("%s: %+v", name, info)
		}
		if !strings.HasSuffix(strings.ToLower(info.ServiceDLL), ".dll") {
			t.Errorf("%s: svchost service without a ServiceDll: %+v", name, info)
		}
		t.Logf("%+v", info)
	}
	if _, err := QueryService("exmon-no-such-service"); !errors.Is(err, ErrServiceNotFound) {
		t.Errorf("missing service: %v", err)
	}
}

func TestExePathFromCommandLine(t *testing.T) {
	win := os.Getenv("SystemRoot")
	svchost := filepath.Join(win, "System32", "svchost.exe")
	tests := []struct{ cmd, want string }{
		{`"C:\Program Files\X Y\x.exe" --flag "a b"`, `C:\Program Files\X Y\x.exe`},
		{svchost + ` -k netsvcs -p`, svchost},
		{`%SystemRoot%\System32\svchost.exe -k LocalService`, filepath.Join(win, `System32\svchost.exe`)},
		{`\SystemRoot\System32\drivers\tcpip.sys`, filepath.Join(win, `System32\drivers\tcpip.sys`)},
		{`system32\drivers\tcpip.sys`, filepath.Join(win, `system32\drivers\tcpip.sys`)},
		{`\??\C:\Windows\x.sys`, `C:\Windows\x.sys`},
		{`C:\Nowhere At All\svc.exe -run`, `C:\Nowhere At All\svc.exe`},
	}
	for _, tt := range tests {
		if got := ExePathFromCommandLine(tt.cmd); !strings.EqualFold(got, tt.want) {
			t.Errorf("%q: got %q, want %q", tt.cmd, got, tt.want)
		}
	}
}

func TestReadFileVersion(t *testing.T) {
	v, err := ReadFileVersion(filepath.Join(os.Getenv("SystemRoot"), "System32", "svchost.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v.FileVersion, "10.0.") || v.Company == "" {
		t.Errorf("%+v", v)
	}
	t.Logf("%+v", v)
	if _, err := ReadFileVersion(filepath.Join(t.TempDir(), "none.exe")); err == nil {
		t.Error("a missing file should fail")
	}
}
