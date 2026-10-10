package remote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

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
// try again later"; the batch stays in the spool. Wrap the error with Permanent
// when retrying can never help (for example the remote rejects the data's format):
// the queue then drops the batch instead of retrying it forever.
type Sender interface {
	Send(ctx context.Context, batch []Record) error
}

// permanentError marks an error as "don't retry".
type permanentError struct{ err error }

func (p permanentError) Error() string { return p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent wraps err so the queue drops the batch rather than retrying it.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

// IsPermanent reports whether err (or an error it wraps) was marked Permanent.
// errors.As walks the chain of wrapped errors looking for that type.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// retryAfter is implemented by errors that know how long the remote asked us to
// wait (for example an HTTP 429 with a Retry-After header).
type retryAfter interface{ RetryAfter() time.Duration }

// retryHint returns the wait the remote asked for, or 0.
func retryHint(err error) time.Duration {
	var r retryAfter
	if errors.As(err, &r) {
		return r.RetryAfter()
	}
	return 0
}

// OutboxSender appends each record as a JSON line to a local file. It is the
// "outbox" remote type: handy for seeing exactly what would be sent, or for a
// local collector that reads the file.
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
