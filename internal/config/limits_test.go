package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpperLimits sets each limited value just outside its range through a config
// file and checks Load rejects it, naming the setting.
func TestUpperLimits(t *testing.T) {
	many := func(n int, item string) string {
		return "[" + strings.TrimSuffix(strings.Repeat(item+",", n), ",") + "]"
	}
	tests := []struct{ json, setting string }{
		{`{"log": {"maxFiles": 51}}`, "log.maxFiles"},
		{`{"log": {"maxSizeMB": 1025}}`, "log.maxSizeMB"},
		{`{"log": {"flushIntervalMs": 50}}`, "log.flushIntervalMs"},
		{`{"wifi": {"pollSeconds": 3601}}`, "wifi.pollSeconds"},
		{`{"nic": {"pollSeconds": 1}}`, "nic.pollSeconds"},
		{`{"vpn": {"pingCount": 11}}`, "vpn.pingCount"},
		{`{"probe": {"timeoutSeconds": 61}}`, "probe.timeoutSeconds"},
		{`{"probe": {"pingCount": 100}}`, "probe.pingCount"},
		{`{"probe": {"pathTrace": {"maxHops": 1000}}}`, "probe.pathTrace.maxHops"},
		{`{"probe": {"dnsNames": ` + many(11, `"a"`) + `}}`, "probe.dnsNames"},
		{`{"probe": {"endpoints": ` + many(26, `{"name":"e","target":"h","port":1}`) + `}}`, "probe.endpoints"},
		{`{"incident": {"windowMinutes": 61}}`, "incident.windowMinutes"},
		{`{"incident": {"maxHoldSeconds": 601}}`, "incident.maxHoldSeconds"},
		{`{"incident": {"maxIncidents": 10001}}`, "incident.maxIncidents"},
		{`{"incident": {"maxIncidentsBySeverity": {"1": 20000}}}`, "incident.maxIncidentsBySeverity[1]"},
		{`{"remote": {"segmentKB": 1}}`, "remote.segmentKB"},
		{`{"remote": {"maxSpoolMB": 5000}}`, "remote.maxSpoolMB"},
		{`{"remote": {"batchSize": 5000}}`, "remote.batchSize"},
		{`{"baseline": {"enabled": true, "maxFiles": 100}}`, "baseline.maxFiles"},
	}
	dir := t.TempDir()
	for i, tt := range tests {
		p := filepath.Join(dir, "c.json")
		if err := os.WriteFile(p, []byte(tt.json), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load([]string{"--config", p})
		if err == nil || !strings.Contains(err.Error(), tt.setting) {
			t.Errorf("case %d (%s): got %v, want an error naming %s", i, tt.setting, err, tt.setting)
		}
	}
}

// TestDefaultsWithinLimits makes sure the built-in defaults pass every limit.
func TestDefaultsWithinLimits(t *testing.T) {
	if err := Defaults().Validate(); err != nil {
		t.Errorf("defaults fail validation: %v", err)
	}
}
