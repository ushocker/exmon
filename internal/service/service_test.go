package service

import (
	"context"
	"errors"
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
