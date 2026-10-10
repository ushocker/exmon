package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"exmon/internal/config"
	"exmon/internal/control"
	"exmon/internal/event"
	"exmon/internal/probe"
	"exmon/internal/service"
	"exmon/internal/signdoc"
	"exmon/internal/updater"
	"exmon/internal/winsys"
)

// newControl builds the control-plane client with its dependencies: the built-in
// signing keys, the version state file, and the fixed list of commands a signed
// command document may run. prober is nil when probes are off.
func newControl(ctx context.Context, opts config.Options, bus *event.Bus, prober *probe.Source, sess *session) (*control.Client, error) {
	keys, err := signdoc.BuiltinKeys()
	if err != nil {
		return nil, err
	}
	log := bus.For("control") // held while starting/waking, like the monitors
	state, err := control.LoadState(filepath.Join(service.DataDir(), "state", "control.json"))
	if errors.Is(err, fs.ErrPermission) {
		// The state folder is admin-only; a console run without elevation can't use it.
		bus.ForUnheld("exmon").Log("WARNING", "control: can't read the state folder (not elevated); applied versions are kept in memory only")
		state, err = control.NewMemoryState(), nil
	}
	if err != nil {
		return nil, err
	}

	// client is declared first so the report-status command (a closure) can use it.
	var client *control.Client
	commands := map[string]func() (string, error){
		"report-status": func() (string, error) {
			client.PublishStatus(ctx)
			return "status published", nil
		},
		"trace-now": func() (string, error) {
			// The path tracer listens for this event on the bus.
			log.Log(probe.KindTraceNow, "path trace requested by a control-plane command")
			return "path trace requested", nil
		},
		"check-now": func() (string, error) {
			if prober == nil {
				return "", errProbesOff
			}
			prober.Trigger()
			return "probe round started", nil
		},
	}
	handlers := map[string]control.Handler{"config": configHandler(sess)}
	updateLog := bus.For("update")
	if opts.Control.Update.Enabled {
		asService := sess != nil && sess.asService
		upd := updater.New(ctx, opts.Control.Update, asService, service.DataDir(),
			func() string { return client.DeviceID() }, updateLog)
		handlers["release"] = upd.Handle
	}
	client = control.New(opts.Control, control.Deps{
		Transport:   control.NewTransport(opts.Control),
		Keys:        keys,
		State:       state,
		Log:         log,
		Handlers:    handlers,
		Commands:    commands,
		StartHelper: func() error { return winsys.StartServiceOnly(updater.HelperService) },
		Status: func() map[string]any {
			return map[string]any{"configVersion": opts.CloudVersion}
		},
	})
	// Finish (or report the rollback of) an update this start may be part of.
	go updater.CheckOnStart(ctx, service.DataDir(), updateLog, client.HasConnected)
	return client, nil
}

// configHandler installs a received cloud config: it is first tried exactly as
// it would load (merged with the local config and flags, validated, and for the
// service checked against the output folders), then stored with the previous one
// kept as last-known-good, marked as on trial, and exmon restarts in-process to
// use it. If exmon then fails to start or crashes repeatedly, supervise puts the
// previous one back.
func configHandler(sess *session) control.Handler {
	return func(raw, _ []byte, h signdoc.Header) (string, error) {
		if sess == nil || sess.restart == nil {
			return "", controlError("this exmon can't restart itself")
		}
		restart := sess.restart // this run's restart function (supervise sets a new one per run)
		trial, err := config.TestCloud(sess.args, sess.trust, raw)
		if err != nil {
			return "", err
		}
		if sess.asService {
			if err := trial.CheckOutputs(service.DataDir()); err != nil {
				return "", err
			}
		}
		if err := config.InstallCloud(raw); err != nil {
			return "", fmt.Errorf("store the cloud config: %w", err)
		}
		pending := &config.Pending{Kind: "config", Version: h.Version, Deadline: time.Now().Add(10 * time.Minute)}
		if err := config.WritePending(config.PendingPath(), pending); err != nil {
			config.RevertCloud()
			return "", fmt.Errorf("record the trial: %w", err)
		}
		// Restart shortly, after the client has acknowledged on the outbox.
		time.AfterFunc(3*time.Second, func() { restart(fmt.Sprintf("cloud config version %d", h.Version)) })
		return "installed; restarting to apply it", nil
	}
}

type controlError string

func (e controlError) Error() string { return string(e) }

const errProbesOff = controlError("probes are turned off on this device")
