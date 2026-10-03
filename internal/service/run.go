package service

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
)

// EventLog writes to the Windows Application event log. It is how the service
// reports problems that happen before (or instead of) its own log file opening.
type EventLog interface {
	Info(eid uint32, msg string) error
	Warning(eid uint32, msg string) error
	Error(eid uint32, msg string) error
}

// Event IDs used in the Application log.
const (
	EventStarted = 1
	EventStopped = 2
	EventWarning = 3
	EventFailed  = 4
)

// App is exmon itself, as the service runs it. It must call ready once monitoring
// has started, and return when ctx is cancelled.
type App func(ctx context.Context, ready func(), elog EventLog) error

// stopWaitHint tells Windows how long a stop may take. Shutdown flushes incidents
// and makes one last remote send (up to 5s), so allow a comfortable margin.
const stopWaitHint = 30 * time.Second

// IsService reports whether this process was started by the Service Control Manager.
func IsService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

// Run runs app as the exmon service until Windows asks it to stop. It returns an
// error if the service could not run at all.
func Run(app App) error {
	elog, err := eventlog.Open(Name)
	if err != nil {
		return err
	}
	defer elog.Close()
	return svc.Run(Name, &handler{app: app, elog: elog})
}

type handler struct {
	app  App
	elog EventLog
}

// Execute is called by svc.Run. It reports the service's state to Windows through
// status and receives requests (stop, shutdown, interrogate) on requests. The two
// return values become the service's exit code; a non-zero code counts as a failure,
// so the recovery actions restart the service.
func (h *handler) Execute(args []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending, WaitHint: uint32(stopWaitHint.Milliseconds())}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- h.app(ctx, func() { close(ready) }, h.elog)
	}()

	// Starting: wait until the app is monitoring, or fails.
	select {
	case <-ready:
	case err := <-done:
		return h.failed(err)
	}
	status <- svc.Status{State: svc.Running, Accepts: accepts}

	for {
		select {
		case err := <-done: // exmon stopped by itself: something went wrong
			if err == nil {
				err = fmt.Errorf("exmon stopped unexpectedly")
			}
			return h.failed(err)
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				status <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				cancel()
				return h.stopping(done, status)
			}
		}
	}
}

// stopping waits for the app to shut down, telling Windows every second that it is
// still making progress (the "checkpoint"), so the stop isn't treated as hung.
func (h *handler) stopping(done <-chan error, status chan<- svc.Status) (bool, uint32) {
	hint := uint32(stopWaitHint.Milliseconds())
	checkpoint := uint32(1)
	status <- svc.Status{State: svc.StopPending, CheckPoint: checkpoint, WaitHint: hint}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				return h.failed(err)
			}
			h.elog.Info(EventStopped, "exmon stopped")
			status <- svc.Status{State: svc.Stopped}
			return false, 0
		case <-tick.C:
			checkpoint++
			status <- svc.Status{State: svc.StopPending, CheckPoint: checkpoint, WaitHint: hint}
		}
	}
}

func (h *handler) failed(err error) (bool, uint32) {
	h.elog.Error(EventFailed, "exmon failed: "+err.Error())
	return true, 1 // service-specific exit code 1: counts as a failure for recovery
}
