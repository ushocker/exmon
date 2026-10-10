package probe

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"exmon/internal/winnet"
)

// result is the outcome of one probe: a log line, its metrics, and whether the
// target counts as up (for PROBE/down and PROBE/up transitions).
type result struct {
	name    string // subject: the probe's name
	kind    string // "PROBE/dns", "PROBE/gateway", ...
	detail  string
	metrics map[string]float64
	up      bool
}

// pingStats summarises several pings.
type pingStats struct {
	sent, recv    int
	min, avg, max time.Duration
}

func (p pingStats) lossPct() float64 {
	if p.sent == 0 {
		return 0
	}
	return float64(p.sent-p.recv) * 100 / float64(p.sent)
}

func (p pingStats) String() string {
	if p.recv == 0 {
		return fmt.Sprintf("ping %d/%d lost", p.sent, p.sent)
	}
	return fmt.Sprintf("ping min/avg/max=%s/%s/%s loss=%.0f%%", ms(p.min), ms(p.avg), ms(p.max), p.lossPct())
}

// pingN pings addr count times, one after another, with a short gap.
func pingN(addr string, count int, timeout time.Duration) pingStats {
	s := pingStats{sent: count}
	var total time.Duration
	for i := 0; i < count; i++ {
		r, err := winnet.Ping(addr, 0, timeout)
		if err == nil && r.OK() {
			s.recv++
			total += r.RTT
			if s.recv == 1 || r.RTT < s.min {
				s.min = r.RTT
			}
			s.max = max(s.max, r.RTT)
		}
		if i < count-1 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if s.recv > 0 {
		s.avg = total / time.Duration(s.recv)
	}
	return s
}

// checkDNS times an uncached lookup of name.
func checkDNS(name string) result {
	start := time.Now()
	addrs, err := winnet.LookupA(name)
	took := time.Since(start)
	r := result{name: "dns:" + name, kind: "PROBE/dns", metrics: map[string]float64{}}
	if err != nil {
		r.detail = fmt.Sprintf("[%s] FAILED after %s: %v", name, ms(took), err)
		return r
	}
	r.up = true
	r.metrics["dns_ms"] = msf(took)
	r.detail = fmt.Sprintf("[%s] %s -> %s", name, ms(took), first(addrs))
	return r
}

// checkGateway pings the default gateway.
func checkGateway(count int, timeout time.Duration) result {
	r := result{name: "gateway", kind: "PROBE/gateway", metrics: map[string]float64{}}
	ads, err := winnet.Adapters()
	if err != nil {
		r.detail = "adapters: " + err.Error()
		return r
	}
	routes, err := winnet.DefaultRoutes()
	if err != nil {
		r.detail = "routes: " + err.Error()
		return r
	}
	route, ad, ok := winnet.BestDefaultRoute(routes, ads)
	if !ok {
		r.detail = "no default route"
		return r
	}
	if route.NextHop == "0.0.0.0" { // on-link default route (e.g. a full-tunnel VPN): nothing to ping
		r.up = true
		r.detail = fmt.Sprintf("default route is on-link via %s; no gateway to ping", ad.Name)
		return r
	}
	p := pingN(route.NextHop, count, timeout)
	r.up = p.recv > 0
	r.metrics["gateway_loss_pct"] = p.lossPct()
	if p.recv > 0 {
		r.metrics["gateway_rtt_ms"] = msf(p.avg)
	}
	r.detail = fmt.Sprintf("[%s via %s] %s", route.NextHop, ad.Name, p)
	return r
}

// checkEndpoint times a TCP connect and pings the endpoint.
func checkEndpoint(ctx context.Context, e Endpoint, count int, timeout time.Duration) result {
	r := result{name: e.Name, kind: "PROBE/endpoint", metrics: map[string]float64{}}
	target := net.JoinHostPort(e.Target, strconv.Itoa(e.Port))

	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := guardedDialContext(&d)(ctx, "tcp4", target)
	took := time.Since(start)
	tcp := ""
	remote := ""
	if err != nil {
		tcp = fmt.Sprintf("tcp FAILED after %s: %v", ms(took), err)
	} else {
		remote = conn.RemoteAddr().(*net.TCPAddr).IP.String()
		conn.Close()
		r.up = true
		r.metrics["tcp_connect_ms"] = msf(took)
		tcp = fmt.Sprintf("tcp %s", ms(took))
	}

	// Ping the address the TCP connect reached, or resolve it if the connect failed.
	if remote == "" {
		if addrs, err := net.DefaultResolver.LookupIP(ctx, "ip4", e.Target); err == nil && len(addrs) > 0 && !blocked(addrs[0]) {
			remote = addrs[0].String()
		}
	}
	pingText := "ping skipped (no address)"
	if remote != "" {
		p := pingN(remote, count, timeout)
		pingText = p.String()
		r.metrics["loss_pct"] = p.lossPct()
		if p.recv > 0 {
			r.metrics["rtt_avg_ms"] = msf(p.avg)
		}
	}
	r.metrics["reachable"] = b2f(r.up)
	r.detail = fmt.Sprintf("[%s] %s (%s) %s %s", e.Name, target, or(remote, "?"), tcp, pingText)
	return r
}

func ms(d time.Duration) string { return fmt.Sprintf("%.1fms", msf(d)) }
func msf(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func first(s []string) string {
	if len(s) == 0 {
		return "-"
	}
	return s[0]
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
