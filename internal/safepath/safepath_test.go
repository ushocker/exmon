package safepath

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithin(t *testing.T) {
	tests := []struct {
		root, path string
		want       bool
	}{
		{`C:\ProgramData\exmon`, `C:\ProgramData\exmon\exmon.log`, true},
		{`C:\ProgramData\exmon`, `c:\programdata\EXMON\incidents`, true}, // case doesn't matter
		{`C:\ProgramData\exmon`, `C:\ProgramData\exmon`, true},
		{`C:\ProgramData\exmon\`, `C:\ProgramData\exmon\x.log`, true},
		{`C:\ProgramData\exmon`, `C:\ProgramData\exmon-evil\x.log`, false}, // a prefix of the name isn't "inside"
		{`C:\ProgramData\exmon`, `C:\ProgramData\exmon\..\x.log`, false},
		{`C:\ProgramData\exmon`, `D:\exmon\x.log`, false},
	}
	for _, tt := range tests {
		if got := Within(tt.root, tt.path); got != tt.want {
			t.Errorf("Within(%q, %q) = %v, want %v", tt.root, tt.path, got, tt.want)
		}
	}
}

func TestCheckNoLinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckNoLinks(root, filepath.Join(root, "logs", "exmon.log")); err != nil {
		t.Errorf("plain folders: %v", err)
	}
	if err := CheckNoLinks(root, filepath.Join(root, "not", "yet", "created.log")); err != nil {
		t.Errorf("missing parts are fine: %v", err)
	}

	// A junction (needs no special rights to create) pointing somewhere else.
	elsewhere := t.TempDir()
	link := filepath.Join(root, "redirect")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, elsewhere).CombinedOutput(); err != nil {
		t.Skipf("can't create a junction here: %v %s", err, out)
	}
	err := CheckNoLinks(root, filepath.Join(link, "exmon.log"))
	if err == nil || !strings.Contains(err.Error(), "junction or link") {
		t.Errorf("path through a junction: got %v, want refused", err)
	}
}

func TestLocalDrive(t *testing.T) {
	for p, want := range map[string]bool{
		`C:\Windows\System32\svchost.exe`:                true,
		`c:/windows/x.exe`:                               true,
		`\\?\C:\Program Files\x\x.exe`:                   true,
		`\\server\share\svc.exe`:                         false,
		`//server/share/svc.exe`:                         false,
		`\\?\UNC\server\share\svc.exe`:                   false,
		`\\.\UNC\server\share\svc.exe`:                   false,
		`\\?\GLOBALROOT\Device\Mup\server\share\svc.exe`: false,
		`\\.\GLOBALROOT\Device\Mup\server\share\svc.exe`: false,
		`\\.\C:\x.exe`:                                   false, // device form: not needed, refused
		`UNC\server\share\svc.exe`:                       false,
		`C:relative.exe`:                                 false,
		`svc.exe`:                                        false,
		``:                                               false,
	} {
		if got := LocalDrive(p); got != want {
			t.Errorf("LocalDrive(%q) = %v, want %v", p, got, want)
		}
	}
}
