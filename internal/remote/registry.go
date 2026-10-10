package remote

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"exmon/internal/event"
)

// SenderType describes one kind of remote target ("outbox", "azureMonitor", ...).
// A remote's "type" picks one; its "settings" object is handed to these functions
// as raw JSON, so each type defines and checks its own settings.
//
// Adding a target type means writing one of these and calling Register for it
// (see internal/remote/senders). Nothing else in exmon needs to change.
type SenderType struct {
	// Validate checks the settings. It runs at startup, before anything is sent.
	Validate func(settings json.RawMessage) error
	// Outputs lists files the sender writes (with %VAR% expanded), for the
	// service's output containment check. nil = it writes no files.
	Outputs func(settings json.RawMessage) ([]string, error)
	// Describe returns a short, secret-free summary of the settings for the
	// startup log (e.g. a file path or an endpoint). nil = nothing to add.
	Describe func(settings json.RawMessage) string
	// New creates the Sender for one remote.
	New func(cfg Config, log event.Logger) (Sender, error)
	// CloudAllowed says whether a cloud-delivered config may use this type.
	// Types that write local files are not allowed (cloud configs can't set paths).
	CloudAllowed bool
}

// The registry of sender types. It starts with the types that live in this
// package; other packages add theirs with Register. The mutex makes it safe to
// read from several goroutines, although in practice everything registers at
// startup.
var (
	registryMu  sync.RWMutex
	senderTypes = map[string]SenderType{"outbox": outboxType}
)

// Register adds (or replaces) a sender type.
func Register(name string, t SenderType) {
	registryMu.Lock()
	defer registryMu.Unlock()
	senderTypes[name] = t
}

// Lookup returns the sender type with this name.
func Lookup(name string) (SenderType, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	t, ok := senderTypes[name]
	return t, ok
}

// TypeNames lists the registered types, sorted, for error messages.
func TypeNames() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(senderTypes))
	for n := range senderTypes {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// NewSender creates the sender for a remote, or the always-failing one when
// SimulateOffline is set.
func NewSender(cfg Config, log event.Logger) (Sender, error) {
	if cfg.SimulateOffline {
		return FailingSender{}, nil
	}
	t, ok := Lookup(cfg.Type)
	if !ok {
		return nil, fmt.Errorf("remote %s: unknown type %q", cfg.Name, cfg.Type)
	}
	return t.New(cfg, log)
}

// Describe is the startup-log summary of a remote's target. It never includes
// secrets: only what the sender type's Describe chooses to show.
func Describe(cfg Config) string {
	if t, ok := Lookup(cfg.Type); ok && t.Describe != nil {
		return t.Describe(cfg.Settings)
	}
	return ""
}

// DecodeSettings decodes a remote's settings into v, rejecting unknown keys so a
// misspelt setting is an error rather than silently ignored.
func DecodeSettings(settings json.RawMessage, v any) error {
	if len(settings) == 0 {
		return errSettings
	}
	dec := json.NewDecoder(bytes.NewReader(settings))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// outboxSettings are the "outbox" type's settings.
type outboxSettings struct {
	Path string `json:"path"` // file that receives sent events; %VAR% expanded
}

func decodeOutbox(settings json.RawMessage) (string, error) {
	var s outboxSettings
	if err := DecodeSettings(settings, &s); err != nil {
		return "", err
	}
	if s.Path == "" {
		return "", fmt.Errorf("path must be set")
	}
	return ExpandPath(s.Path)
}

var outboxType = SenderType{
	Validate: func(settings json.RawMessage) error {
		_, err := decodeOutbox(settings)
		return err
	},
	Outputs: func(settings json.RawMessage) ([]string, error) {
		p, err := decodeOutbox(settings)
		return []string{p}, err
	},
	Describe: func(settings json.RawMessage) string {
		p, _ := decodeOutbox(settings)
		return p
	},
	New: func(cfg Config, _ event.Logger) (Sender, error) {
		p, err := decodeOutbox(cfg.Settings)
		if err != nil {
			return nil, err
		}
		return OutboxSender{Path: p}, nil
	},
	CloudAllowed: false, // writes a local file
}
