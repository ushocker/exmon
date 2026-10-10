package helper

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

// ServiceName is the helper's own service.
const ServiceName = "exmon-helper"

// RunService runs one helper pass as the Windows service and then stops, so it
// is only running while it has work. Its log goes to the Application event log
// (source exmon-helper).
func RunService(cfg Config, d Deps) error {
	elog, err := eventlog.Open(ServiceName)
	if err != nil {
		return err
	}
	defer elog.Close()
	d.Log = func(warning bool, msg string) {
		if warning {
			elog.Warning(2, msg)
		} else {
			elog.Info(1, msg)
		}
	}
	return svc.Run(ServiceName, &handler{cfg: cfg, d: d})
}

type handler struct {
	cfg Config
	d   Deps
}

// Execute reports Running, does the pass in a goroutine, and stops when it ends
// or when Windows asks.
func (h *handler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, h.cfg, h.d) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			if err != nil {
				h.d.Log(true, err.Error())
			}
			status <- svc.Status{State: svc.StopPending}
			return false, 0
		case r := <-requests:
			switch r.Cmd {
			case svc.Interrogate:
				status <- r.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, WaitHint: 10000}
				cancel()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
				}
				return false, 0
			}
		}
	}
}

// Services is the real ServiceControl, through the Service Control Manager.
type Services struct{}

func (Services) Stop(name string) error {
	m, s, err := open(name)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State == svc.Stopped {
		return nil
	}
	if _, err := s.Control(svc.Stop); err != nil {
		return err
	}
	return waitState(s, svc.Stopped)
}

func (Services) Start(name string) error {
	m, s, err := open(name)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State == svc.Running {
		return nil
	}
	if err := s.Start(); err != nil {
		return err
	}
	return waitState(s, svc.Running)
}

func open(name string) (*mgr.Mgr, *mgr.Service, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, nil, err
	}
	s, err := m.OpenService(name)
	if err != nil {
		m.Disconnect()
		return nil, nil, err
	}
	return m, s, nil
}

func waitState(s *mgr.Service, want svc.State) error {
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
	}
	return fmt.Errorf("service didn't reach state %d within a minute", want)
}
