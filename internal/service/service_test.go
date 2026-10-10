package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

func TestCommandLine(t *testing.T) {
	got := commandLine(`C:\Program Files\exmon\exmon.exe`, []string{"--log-path", `D:\My Logs\exmon.log`, "--baseline"})
	want := `"C:\Program Files\exmon\exmon.exe" --log-path "D:\My Logs\exmon.log" --baseline`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// fakeLog records event log messages.
type fakeLog struct{ msgs []string }

func (f *fakeLog) Info(_ uint32, m string) error    { f.msgs = append(f.msgs, "I:"+m); return nil }
func (f *fakeLog) Warning(_ uint32, m string) error { f.msgs = append(f.msgs, "W:"+m); return nil }
func (f *fakeLog) Error(_ uint32, m string) error   { f.msgs = append(f.msgs, "E:"+m); return nil }

// runHandler drives Execute like the Service Control Manager would.
func runHandler(t *testing.T, app App, sendStop bool) (bool, uint32, []svc.State, *fakeLog) {
	t.Helper()
	elog := &fakeLog{}
	h := &handler{app: app, elog: elog}
	requests := make(chan svc.ChangeRequest, 1)
	status := make(chan svc.Status, 100)
	var states []svc.State
	collected := make(chan struct{})
	go func() {
		for s := range status {
			states = append(states, s.State)
			if s.State == svc.Running && sendStop {
				requests <- svc.ChangeRequest{Cmd: svc.Stop}
			}
		}
		close(collected)
	}()
	special, code := h.Execute(nil, requests, status)
	close(status)
	<-collected
	return special, code, states, elog
}

func TestHandlerStartAndStop(t *testing.T) {
	app := func(ctx context.Context, ready func(), _ EventLog) error {
		ready()
		<-ctx.Done()
		time.Sleep(1200 * time.Millisecond) // a slow shutdown: Windows gets checkpoints meanwhile
		return nil
	}
	special, code, states, elog := runHandler(t, app, true)
	if special || code != 0 {
		t.Errorf("exit = %v/%d, want clean", special, code)
	}
	if states[0] != svc.StartPending || states[1] != svc.Running || states[len(states)-1] != svc.Stopped {
		t.Errorf("states %v", states)
	}
	pending := 0
	for _, s := range states {
		if s == svc.StopPending {
			pending++
		}
	}
	if pending < 2 {
		t.Errorf("want repeated StopPending checkpoints during a slow stop, got %v", states)
	}
	if len(elog.msgs) == 0 || elog.msgs[len(elog.msgs)-1] != "I:exmon stopped" {
		t.Errorf("event log %v", elog.msgs)
	}
}

func TestHandlerStartupFailure(t *testing.T) {
	app := func(context.Context, func(), EventLog) error { return errors.New("bad config") }
	special, code, states, elog := runHandler(t, app, false)
	if !special || code != 1 {
		t.Errorf("exit = %v/%d, want a service-specific failure", special, code)
	}
	for _, s := range states {
		if s == svc.Running {
			t.Error("must not report Running when startup failed")
		}
	}
	if len(elog.msgs) != 1 || elog.msgs[0] != "E:exmon failed: bad config" {
		t.Errorf("event log %v", elog.msgs)
	}
}

// TestHandlerPreShutdown checks Windows' early shutdown notice stops exmon cleanly.
func TestHandlerPreShutdown(t *testing.T) {
	elog := &fakeLog{}
	h := &handler{app: func(ctx context.Context, ready func(), _ EventLog) error {
		ready()
		<-ctx.Done()
		return nil
	}, elog: elog}
	requests := make(chan svc.ChangeRequest, 1)
	status := make(chan svc.Status, 100)
	go func() {
		for s := range status {
			if s.State == svc.Running {
				if s.Accepts&svc.AcceptPreShutdown == 0 {
					t.Error("the service must accept pre-shutdown")
				}
				requests <- svc.ChangeRequest{Cmd: svc.PreShutdown}
			}
		}
	}()
	special, code := h.Execute(nil, requests, status)
	close(status)
	if special || code != 0 {
		t.Errorf("exit = %v/%d, want a clean stop", special, code)
	}
}

func TestTakeProxyFlag(t *testing.T) {
	rest, proxy, set, err := takeProxyFlag([]string{"--probe-interval-seconds", "60", "--proxy", "http://p:8080", "--baseline"})
	if err != nil || !set || proxy != "http://p:8080" || strings.Join(rest, " ") != "--probe-interval-seconds 60 --baseline" {
		t.Errorf("got %v %q %v %v", rest, proxy, set, err)
	}
	if _, proxy, set, _ := takeProxyFlag([]string{"-proxy="}); !set || proxy != "" {
		t.Errorf("--proxy= should mean remove: %q %v", proxy, set)
	}
	if _, _, set, _ := takeProxyFlag([]string{"--baseline"}); set {
		t.Error("no --proxy given")
	}
	for _, bad := range [][]string{{"--proxy"}, {"--proxy", "proxy.corp:8080"}} {
		if _, _, _, err := takeProxyFlag(bad); err == nil {
			t.Errorf("%v should fail", bad)
		}
	}
}

func TestFolderPermissions(t *testing.T) {
	const svc = "S-1-5-80-1-2-3-4-5"
	folders := LockedFolders(svc)
	data := DataDir()
	want := map[string]struct {
		users bool
		svc   string // "" = no access
	}{
		data:                                          {true, modify},
		filepath.Join(data, "config"):                 {false, readExecute},
		filepath.Join(data, "config", "cloud"):        {false, modify},
		filepath.Join(data, "state"):                  {false, modify},
		filepath.Join(data, "update"):                 {false, ""},
		filepath.Join(data, "update", "incoming"):     {false, modify},
		filepath.Join(data, "update", "status"):       {false, readExecute},
		filepath.Join(data, "update", "verified"):     {false, ""},
		filepath.Join(data, "update", "helper-state"): {false, ""},
	}
	if len(folders) != len(want) {
		t.Errorf("folders = %d, want %d", len(folders), len(want))
	}
	for path, w := range want {
		sddl, ok := folders[path]
		if !ok {
			t.Errorf("%s missing", path)
			continue
		}
		if got := strings.Contains(sddl, ";;;BU)"); got != w.users {
			t.Errorf("%s: users can read = %v, want %v", path, got, w.users)
		}
		hasSvc := strings.Contains(sddl, ";;;"+svc+")")
		if (w.svc != "") != hasSvc || (hasSvc && !strings.Contains(sddl, w.svc+";;;"+svc)) {
			t.Errorf("%s: service access wrong in %s (want %q)", path, sddl, w.svc)
		}
	}
	// Before the service exists, nothing grants it access.
	for p, sddl := range LockedFolders("") {
		if strings.Contains(sddl, "S-1-5-80") {
			t.Errorf("%s grants an unknown service: %s", p, sddl)
		}
	}
}

func TestHelperSDDL(t *testing.T) {
	sddl := helperSDDL("S-1-5-80-9")
	if !strings.Contains(sddl, "(A;;LCRP;;;S-1-5-80-9)") || strings.Contains(sddl, "WP;;;S-1-5-80-9") {
		t.Errorf("exmon must only query and start the helper: %s", sddl)
	}
}
