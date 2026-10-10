package release

// The files exmon (LocalService) and exmon-helper (SYSTEM) use to hand an update
// over. Who may write each one is the point:
//
//	update\incoming\request.json, exmon-<v>.exe   written by exmon     the helper trusts NONE of it
//	update\status\trial.json                     written by the helper exmon may only read it
//	state\update-health.json                     written by exmon     a lie can only prevent a rollback
//	update\verified\, update\helper-state\       the helper only      exmon can't even read them

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Paths under %ProgramData%\exmon.
func IncomingDir(data string) string { return filepath.Join(data, "update", "incoming") }
func RequestPath(data string) string { return filepath.Join(IncomingDir(data), "request.json") }
func VerifiedDir(data string) string { return filepath.Join(data, "update", "verified") }
func StatusDir(data string) string   { return filepath.Join(data, "update", "status") }
func TrialPath(data string) string   { return filepath.Join(StatusDir(data), "trial.json") }
func HelperStatePath(data string) string {
	return filepath.Join(data, "update", "helper-state", "state.json")
}
func HealthPath(data string) string { return filepath.Join(data, "state", "update-health.json") }

// MaxRequestBytes caps request.json; a signed manifest is a few KB.
const MaxRequestBytes = 128 << 10

// Request is what exmon hands the helper: the signed manifest exactly as it was
// received (the helper verifies the signature itself), and the downloaded file's
// name inside update\incoming.
type Request struct {
	Envelope []byte `json:"envelope"` // a []byte is stored as base64, so the bytes stay exactly as received
	File     string `json:"file"`
}

// Trial is the update the helper is trying. exmon reads it to report the outcome.
type Trial struct {
	ReleaseVersion  string    `json:"releaseVersion"`
	PreviousVersion string    `json:"previousVersion"`
	DocVersion      int64     `json:"docVersion"`
	Deadline        time.Time `json:"deadline"`
	Outcome         string    `json:"outcome,omitempty"` // "", "healthy" or "rolled_back"
	Detail          string    `json:"detail,omitempty"`
}

// Health is exmon's report on a trial: the new version is running and connected.
// Reported records that exmon has logged the trial's outcome, so it isn't logged
// again at every start.
type Health struct {
	DocVersion int64  `json:"docVersion"`
	Version    string `json:"version"`
	Healthy    bool   `json:"healthy"`
	Reported   string `json:"reported,omitempty"`
}

// ReadJSON reads a small JSON file into v. A missing file returns (false, nil).
func ReadJSON(path string, max int64, v any) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false, err
	}
	if st.Size() > max {
		return false, errors.New(path + " is too large")
	}
	b := make([]byte, st.Size())
	if _, err := f.Read(b); err != nil && st.Size() > 0 {
		return false, err
	}
	return true, json.Unmarshal(b, v)
}

// WriteJSON writes v atomically (a temporary file, then a rename), or removes
// the file when v is nil.
func WriteJSON(path string, v any) error {
	if v == nil {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
