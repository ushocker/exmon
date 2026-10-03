package nic

import (
	"strings"
	"testing"

	"exmon/internal/winnet"
)

func up(ip string, speed int64) winnet.Adapter {
	return winnet.Adapter{Name: "Ethernet", Up: true, Type: winnet.TypeEthernet, SpeedMbps: speed,
		IPv4: []string{ip}, Gateways: []string{"10.0.0.1"}, DNSServers: []string{"10.0.0.2"}}
}

func TestDiff(t *testing.T) {
	down := up("10.0.0.5", 1000)
	down.Up = false
	tests := []struct {
		name       string
		prev, cur  winnet.Adapter
		kind, want string
	}{
		{"no change", up("10.0.0.5", 1000), up("10.0.0.5", 1000), "", ""},
		{"went down", up("10.0.0.5", 1000), down, "NIC/down", "[Ethernet]"},
		{"came up", down, up("10.0.0.5", 1000), "NIC/up", "ip=10.0.0.5"},
		{"stays down", down, down, "", ""},
		{"speed", up("10.0.0.5", 1000), up("10.0.0.5", 100), "NIC/changed", "speed 1000->100Mbps"},
		{"ip", up("10.0.0.5", 1000), up("10.0.0.9", 1000), "NIC/changed", "ip 10.0.0.5->10.0.0.9"},
	}
	for _, tt := range tests {
		kind, detail := diff(tt.prev, tt.cur)
		if kind != tt.kind || !strings.Contains(detail, tt.want) {
			t.Errorf("%s: got %q %q, want %q containing %q", tt.name, kind, detail, tt.kind, tt.want)
		}
	}
}

func TestDiffCountersReset(t *testing.T) {
	d := diffCounters(winnet.IfCounters{InOctets: 500, InErrors: 3}, winnet.IfCounters{InOctets: 1000, InErrors: 1})
	if d.InOctets != 500 || d.InErrors != 2 {
		t.Errorf("got %+v, want InOctets=500 (reset) InErrors=2", d)
	}
}

func TestWatched(t *testing.T) {
	s := New(DefaultConfig())
	cases := map[string]bool{
		"Intel(R) Ethernet Connection":       true,
		"Hyper-V Virtual Ethernet Adapter":   false,
		"PANGP Virtual Ethernet Adapter":     false,
		"Bluetooth Device (Personal Area N)": false,
	}
	for desc, want := range cases {
		if got := s.watched(winnet.Adapter{Description: desc, Type: winnet.TypeEthernet}); got != want {
			t.Errorf("%s: watched=%v, want %v", desc, got, want)
		}
	}
	if s.watched(winnet.Adapter{Type: winnet.TypeLoopback}) {
		t.Error("loopback should never be watched")
	}
	s.cfg.IncludeVirtual = true
	if !s.watched(winnet.Adapter{Description: "Hyper-V Virtual Ethernet Adapter", Type: winnet.TypeEthernet}) {
		t.Error("includeVirtual should watch virtual adapters")
	}
}

func TestBps(t *testing.T) {
	if bps(1_500_000) != "1.5Mbps" || bps(800) != "800bps" {
		t.Errorf("bps formatting: %s %s", bps(1_500_000), bps(800))
	}
}
