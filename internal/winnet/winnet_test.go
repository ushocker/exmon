package winnet

import (
	"testing"
	"time"
	"unsafe"
)

func TestStructLayout(t *testing.T) {
	checks := []struct {
		name      string
		got, want uintptr
	}{
		{"sizeof IP_OPTION_INFORMATION", unsafe.Sizeof(ipOptionInformation{}), 16},
		{"sizeof ICMP_ECHO_REPLY", unsafe.Sizeof(icmpEchoReply{}), 40},
		{"ICMP_ECHO_REPLY.Data", unsafe.Offsetof(icmpEchoReply{}.Data), 16},
		{"ICMP_ECHO_REPLY.Options", unsafe.Offsetof(icmpEchoReply{}.Options), 24},
		{"sizeof RASCONNW (pack 4)", unsafe.Sizeof(rasConn{}), 1388},
		{"RASCONNW.EntryName", unsafe.Offsetof(rasConn{}.EntryName), 12},
		{"RASCONNW.SubEntry", unsafe.Offsetof(rasConn{}.SubEntry), 1340},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

func TestPingLoopback(t *testing.T) {
	r, err := Ping("127.0.0.1", 0, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK() || r.From != "127.0.0.1" {
		t.Errorf("got %+v (%s), want an answer from 127.0.0.1", r, StatusText(r.Status))
	}
}

func TestPingRejectsNonIPv4(t *testing.T) {
	if _, err := Ping("example.com", 0, time.Second); err == nil {
		t.Error("expected an error for a host name")
	}
}

func TestAdaptersAndRoutes(t *testing.T) {
	ads, err := Adapters()
	if err != nil {
		t.Fatal(err)
	}
	if len(ads) == 0 || ads[0].LUID == 0 {
		t.Fatalf("expected adapters with LUIDs, got %+v", ads)
	}
	for _, a := range ads {
		if a.Up && a.Type != TypeLoopback {
			if _, err := Counters(a.LUID); err != nil {
				t.Errorf("Counters(%s): %v", a.Name, err)
			}
		}
	}
	routes, err := DefaultRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if r, a, ok := BestDefaultRoute(routes, ads); ok {
		t.Logf("default route via %s (%s) next hop %s", a.Name, TypeName(a.Type), r.NextHop)
	} else {
		t.Log("no default route (offline machine?)")
	}
}

// TestPingTTLExpires checks the path-trace mechanism: with TTL 1 the first router
// answers instead of the target. Skipped when there is no default gateway.
func TestPingTTLExpires(t *testing.T) {
	ads, _ := Adapters()
	routes, _ := DefaultRoutes()
	r, _, ok := BestDefaultRoute(routes, ads)
	if !ok || r.NextHop == "0.0.0.0" {
		t.Skip("no gateway")
	}
	rep, err := Ping("1.1.1.1", 1, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status == StatusTimedOut {
		t.Skip("first hop does not answer ICMP")
	}
	if rep.Status != StatusTTLExpiredTransit || rep.From == "" {
		t.Errorf("got %+v (%s), want ttl_expired from a router", rep, StatusText(rep.Status))
	}
	t.Logf("first hop: %s in %v", rep.From, rep.RTT)
}

func TestRasAndProcesses(t *testing.T) {
	if _, err := RasConnections(); err != nil {
		t.Errorf("RasConnections: %v", err)
	}
	procs, err := Processes()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range procs {
		found = found || p == "System"
	}
	if !found {
		t.Errorf("expected the System process in %d processes", len(procs))
	}
}
