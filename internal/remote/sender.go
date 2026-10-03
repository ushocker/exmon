package remote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"exmon/internal/event"
)

// Record is one event as stored in the spool and sent to the remote. The ID is
// unique per event, so the remote can drop duplicates: after a crash between a
// successful send and saving our progress, the same batch is sent again.
type Record struct {
	ID    string      `json:"id"`
	Host  string      `json:"host"` // computer name, so the remote knows which machine sent it
	Event event.Event `json:"event"`
}

func newID() string {
	var b [8]byte
	rand.Read(b[:]) // crypto/rand never fails on Windows
	return hex.EncodeToString(b[:])
}

// Sender delivers a batch to the remote. Returning an error means "not delivered,
// try again later"; the batch stays in the spool.
type Sender interface {
	Send(ctx context.Context, batch []Record) error
}

// OutboxSender is a stand-in for the real remote: it appends each record as a JSON
// line to a local file so you can see exactly what would be sent.
//
// TODO: replace with the real remote target (Config.Endpoint) once it exists.
type OutboxSender struct {
	Path string
}

func (s OutboxSender) Send(_ context.Context, batch []Record) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f) // Encode writes one JSON value plus "\n"
	for _, r := range batch {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return f.Sync()
}

// FailingSender always fails. Use it (--remote-simulate-offline) to test retries
// and redelivery after a restart without touching the network.
type FailingSender struct{}

func (FailingSender) Send(context.Context, []Record) error {
	return errors.New("simulated offline")
}
