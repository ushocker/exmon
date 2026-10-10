package config

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// opts returns options with every output inside dataDir and everything enabled.
func outputOpts(dataDir string) Options {
	o := Defaults()
	o.Log.Path = filepath.Join(dataDir, "exmon.log")
	o.Incident.Dir = filepath.Join(dataDir, "incidents")
	o.Remotes[0].SpoolDir = filepath.Join(dataDir, "remote-spool", "outbox")
	o.Remotes[0].Settings = []byte(`{"path": ` + strconv.Quote(filepath.Join(dataDir, "remote-outbox.jsonl")) + `}`)
	o.Baseline.Enabled = true
	o.Baseline.Path = filepath.Join(dataDir, "baseline.jsonl")
	return o
}

func TestCheckOutputs(t *testing.T) {
	data := t.TempDir()
	o := outputOpts(data)
	if err := o.CheckOutputs(data); err != nil {
		t.Fatalf("defaults inside the data folder: %v", err)
	}

	// An output elsewhere is refused, naming the setting.
	other := t.TempDir()
	o.Incident.Dir = filepath.Join(other, "incidents")
	err := o.CheckOutputs(data)
	if err == nil || !strings.Contains(err.Error(), "incident.dir") || !strings.Contains(err.Error(), "outputRoots") {
		t.Errorf("got %v, want incident.dir refused with a hint about outputRoots", err)
	}

	// ...unless its folder is listed in outputRoots.
	o.OutputRoots = []string{other}
	if err := o.CheckOutputs(data); err != nil {
		t.Errorf("with outputRoots: %v", err)
	}

	// A disabled feature's path isn't checked.
	o = outputOpts(data)
	o.Baseline.Enabled = false
	o.Baseline.Path = `Z:\nowhere\baseline.jsonl`
	if err := o.CheckOutputs(data); err != nil {
		t.Errorf("disabled baseline path checked: %v", err)
	}

	// outputRoots must be full paths.
	o.OutputRoots = []string{`relative\logs`}
	if err := o.CheckOutputs(data); err == nil {
		t.Error("a relative outputRoots entry should be refused")
	}
}

func TestCheckOutputsRefusesJunction(t *testing.T) {
	data, elsewhere := t.TempDir(), t.TempDir()
	link := filepath.Join(data, "incidents")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, elsewhere).CombinedOutput(); err != nil {
		t.Skipf("can't create a junction here: %v %s", err, out)
	}
	o := outputOpts(data)
	err := o.CheckOutputs(data)
	if err == nil || !strings.Contains(err.Error(), "incident.dir") || !strings.Contains(err.Error(), "junction") {
		t.Errorf("got %v, want incident.dir refused as a junction", err)
	}
}
