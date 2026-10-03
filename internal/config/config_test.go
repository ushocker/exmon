package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exmon.json")
	json := `{"log": {"maxFiles": 9}, "wifi": {"pollSeconds": 10, "rssiThreshold": 7}}`
	if err := os.WriteFile(path, []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}

	opts, err := Load([]string{"--config", path, "--wifi-poll-seconds", "2", "--log-path", `%TEMP%\x.log`})
	if err != nil {
		t.Fatal(err)
	}

	if opts.WiFi.PollSeconds != 2 {
		t.Errorf("pollSeconds = %d, want 2 (flag beats file)", opts.WiFi.PollSeconds)
	}
	if opts.WiFi.RSSIThreshold != 7 {
		t.Errorf("rssiThreshold = %d, want 7 (file beats default)", opts.WiFi.RSSIThreshold)
	}
	if opts.Log.MaxFiles != 9 {
		t.Errorf("maxFiles = %d, want 9 (file beats default)", opts.Log.MaxFiles)
	}
	if opts.WiFi.RateChangePercent != 50 {
		t.Errorf("rateChangePercent = %d, want default 50", opts.WiFi.RateChangePercent)
	}
	if want := filepath.Join(os.Getenv("TEMP"), "x.log"); opts.Log.Path != want {
		t.Errorf("log path = %q, want %q (%%TEMP%% expanded)", opts.Log.Path, want)
	}
}

func TestMissingRequiredConfig(t *testing.T) {
	if _, err := Load([]string{"--config", filepath.Join(t.TempDir(), "nope.json")}); err == nil {
		t.Error("expected an error for a missing --config file")
	}
}

func TestValidate(t *testing.T) {
	if _, err := Load([]string{"--config", writeEmpty(t), "--wifi-poll-seconds", "0"}); err == nil {
		t.Error("expected pollSeconds=0 to fail validation")
	}
}

func writeEmpty(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "empty.json")
	os.WriteFile(p, []byte("{}"), 0o644)
	return p
}

func TestTriggersFromJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"incident": {"triggers": [
		{"name": "weak", "source": "wifi", "metric": "rssi_dbm", "op": "<", "threshold": -75, "forSamples": 3}]},
		"remote": {"include": [{"kinds": ["ACM/*"]}]}}`), 0o644)
	opts, err := Load([]string{"--config", p})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.Incident.Triggers) != 1 || opts.Incident.Triggers[0].Threshold != -75 || opts.Incident.Triggers[0].ForSamples != 3 {
		t.Errorf("triggers = %+v", opts.Incident.Triggers)
	}
	if len(opts.Remote.Include) != 1 || opts.Remote.Include[0].Kinds[0] != "ACM/*" {
		t.Errorf("include = %+v", opts.Remote.Include)
	}

	os.WriteFile(p, []byte(`{"incident": {"triggers": [{"name": "x", "source": "wifi", "metric": "m", "op": "=="}]}}`), 0o644)
	if _, err := Load([]string{"--config", p}); err == nil {
		t.Error("op == should fail validation")
	}
}
