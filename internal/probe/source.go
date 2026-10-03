// Package probe checks connectivity on a timer: DNS lookup time, default gateway
// ping, TCP connect + ping to configured endpoints, and timed HTTP requests. Its
// Tracer runs path traces (traceroute) on a slower timer and when incidents fire.
package probe

import (
	"context"
	"fmt"
	"sync"
	"time"

	"exmon/internal/event"
)

// Metric names and which direction is bad.
var metricDirections = map[string]event.Direction{
	"dns_ms":           event.HighIsBad,
	"gateway_rtt_ms":   event.HighIsBad,
	"gateway_loss_pct": event.HighIsBad,
	"tcp_connect_ms":   event.HighIsBad,
	"rtt_avg_ms":       event.HighIsBad,
	"loss_pct":         event.HighIsBad,
	"reachable":        event.LowIsBad,
	"http_total_ms":    event.HighIsBad,
	"http_ttfb_ms":     event.HighIsBad,
	"http_tls_ms":      event.HighIsBad,
	"http_ok":          event.LowIsBad,
}

// Source implements source.Source for the connectivity probes.
type Source struct {
	cfg   Config
	wasUp map[string]bool // probe name -> up at the last round, for down/up transitions
}

// New creates the probe source.
func New(cfg Config) *Source { return &Source{cfg: cfg, wasUp: map[string]bool{}} }

// Name implements source.Source.
func (s *Source) Name() string { return "probe" }

// Run implements source.Source: one round now, then every IntervalSeconds.
func (s *Source) Run(ctx context.Context, log event.Logger) error {
	log.DeclareMetrics(metricDirections)
	ticker := time.NewTicker(time.Duration(s.cfg.IntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		s.round(ctx, log)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// round runs every probe at once and logs the results in a fixed order.
func (s *Source) round(ctx context.Context, log event.Logger) {
	timeout := time.Duration(max(1, s.cfg.TimeoutSeconds)) * time.Second
	count := max(1, s.cfg.PingCount)

	// Each probe gets a slot in results; goroutines fill their own slot, so they
	// never write to the same memory and need no lock.
	var jobs []func() result
	for _, n := range s.cfg.DNSNames {
		jobs = append(jobs, func() result { return checkDNS(n) })
	}
	jobs = append(jobs, func() result { return checkGateway(count, timeout) })
	for _, e := range s.cfg.Endpoints {
		jobs = append(jobs, func() result { return checkEndpoint(ctx, e, count, timeout) })
	}
	for _, h := range s.cfg.HTTPTests {
		jobs = append(jobs, func() result { return checkHTTP(ctx, h, 3*timeout) })
	}

	results := make([]result, len(jobs))
	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = job()
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return // shutting down; results may just be cancellations
	}

	for _, r := range results {
		log.Log(r.kind, r.detail)
		if len(r.metrics) > 0 {
			log.Metrics(r.name, r.metrics)
		}
		if kind, ok := s.transition(r); ok {
			log.Log(kind, fmt.Sprintf("[%s] %s", r.name, r.detail))
		}
	}
}

// transition returns PROBE/down or PROBE/up when a probe's state flips. A probe
// that is down on its very first round counts as a transition to down.
func (s *Source) transition(r result) (string, bool) {
	prev, seen := s.wasUp[r.name]
	s.wasUp[r.name] = r.up
	switch {
	case !r.up && (!seen || prev):
		return "PROBE/down", true
	case r.up && seen && !prev:
		return "PROBE/up", true
	}
	return "", false
}
