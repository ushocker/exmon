package winsys

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// descriptor builds a security descriptor from SDDL for tests.
func descriptor(t *testing.T, sddl string) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	return sd
}

func TestCheckDescriptor(t *testing.T) {
	tests := []struct {
		name string
		sddl string
		ok   bool
		who  string
	}{
		{"admin only", "O:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;0x1200a9;;;BU)", true, ""},
		{"users can write", "O:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;0x1301bf;;;BU)", false, "Users"},
		{"everyone full", "O:BAD:P(A;;FA;;;SY)(A;;GA;;;WD)", false, "Everyone"},
		{"untrusted owner", "O:BUD:P(A;;FA;;;SY)(A;;FA;;;BA)", false, "owner"},
		{"untrusted owner neutralised by OWNER RIGHTS", "O:BUD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;0x1200a9;;;OW)", true, ""},
		{"OWNER RIGHTS with write", "O:BUD:P(A;;FA;;;SY)(A;;FA;;;OW)", false, "OWNER RIGHTS"},
		{"inherit-only write ignored", "O:BAD:P(A;;FA;;;SY)(A;OICIIO;FA;;;CO)(A;;0x1200a9;;;BU)", true, ""},
		{"deny entries ignored", "O:BAD:P(D;;FA;;;BU)(A;;FA;;;SY)", true, ""},
		{"null DACL", "O:BA", false, "everyone"},
	}
	for _, tt := range tests {
		ok, who, err := checkDescriptor(descriptor(t, tt.sddl))
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if ok != tt.ok || !strings.Contains(who, tt.who) {
			t.Errorf("%s: ok=%v who=%q, want ok=%v who containing %q", tt.name, ok, who, tt.ok, tt.who)
		}
	}
}

// TestAdminOnlyWritableRealFile checks a file the test user owns: it must not count
// as admin-only, even after its permissions are locked down, because its owner (the
// test user) could change them back.
func TestAdminOnlyWritableRealFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.json")
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := AdminOnlyWritable(p); err != nil || ok {
		t.Errorf("user-owned temp file: ok=%v err=%v, want not admin-only", ok, err)
	}
	if err := ApplyDACL(p, "D:P(A;;FA;;;SY)(A;;FA;;;BA)"); err != nil {
		t.Fatal(err)
	}
	ok, who, err := AdminOnlyWritable(p)
	if err != nil || ok || !strings.Contains(who, "owner") {
		t.Errorf("locked down but user-owned: ok=%v who=%q err=%v, want untrusted owner", ok, who, err)
	}

	// A read-only OWNER RIGHTS entry takes away the owner's right to change the
	// permissions, which is how the installer makes admin-written files trusted.
	if err := ApplyDACL(p, "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;0x1200a9;;;OW)"); err != nil {
		t.Fatal(err)
	}
	if ok, who, err := AdminOnlyWritable(p); err != nil || !ok {
		t.Errorf("owner neutralised: ok=%v who=%q err=%v, want admin-only", ok, who, err)
	}
}

func TestConsoleUser(t *testing.T) {
	// In an interactive session this is the person running the tests; on a build
	// server with nobody logged on it may be empty.
	t.Logf("console user: %q, elevated: %v", ConsoleUser(), IsElevated())
}

// TestWatchPower checks registration and unregistration work. Sleep itself can't be
// triggered from a test, so the events are tested in internal/power with fakes.
func TestWatchPower(t *testing.T) {
	ch := make(chan PowerEvent, 1)
	w, err := WatchPower(ch)
	if err != nil {
		t.Fatalf("WatchPower: %v", err)
	}
	w.Stop()
	w.Stop() // safe twice
	if PowerSuspend.String() != "suspend" || PowerResume.String() != "resume" {
		t.Error("PowerEvent names")
	}
}

// A file moved in from a locked folder keeps that folder's permissions until
// InheritDACL gives it its new folder's.
func TestInheritDACL(t *testing.T) {
	dir := t.TempDir()
	locked, open := filepath.Join(dir, "locked"), filepath.Join(dir, "open")
	for _, d := range []string{locked, open} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// "locked" grants only SYSTEM; "open" also grants Users read (inheritable).
	if err := ApplyDACL(locked, "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;OW)"); err != nil {
		t.Fatal(err)
	}
	if err := ApplyDACL(open, "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;OW)(A;OICI;0x1200a9;;;BU)"); err != nil {
		t.Fatal(err)
	}
	src, dst := filepath.Join(locked, "x.exe"), filepath.Join(open, "x.exe")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(src, dst); err != nil {
		t.Fatal(err)
	}
	if usersCanRead(t, dst) {
		t.Skip("this Windows recomputed permissions on rename; nothing to test")
	}
	if err := InheritDACL(dst); err != nil {
		t.Fatal(err)
	}
	if !usersCanRead(t, dst) {
		t.Error("after InheritDACL the file should have its folder's Users read entry")
	}
}

func usersCanRead(t *testing.T, path string) bool {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(sd.String(), ";;;BU)")
}
