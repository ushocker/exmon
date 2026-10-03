package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchTrusted(t *testing.T) {
	dir := t.TempDir()
	next, machine := filepath.Join(dir, "next.json"), filepath.Join(dir, "machine.json")
	os.WriteFile(next, []byte("{}"), 0o644)
	os.WriteFile(machine, []byte("{}"), 0o644)

	distrust := func(bad string) TrustFunc {
		return func(p string) error {
			if p == bad {
				return errors.New("users can write")
			}
			return nil
		}
	}

	// Untrusted next-to-exe file: skipped with a warning, machine-wide used.
	found, warnings, err := searchTrusted([]string{next, machine}, distrust(next))
	if err != nil || found != machine || len(warnings) != 1 || !strings.Contains(warnings[0], "next.json") {
		t.Errorf("got %q %v %v", found, warnings, err)
	}

	// Untrusted machine-wide file: an error, not a silent fallback to defaults.
	os.Remove(next)
	if _, _, err := searchTrusted([]string{next, machine}, distrust(machine)); err == nil {
		t.Error("an untrusted machine-wide config should be an error")
	}

	// No trust function: first existing file, as before.
	if found, _, _ := searchTrusted([]string{next, machine}, nil); found != machine {
		t.Errorf("got %q", found)
	}
}

func TestLoadWithRejectsUntrustedExplicitConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte("{}"), 0o644)
	_, err := LoadWith([]string{"--config", p}, func(string) error { return errors.New("not admin-only") })
	if err == nil || !strings.Contains(err.Error(), "refusing config") {
		t.Errorf("got %v", err)
	}
}
