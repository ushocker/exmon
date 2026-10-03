// Package vpn detects VPN tunnels three ways, like the AzureNetMonitor script:
// built-in Windows VPN connections (RAS), adapters of third-party VPN clients
// (matched by name), and running VPN client processes. It also tells a full tunnel
// (the VPN owns the default route) from a split tunnel.
package vpn

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"exmon/internal/event"
	"exmon/internal/winnet"
)

var metricDirections = map[string]event.Direction{
	"vpn_up":            event.LowIsBad,
	"vpn_server_rtt_ms": event.HighIsBad,
	"in_errors":         event.HighIsBad,
	"out_errors":        event.HighIsBad,
}

// tunnel is one detected VPN.
type tunnel struct {
	Name   string // RAS entry name, or the adapter name
	Kind   string // "builtin" or "adapter"
	Desc   string // adapter description
	Up     bool
	IP     string
	Mode   string // "full", "split", or "" when down
	Server string // built-in VPN server from the phonebook
	LUID   uint64
}

func (t tunnel) key() string { return t.Kind + "|" + t.Name }

func (t tunnel) String() string {
	s := fmt.Sprintf("[%s] %s", t.Name, t.Kind)
	if t.Desc != "" {
		s += fmt.Sprintf(" %q", t.Desc)
	}
	if t.Up {
		s += fmt.Sprintf(" up ip=%s tunnel=%s", or(t.IP, "-"), or(t.Mode, "?"))
	} else {
		s += " down"
	}
	if t.Server != "" {
		s += " server=" + t.Server
	}
	return s
}

// Source implements source.Source for VPN detection.
type Source struct {
	cfg      Config
	prev     map[string]tunnel
	counters map[uint64]winnet.IfCounters
	lastHB   time.Time
	started  bool
}

// New creates the VPN source.
func New(cfg Config) *Source {
	return &Source{cfg: cfg, prev: map[string]tunnel{}, counters: map[uint64]winnet.IfCounters{}}
}

// Name implements source.Source.
func (s *Source) Name() string { return "vpn" }

// Run implements source.Source.
func (s *Source) Run(ctx context.Context, log event.Logger) error {
	log.DeclareMetrics(metricDirections)
	ticker := time.NewTicker(time.Duration(max(1, s.cfg.PollSeconds)) * time.Second)
	defer ticker.Stop()
	for {
		s.poll(log)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Source) poll(log event.Logger) {
	ads, err := winnet.Adapters()
	if err != nil {
		log.Log("ERROR", "vpn: "+err.Error())
		return
	}
	routes, _ := winnet.DefaultRoutes() // without routes we just can't tell full from split
	ras, err := winnet.RasConnections()
	if err != nil {
		log.Log("ERROR", "vpn: "+err.Error())
	}
	procs, _ := winnet.Processes()

	tunnels := detect(ras, ads, routes, s.cfg.AdapterPatterns)
	clients := matchProcesses(procs, s.cfg.ProcessNames)

	cur := map[string]tunnel{}
	for _, t := range tunnels {
		cur[t.key()] = t
		if !s.started {
			continue // the first poll is reported as one VPN/status line below
		}
		prev, seen := s.prev[t.key()]
		if kind, detail := diff(prev, seen, t); kind != "" {
			log.Log(kind, detail)
		}
	}
	for k, t := range s.prev {
		if _, still := cur[k]; !still && t.Up && s.started {
			log.Log("VPN/disconnected", t.Name+" (gone)")
		}
	}
	s.prev = cur

	s.metrics(log, tunnels)

	if !s.started || s.heartbeatDue() {
		s.started = true
		s.lastHB = time.Now()
		log.Log("VPN/status", status(tunnels, clients))
	}
}

// detect builds the tunnel list from RAS connections and VPN-looking adapters.
func detect(ras []winnet.RasConn, ads []winnet.Adapter, routes []winnet.Route, patterns []string) []tunnel {
	full := map[uint32]bool{} // interface index -> owns a default route
	for _, r := range routes {
		full[r.IfIndex] = true
	}
	mode := func(a winnet.Adapter) string {
		if !a.Up {
			return ""
		}
		if full[a.IfIndex] {
			return "full"
		}
		return "split"
	}

	var out []tunnel
	used := map[uint64]bool{}
	// 1. Built-in Windows VPN: RAS shows its adapter as a PPP interface named after the entry.
	for _, c := range ras {
		if !strings.EqualFold(c.DeviceType, "vpn") {
			continue
		}
		t := tunnel{Name: c.EntryName, Kind: "builtin", Desc: c.DeviceName, Up: true, Server: serverFor(c.EntryName, c.Phonebook)}
		for _, a := range ads {
			if a.Type == winnet.TypePPP && (a.Name == c.EntryName || a.Description == c.EntryName) {
				t.IP, t.Mode, t.LUID, t.Up = first(a.IPv4), mode(a), a.LUID, a.Up
				used[a.LUID] = true
			}
		}
		out = append(out, t)
	}
	// 2. Third-party clients: adapters whose name or description matches a pattern.
	for _, a := range ads {
		if used[a.LUID] || !matchesAny(a.Name+" "+a.Description, patterns) {
			continue
		}
		out = append(out, tunnel{Name: a.Name, Kind: "adapter", Desc: a.Description, Up: a.Up,
			IP: first(a.IPv4), Mode: mode(a), LUID: a.LUID})
	}
	return out
}

// diff returns the line to log when a tunnel changed.
func diff(prev tunnel, seen bool, cur tunnel) (kind, detail string) {
	switch {
	case cur.Up && (!seen || !prev.Up):
		return "VPN/connected", cur.String()
	case !cur.Up && seen && prev.Up:
		return "VPN/disconnected", cur.String()
	case !cur.Up:
		return "", ""
	}
	var changes []string
	if prev.Mode != cur.Mode {
		changes = append(changes, fmt.Sprintf("tunnel %s->%s", prev.Mode, cur.Mode))
	}
	if prev.IP != cur.IP {
		changes = append(changes, fmt.Sprintf("ip %s->%s", or(prev.IP, "-"), or(cur.IP, "-")))
	}
	if prev.Server != cur.Server {
		changes = append(changes, fmt.Sprintf("server %s->%s", or(prev.Server, "-"), or(cur.Server, "-")))
	}
	if len(changes) == 0 {
		return "", ""
	}
	return "VPN/changed", fmt.Sprintf("[%s] %s", cur.Name, strings.Join(changes, " "))
}

// metrics reports per-tunnel metrics: up/down, server RTT for built-in VPNs, and
// adapter error growth since the last poll.
func (s *Source) metrics(log event.Logger, tunnels []tunnel) {
	for _, t := range tunnels {
		m := map[string]float64{"vpn_up": 0}
		if t.Up {
			m["vpn_up"] = 1
			if rtt, ok := s.serverRTT(t.Server); ok {
				m["vpn_server_rtt_ms"] = rtt
			}
			if t.LUID != 0 {
				if c, err := winnet.Counters(t.LUID); err == nil {
					if p, ok := s.counters[t.LUID]; ok {
						m["in_errors"] = float64(grow(c.InErrors, p.InErrors))
						m["out_errors"] = float64(grow(c.OutErrors, p.OutErrors))
					}
					s.counters[t.LUID] = c
				}
			}
		}
		log.Metrics(t.Name, m)
	}
}

// serverRTT pings a VPN server (name or IPv4) and returns the average in ms.
func (s *Source) serverRTT(server string) (float64, bool) {
	if server == "" {
		return 0, false
	}
	addr := server
	if net.ParseIP(server) == nil {
		ips, err := net.LookupIP(server)
		if err != nil {
			return 0, false
		}
		addr = ""
		for _, ip := range ips {
			if ip.To4() != nil {
				addr = ip.String()
				break
			}
		}
		if addr == "" {
			return 0, false
		}
	}
	var total time.Duration
	got := 0
	for i := 0; i < max(1, s.cfg.PingCount); i++ {
		if r, err := winnet.Ping(addr, 0, 2*time.Second); err == nil && r.OK() {
			total += r.RTT
			got++
		}
	}
	if got == 0 {
		return 0, false
	}
	return float64((total / time.Duration(got)).Microseconds()) / 1000, true
}

func (s *Source) heartbeatDue() bool {
	return s.cfg.HeartbeatSeconds > 0 && time.Since(s.lastHB) >= time.Duration(s.cfg.HeartbeatSeconds)*time.Second
}

// status is the VPN/status summary line.
func status(tunnels []tunnel, clients []string) string {
	var up []string
	for _, t := range tunnels {
		if t.Up {
			up = append(up, t.String())
		}
	}
	connected := "no"
	if len(up) > 0 {
		connected = "yes"
	}
	return fmt.Sprintf("connected=%s tunnels=%d up=%d clients=%s %s",
		connected, len(tunnels), len(up), or(strings.Join(clients, ","), "none"), strings.Join(up, "; "))
}

// matchProcesses returns the configured VPN client names that are running.
func matchProcesses(running, wanted []string) []string {
	var out []string
	for _, w := range wanted {
		if slices.ContainsFunc(running, func(p string) bool { return strings.EqualFold(p, w) }) {
			out = append(out, w)
		}
	}
	return out
}

func matchesAny(text string, patterns []string) bool {
	text = strings.ToLower(text)
	for _, p := range patterns {
		if p != "" && strings.Contains(text, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

func grow(cur, prev uint64) uint64 {
	if cur >= prev {
		return cur - prev
	}
	return cur
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
