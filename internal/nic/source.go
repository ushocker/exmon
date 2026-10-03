// Package nic monitors network adapters: up/down and address changes as they happen
// (Windows notifies us), and traffic/error counters on a timer.
package nic

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"exmon/internal/event"
	"exmon/internal/winnet"
)

// Metric names and which direction is bad.
var metricDirections = map[string]event.Direction{
	"link_speed_mbps": event.LowIsBad,
	"in_errors":       event.HighIsBad,
	"out_errors":      event.HighIsBad,
	"in_discards":     event.HighIsBad,
	"out_discards":    event.HighIsBad,
}

// debounce: Windows sends several notifications per change; wait for them to settle.
const debounce = 500 * time.Millisecond

// adapterState is what we remember about one adapter between scans.
type adapterState struct {
	ad       winnet.Adapter
	everUp   bool // seen up at least once since exmon started
	counters winnet.IfCounters
	countAt  time.Time
	hasCount bool

	statusCounters winnet.IfCounters // counters at the last NIC/status line
	statusAt       time.Time
}

// Source implements source.Source for network adapters.
type Source struct {
	cfg    Config
	state  map[uint64]*adapterState // keyed by LUID
	now    func() time.Time
	lastHB time.Time
}

// New creates the NIC source.
func New(cfg Config) *Source {
	return &Source{cfg: cfg, state: map[uint64]*adapterState{}, now: time.Now}
}

// Name implements source.Source.
func (s *Source) Name() string { return "nic" }

// Run implements source.Source.
func (s *Source) Run(ctx context.Context, log event.Logger) error {
	log.DeclareMetrics(metricDirections)

	wake := make(chan struct{}, 1)
	watch, err := winnet.WatchInterfaces(wake)
	if err != nil {
		log.Log("ERROR", "nic: no change notifications, polling only: "+err.Error())
	}
	defer watch.Stop()

	ticker := time.NewTicker(time.Duration(max(1, s.cfg.PollSeconds)) * time.Second)
	defer ticker.Stop()
	settle := time.NewTimer(time.Hour)
	settle.Stop()

	s.scan(log, true)
	s.readCounters(log)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
			settle.Reset(debounce) // restart the wait on every notification
		case <-settle.C:
			s.scan(log, false)
		case <-ticker.C:
			s.scan(log, false)
			s.readCounters(log)
			s.heartbeat(log)
		}
	}
}

// scan reads the adapter list and logs what changed. At startup every adapter that
// is up gets a NIC/status line instead of NIC/up.
func (s *Source) scan(log event.Logger, startup bool) {
	ads, err := winnet.Adapters()
	if err != nil {
		log.Log("ERROR", "nic: "+err.Error())
		return
	}
	seen := map[uint64]bool{}
	for _, ad := range ads {
		if !s.watched(ad) {
			continue
		}
		seen[ad.LUID] = true
		st, known := s.state[ad.LUID]
		if !known {
			st = &adapterState{ad: ad, everUp: ad.Up}
			s.state[ad.LUID] = st
			switch {
			case startup && ad.Up:
				log.Log("NIC/status", describe(ad))
			case ad.Up:
				log.Log("NIC/up", describe(ad))
			}
			continue
		}
		if kind, detail := diff(st.ad, ad); kind != "" {
			log.Log(kind, detail)
		}
		if ad.Up && !st.ad.Up {
			st.hasCount = false // counters restart from here
		}
		st.everUp = st.everUp || ad.Up
		st.ad = ad
	}
	for luid, st := range s.state {
		if !seen[luid] {
			if st.ad.Up {
				log.Log("NIC/down", fmt.Sprintf("[%s] removed", st.ad.Name))
			}
			delete(s.state, luid)
		}
	}
}

// watched reports whether an adapter is one we monitor.
func (s *Source) watched(ad winnet.Adapter) bool {
	if ad.Type == winnet.TypeLoopback || ad.Type == winnet.TypeTunnel {
		return false
	}
	if s.cfg.IncludeVirtual {
		return true
	}
	text := strings.ToLower(ad.Name + " " + ad.Description)
	for _, p := range s.cfg.VirtualPatterns {
		if strings.Contains(text, strings.ToLower(p)) {
			return false
		}
	}
	return true
}

// diff compares two readings of one adapter and returns the line to log, if any.
func diff(prev, cur winnet.Adapter) (kind, detail string) {
	switch {
	case cur.Up && !prev.Up:
		return "NIC/up", describe(cur)
	case !cur.Up && prev.Up:
		return "NIC/down", fmt.Sprintf("[%s] %s", cur.Name, cur.Description)
	case !cur.Up:
		return "", "" // still down; nothing to say
	}
	var changes []string
	if prev.SpeedMbps != cur.SpeedMbps {
		changes = append(changes, fmt.Sprintf("speed %d->%dMbps", prev.SpeedMbps, cur.SpeedMbps))
	}
	if !slices.Equal(prev.IPv4, cur.IPv4) {
		changes = append(changes, fmt.Sprintf("ip %s->%s", list(prev.IPv4), list(cur.IPv4)))
	}
	if !slices.Equal(prev.Gateways, cur.Gateways) {
		changes = append(changes, fmt.Sprintf("gw %s->%s", list(prev.Gateways), list(cur.Gateways)))
	}
	if !slices.Equal(prev.DNSServers, cur.DNSServers) {
		changes = append(changes, fmt.Sprintf("dns %s->%s", list(prev.DNSServers), list(cur.DNSServers)))
	}
	if len(changes) == 0 {
		return "", ""
	}
	return "NIC/changed", fmt.Sprintf("[%s] %s", cur.Name, strings.Join(changes, " "))
}

// readCounters reads every up adapter's counters, reports metrics and logs error growth.
func (s *Source) readCounters(log event.Logger) {
	now := s.now()
	for _, st := range s.state {
		if !st.ad.Up {
			continue
		}
		c, err := winnet.Counters(st.ad.LUID)
		if err != nil {
			continue // the adapter may have just disappeared; the next scan will notice
		}
		if st.hasCount {
			d := diffCounters(c, st.counters)
			secs := now.Sub(st.countAt).Seconds()
			log.Metrics(st.ad.Name, map[string]float64{
				"link_speed_mbps": float64(st.ad.SpeedMbps),
				"rx_bps":          rate(d.InOctets, secs),
				"tx_bps":          rate(d.OutOctets, secs),
				"in_errors":       float64(d.InErrors),
				"out_errors":      float64(d.OutErrors),
				"in_discards":     float64(d.InDiscards),
				"out_discards":    float64(d.OutDiscards),
			})
			if d.InErrors+d.OutErrors+d.InDiscards+d.OutDiscards > 0 {
				log.Log("NIC/errors", fmt.Sprintf("[%s] in_errors=%d out_errors=%d in_discards=%d out_discards=%d (last %.0fs)",
					st.ad.Name, d.InErrors, d.OutErrors, d.InDiscards, d.OutDiscards, secs))
			}
		} else {
			st.statusCounters, st.statusAt = c, now
		}
		st.counters, st.countAt, st.hasCount = c, now, true
	}
}

// heartbeat writes one NIC/status line per up adapter every HeartbeatSeconds, with
// traffic and error totals since the previous status line.
func (s *Source) heartbeat(log event.Logger) {
	now := s.now()
	if s.cfg.HeartbeatSeconds <= 0 || now.Sub(s.lastHB) < time.Duration(s.cfg.HeartbeatSeconds)*time.Second {
		return
	}
	s.lastHB = now
	for _, st := range s.state {
		if !st.ad.Up || !st.hasCount {
			continue
		}
		d := diffCounters(st.counters, st.statusCounters)
		secs := st.countAt.Sub(st.statusAt).Seconds()
		log.Log("NIC/status", fmt.Sprintf("%s rx=%s tx=%s in_errors=%d out_errors=%d in_discards=%d out_discards=%d (last %.0fs)",
			describe(st.ad), bps(rate(d.InOctets, secs)), bps(rate(d.OutOctets, secs)),
			d.InErrors, d.OutErrors, d.InDiscards, d.OutDiscards, secs))
		st.statusCounters, st.statusAt = st.counters, st.countAt
	}
}

// describe is the adapter's one-line summary.
func describe(ad winnet.Adapter) string {
	return fmt.Sprintf("[%s] %s type=%s speed=%dMbps ip=%s gw=%s dns=%s mac=%s",
		ad.Name, ad.Description, winnet.TypeName(ad.Type), ad.SpeedMbps,
		list(ad.IPv4), list(ad.Gateways), list(ad.DNSServers), ad.MAC)
}

func list(s []string) string {
	if len(s) == 0 {
		return "-"
	}
	return strings.Join(s, ",")
}

// diffCounters is how much each counter grew. Counters restart when an adapter
// resets; a counter that went down counts from zero (same rule as the Wi-Fi stats).
func diffCounters(cur, prev winnet.IfCounters) winnet.IfCounters {
	d := func(c, p uint64) uint64 {
		if c >= p {
			return c - p
		}
		return c
	}
	return winnet.IfCounters{
		InOctets: d(cur.InOctets, prev.InOctets), OutOctets: d(cur.OutOctets, prev.OutOctets),
		InPackets: d(cur.InPackets, prev.InPackets), OutPackets: d(cur.OutPackets, prev.OutPackets),
		InErrors: d(cur.InErrors, prev.InErrors), OutErrors: d(cur.OutErrors, prev.OutErrors),
		InDiscards: d(cur.InDiscards, prev.InDiscards), OutDiscards: d(cur.OutDiscards, prev.OutDiscards),
	}
}

// rate converts bytes over seconds to bits per second.
func rate(bytes uint64, secs float64) float64 {
	if secs <= 0 {
		return 0
	}
	return float64(bytes) * 8 / secs
}

// bps formats bits per second for people.
func bps(v float64) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%.1fGbps", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.1fMbps", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.1fkbps", v/1e3)
	}
	return fmt.Sprintf("%.0fbps", v)
}
