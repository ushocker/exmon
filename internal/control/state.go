package control

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// State is what the control client remembers across restarts: the newest
// version of each document kind it has applied, so an older (replayed) document
// is refused and a retained one delivered again on reconnect is ignored. It is
// kept in %ProgramData%\exmon\state\control.json, which only administrators can
// change.
type State struct {
	mu       sync.Mutex
	path     string
	Versions map[string]int64 `json:"versions"` // document kind -> newest applied version
}

// LoadState reads the state file; a missing file is an empty state.
func LoadState(path string) (*State, error) {
	s := &State{path: path, Versions: map[string]int64{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	if s.Versions == nil {
		s.Versions = map[string]int64{}
	}
	return s, nil
}

// NewMemoryState is a state that isn't saved: used when the state folder can't
// be read (a console run without elevation). Versions are then only remembered
// until exmon stops.
func NewMemoryState() *State { return &State{Versions: map[string]int64{}} }

// Version returns the newest applied version of a kind (0 = none).
func (s *State) Version(kind string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Versions[kind]
}

// SetVersion records an applied version and saves the file.
func (s *State) SetVersion(kind string, v int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Versions[kind] = v
	return s.save()
}

// save writes to a temporary file and renames it over the old one, so a crash
// mid-write never leaves a half-written state file.
func (s *State) save() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := s.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
