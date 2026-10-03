package vpn

import (
	"bufio"
	"strings"
	"testing"

	"exmon/internal/winnet"
)

func TestDetectFullAndSplit(t *testing.T) {
	ads := []winnet.Adapter{
		{Name: "Wi-Fi", Description: "Intel Wi-Fi", Up: true, IfIndex: 5, LUID: 1, IPv4: []string{"192.168.1.5"}},
		{Name: "Ethernet 3", Description: "PANGP Virtual Ethernet Adapter", Up: true, IfIndex: 9, LUID: 2, IPv4: []string{"10.8.0.4"}},
		{Name: "Local Area Connection", Description: "Fortinet SSL VPN Virtual Ethernet Adapter", Up: true, IfIndex: 11, LUID: 3},
		{Name: "Work VPN", Description: "Work VPN", Type: winnet.TypePPP, Up: true, IfIndex: 12, LUID: 4, IPv4: []string{"172.16.0.9"}},
	}
	routes := []winnet.Route{{IfIndex: 5}, {IfIndex: 9}} // Wi-Fi and the PANGP adapter have default routes
	ras := []winnet.RasConn{{EntryName: "Work VPN", DeviceType: "vpn", DeviceName: "WAN Miniport (IKEv2)"}}

	got := detect(ras, ads, routes, DefaultConfig().AdapterPatterns)
	if len(got) != 3 {
		t.Fatalf("got %d tunnels, want 3: %+v", len(got), got)
	}
	want := map[string]struct{ kind, mode, ip string }{
		"Work VPN":              {"builtin", "split", "172.16.0.9"},
		"Ethernet 3":            {"adapter", "full", "10.8.0.4"},
		"Local Area Connection": {"adapter", "split", ""},
	}
	for _, tn := range got {
		w, ok := want[tn.Name]
		if !ok || tn.Kind != w.kind || tn.Mode != w.mode || tn.IP != w.ip {
			t.Errorf("%s: got kind=%s mode=%s ip=%s, want %+v", tn.Name, tn.Kind, tn.Mode, tn.IP, w)
		}
	}
}

func TestDiff(t *testing.T) {
	on := tunnel{Name: "v", Kind: "adapter", Up: true, Mode: "split", IP: "10.0.0.1"}
	off := on
	off.Up, off.Mode = false, ""
	full := on
	full.Mode = "full"

	cases := []struct {
		name string
		prev tunnel
		seen bool
		cur  tunnel
		want string
	}{
		{"new and up", tunnel{}, false, on, "VPN/connected"},
		{"new and down", tunnel{}, false, off, ""},
		{"went down", on, true, off, "VPN/disconnected"},
		{"came back", off, true, on, "VPN/connected"},
		{"split to full", on, true, full, "VPN/changed"},
		{"same", on, true, on, ""},
	}
	for _, c := range cases {
		if kind, _ := diff(c.prev, c.seen, c.cur); kind != c.want {
			t.Errorf("%s: got %q, want %q", c.name, kind, c.want)
		}
	}
}

func TestPhonebook(t *testing.T) {
	pbk := "[Other]\nPhoneNumber=other.example.com\n\n[Work VPN]\nMEDIA=rastapi\nPhoneNumber=vpn.example.com\n"
	if got := phoneNumber(bufio.NewScanner(strings.NewReader(pbk)), "work vpn"); got != "vpn.example.com" {
		t.Errorf("got %q", got)
	}
	if got := phoneNumber(bufio.NewScanner(strings.NewReader(pbk)), "Missing"); got != "" {
		t.Errorf("got %q for a missing entry", got)
	}
}

func TestMatchProcesses(t *testing.T) {
	got := matchProcesses([]string{"explorer", "pangpa", "svchost"}, DefaultConfig().ProcessNames)
	if len(got) != 1 || got[0] != "PanGPA" {
		t.Errorf("got %v", got)
	}
}
