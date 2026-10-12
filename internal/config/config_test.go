package config

import (
	"os"
	"path/filepath"
	"strings"
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
		"remotes": [{"name": "local", "type": "outbox", "settings": {"path": "x.jsonl"}, "include": [{"kinds": ["ACM/*"]}]}]}`), 0o644)
	opts, err := Load([]string{"--config", p})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.Incident.Triggers) != 1 || opts.Incident.Triggers[0].Threshold != -75 || opts.Incident.Triggers[0].ForSamples != 3 {
		t.Errorf("triggers = %+v", opts.Incident.Triggers)
	}
	if len(opts.Remotes) != 1 || opts.Remotes[0].Include[0].Kinds[0] != "ACM/*" || opts.Remotes[0].BatchSize != 50 {
		t.Errorf("remotes = %+v (want the file's list, with defaults filled in)", opts.Remotes)
	}

	os.WriteFile(p, []byte(`{"incident": {"triggers": [{"name": "x", "source": "wifi", "metric": "m", "op": "=="}]}}`), 0o644)
	if _, err := Load([]string{"--config", p}); err == nil {
		t.Error("op == should fail validation")
	}
}

func TestExampleConfigLoads(t *testing.T) {
	opts, err := Load([]string{"--config", "../../exmon.example.json"})
	if err != nil {
		t.Fatalf("exmon.example.json: %v", err)
	}
	if len(opts.Probe.Endpoints) == 0 || len(opts.VPN.AdapterPatterns) == 0 || len(opts.Incident.Triggers) < 5 {
		t.Errorf("example sections not loaded: %+v", opts.Probe)
	}
}

func TestFindConfigOrder(t *testing.T) {
	dir := t.TempDir()
	next, machine := filepath.Join(dir, "next.json"), filepath.Join(dir, "machine.json")
	if got := findConfig([]string{next, machine}); got != "" {
		t.Errorf("nothing exists: got %q", got)
	}
	os.WriteFile(machine, []byte("{}"), 0o644)
	if got := findConfig([]string{next, machine}); got != machine {
		t.Errorf("only machine-wide: got %q", got)
	}
	os.WriteFile(next, []byte("{}"), 0o644)
	if got := findConfig([]string{next, machine}); got != next {
		t.Errorf("next to the exe should win: got %q", got)
	}
}

func TestSearchPaths(t *testing.T) {
	paths := SearchPaths()
	want := filepath.Join(os.Getenv("ProgramData"), `exmon\config\exmon.json`)
	if len(paths) != 2 || filepath.Base(paths[0]) != "exmon.json" || paths[1] != want {
		t.Errorf("got %v, want [<exe dir>/exmon.json %s]", paths, want)
	}
}

func TestWriteDefaultConfig(t *testing.T) {
	target := filepath.Join(t.TempDir(), "sub", "exmon.json")
	opts, err := Load([]string{"--write-default-config", "--config", target})
	if err != nil || opts.WriteDefaultConfig != target {
		t.Fatalf("got %q, %v", opts.WriteDefaultConfig, err)
	}
	if err := WriteDefault(target, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := WriteDefault(target, []byte("{}")); err == nil {
		t.Error("second write should refuse to overwrite")
	}
	if opts, _ := Load([]string{"--write-default-config"}); !strings.HasSuffix(opts.WriteDefaultConfig, `exmon\config\exmon.json`) {
		t.Errorf("default target = %q", opts.WriteDefaultConfig)
	}
}

func TestRenamedDisconnectMetric(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"incident": {"triggers": [{"name": "f", "source": "wifi", "metric": "disconnects_30m", "op": ">", "threshold": 2}]}}`), 0o644)
	if _, err := Load([]string{"--config", p}); err == nil || !strings.Contains(err.Error(), "recent_disconnects") {
		t.Errorf("want a rename error, got %v", err)
	}
	os.WriteFile(p, []byte(`{"wifi": {"disconnectWindowMinutes": 0}}`), 0o644)
	if _, err := Load([]string{"--config", p}); err == nil {
		t.Error("disconnectWindowMinutes 0 should fail")
	}
}

func TestExampleSeverities(t *testing.T) {
	opts, err := Load([]string{"--config", "../../exmon.example.json"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Incident.MaxIncidentsBySeverity[1] != 500 || opts.Incident.MaxIncidentsBySeverity[5] != 20 {
		t.Errorf("maxIncidentsBySeverity = %v", opts.Incident.MaxIncidentsBySeverity)
	}
	if len(opts.Remotes) == 0 || opts.Remotes[0].Include[1].SeverityAtLeast != 3 {
		t.Errorf("remotes = %+v", opts.Remotes)
	}
}

func TestOldRemoteKeyRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"remote": {"enabled": true}}`), 0o644)
	if _, err := Load([]string{"--config", p}); err == nil || !strings.Contains(err.Error(), `"remotes"`) {
		t.Errorf("want a migration error naming \"remotes\", got %v", err)
	}
}

func TestRemoteFlags(t *testing.T) {
	opts, err := Load([]string{"--config", writeEmpty(t), "--remote-simulate-offline", "--remotes-enabled=false"})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.Remotes) != 1 || opts.Remotes[0].IsEnabled() || !opts.Remotes[0].SimulateOffline {
		t.Errorf("flags not applied to the default remote: %+v", opts.Remotes)
	}
	if want := filepath.Join(os.Getenv("ProgramData"), `exmon\remote-spool\outbox`); opts.Remotes[0].SpoolDir != want {
		t.Errorf("default spool = %q, want %q", opts.Remotes[0].SpoolDir, want)
	}
}

func TestAzureMonitorRemoteValidates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"remotes": [{"name": "law", "type": "azureMonitor", "settings": {
		"endpoint": "https://x.ingest.monitor.azure.com", "dcrImmutableId": "dcr-0123456789abcdef0123456789abcdef",
		"streamName": "Custom-ExmonEvents", "tenantId": "11111111-2222-3333-4444-555555555555",
		"clientId": "66666666-7777-8888-9999-000000000000", "clientSecret": "s3cret"},
		"include": [{"kinds": ["INCIDENT/*"]}]}]}`), 0o644)
	opts, err := Load([]string{"--config", p})
	if err != nil {
		t.Fatalf("azureMonitor remote: %v", err)
	}
	// The remote writes only its spool (the azureMonitor type writes no local files).
	out := opts.outputs()
	if last := out[len(out)-1]; last.setting != "remotes[law]" || !strings.HasSuffix(last.path, `remote-spool\law`) ||
		len(out) != 3 {
		t.Errorf("outputs = %+v, want log, incidents and the remote's spool", out)
	}
	os.WriteFile(p, []byte(`{"remotes": [{"name": "law", "type": "azureMonitor", "settings": {"endpoint": "http://x"}}]}`), 0o644)
	if _, err := Load([]string{"--config", p}); err == nil || !strings.Contains(err.Error(), "remotes[law].settings") {
		t.Errorf("bad settings should name the remote: %v", err)
	}
}
