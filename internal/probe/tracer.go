package probe

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"exmon/internal/event"
	"exmon/internal/incident"
	"exmon/internal/winnet"
)

const (
	hopProbes  = 3 // pings per hop
	hopTimeout = time.Second
)

// hop is one router on the path.
type hop struct {
	ttl      int
	from     string // address that answered, "" if nobody did
	name     string // reverse DNS name, if any
	sent     int
	recv     int
	min, avg time.Duration
	max      time.Duration
	reached  bool // the destination itself answered at this TTL
}

// traceRequest asks the worker for one trace. incidentID is set when an incident
// asked for it; the worker releases that incident's hold when done.
type traceRequest struct {
	reason     string // "interval" or "incident"
	incidentID string
}

// Tracer runs path traces to the probe endpoints: every PathTrace.IntervalMinutes,
// and when an incident triggers (holding the incident open until the hops are in).
//
// It is a bus consumer. Its reader goroutine only queues requests; traces run on a
// separate worker goroutine, which is allowed to publish (see the rule on event.Bus).
type Tracer struct {
	cfg    Config
	events <-chan event.Event
	log    event.Logger
	reqs   chan traceRequest
	done   chan struct{}

	// While a trace runs, an incident that triggers is attached to it instead of
	// queueing a second, identical trace. The reader and worker goroutines both use
	// these, so a mutex guards them.
	mu       sync.Mutex
	running  bool
	attached []string // incident ids waiting on the running trace
}

// attach adds an incident to the running trace. It returns false if no trace is
// running.
func (t *Tracer) attach(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.running {
		return false
	}
	t.attached = append(t.attached, id)
	return true
}

// StartTracer starts the tracer. log should be bus.For("pathtrace").
// KindTraceNow is the event that asks the tracer for a path trace now (the
// control plane's trace-now command logs it).
const KindTraceNow = "CONTROL/trace_now"

func StartTracer(cfg Config, events <-chan event.Event, log event.Logger) *Tracer {
	t := &Tracer{cfg: cfg, events: events, log: log, reqs: make(chan traceRequest, 16), done: make(chan struct{})}
	worker := make(chan struct{})
	go func() { t.worker(); close(worker) }()
	go func() { t.reader(); close(t.reqs); <-worker; close(t.done) }()
	return t
}

// Wait blocks until the tracer has stopped (after Bus.Close).
func (t *Tracer) Wait() { <-t.done }

func (t *Tracer) reader() {
	var tick <-chan time.Time // a nil channel never fires, so "off" needs no special case
	if m := t.cfg.PathTrace.IntervalMinutes; m > 0 {
		ticker := time.NewTicker(time.Duration(m) * time.Minute)
		defer ticker.Stop()
		tick = ticker.C
		t.request(traceRequest{reason: "interval"}) // one trace at startup for a baseline
	}
	for {
		select {
		case ev, ok := <-t.events:
			if !ok {
				return
			}
			if ev.Kind == KindTraceNow {
				t.request(traceRequest{reason: "command"}) // a signed trace-now command (control plane)
				continue
			}
			if ev.Kind == "INCIDENT/triggered" && t.cfg.PathTrace.OnIncident {
				id := incident.IDFromDetail(ev.Detail)
				// Hold right away, from a new goroutine (we must not publish from our own
				// reader), so the incident stays open until the trace is done.
				go t.log.Log(incident.KindHold, "id="+id)
				if !t.attach(id) {
					t.request(traceRequest{reason: "incident", incidentID: id})
				}
			}
		case <-tick:
			t.request(traceRequest{reason: "interval"})
		}
	}
}

// request queues a trace without blocking. If the queue is full the request is
// dropped (and an incident's hold released, so it isn't kept open for nothing).
func (t *Tracer) request(r traceRequest) {
	select {
	case t.reqs <- r:
	default:
		if r.incidentID != "" {
			go t.log.Log(incident.KindRelease, "id="+r.incidentID)
		}
	}
}

// worker runs queued traces one at a time. Requests that queued up while a trace
// ran are served by a single new trace.
func (t *Tracer) worker() {
	for r := range t.reqs {
		batch := []traceRequest{r}
		for len(t.reqs) > 0 {
			batch = append(batch, <-t.reqs)
		}
		reason := "interval"
		t.mu.Lock()
		t.running = true
		for _, b := range batch {
			if b.incidentID != "" {
				reason = "incident"
				t.attached = append(t.attached, b.incidentID)
			}
		}
		t.mu.Unlock()

		ids := t.traceAll(reason)

		for _, id := range ids {
			t.log.Log(incident.KindRelease, "id="+id)
		}
	}
}

// finish marks the trace as done and returns the incidents attached to it.
func (t *Tracer) finish() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := t.attached
	t.running, t.attached = false, nil
	return ids
}

// targets are the endpoints, or the DNS names when no endpoints are configured.
func (t *Tracer) targets() []Endpoint {
	if len(t.cfg.Endpoints) > 0 {
		return t.cfg.Endpoints
	}
	var out []Endpoint
	for _, n := range t.cfg.DNSNames {
		out = append(out, Endpoint{Name: n, Target: n})
	}
	return out
}

// traceAll traces every target at once, ends the run, and logs the hops one target
// at a time. It returns the incidents that were attached to this run; the hop lines
// are logged before their holds are released, so they land in those incidents.
func (t *Tracer) traceAll(reason string) []string {
	targets := t.targets()
	results := make([][]hop, len(targets))
	addrs := make([]string, len(targets))
	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, e := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			addrs[i], errs[i] = resolve4(e.Target)
			if errs[i] == nil {
				results[i] = trace(addrs[i], max(1, t.cfg.PathTrace.MaxHops))
			}
		}()
	}
	wg.Wait()

	ids := t.finish() // from here on, a new incident queues a fresh trace
	why := reason
	if len(ids) > 0 {
		why = "incident id=" + strings.Join(ids, ",")
	}
	for i, e := range targets {
		if errs[i] != nil {
			t.log.Log("PATHTRACE/done", fmt.Sprintf("[%s] FAILED: %v (%s)", e.Name, errs[i], why))
			continue
		}
		for _, h := range results[i] {
			t.log.Log("PATHTRACE/hop", fmt.Sprintf("[%s] %s", e.Name, h))
		}
		hops := results[i]
		reached := len(hops) > 0 && hops[len(hops)-1].reached
		t.log.Log("PATHTRACE/done", fmt.Sprintf("[%s] target=%s hops=%d reached=%v (%s)", e.Name, addrs[i], len(hops), reached, why))
	}
	return ids
}

// trace finds the path to addr. Every TTL from 1 to maxHops is probed at the same
// time, so a trace takes a few seconds instead of a few seconds per hop. The path
// is cut at the first hop where the destination itself answered.
func trace(addr string, maxHops int) []hop {
	hops := make([]hop, maxHops)
	var wg sync.WaitGroup
	for i := range hops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hops[i] = probeHop(addr, i+1)
		}()
	}
	wg.Wait()

	for i, h := range hops {
		if h.reached {
			return hops[:i+1]
		}
	}
	// Never reached: drop the trailing hops where nobody answered.
	end := len(hops)
	for end > 0 && hops[end-1].from == "" {
		end--
	}
	return hops[:min(len(hops), end+1)]
}

// probeHop pings addr with a fixed TTL a few times and looks up who answered.
func probeHop(addr string, ttl int) hop {
	h := hop{ttl: ttl, sent: hopProbes}
	var total time.Duration
	for i := 0; i < hopProbes; i++ {
		r, err := winnet.Ping(addr, ttl, hopTimeout)
		if err != nil || !r.Answered() {
			continue
		}
		h.from = r.From
		h.reached = h.reached || r.OK()
		h.recv++
		total += r.RTT
		if h.recv == 1 || r.RTT < h.min {
			h.min = r.RTT
		}
		h.max = max(h.max, r.RTT)
	}
	if h.recv > 0 {
		h.avg = total / time.Duration(h.recv)
	}
	if h.from != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if names, err := net.DefaultResolver.LookupAddr(ctx, h.from); err == nil && len(names) > 0 {
			h.name = strings.TrimSuffix(names[0], ".")
		}
	}
	return h
}

func (h hop) String() string {
	if h.from == "" {
		return fmt.Sprintf("%2d * loss=100%%", h.ttl)
	}
	who := h.from
	if h.name != "" {
		who += " (" + h.name + ")"
	}
	loss := float64(h.sent-h.recv) * 100 / float64(h.sent)
	return fmt.Sprintf("%2d %s min/avg/max=%s/%s/%s loss=%.0f%%", h.ttl, who, ms(h.min), ms(h.avg), ms(h.max), loss)
}

// resolve4 returns an IPv4 address for a host name or address.
func resolve4(target string) (string, error) {
	if ip := net.ParseIP(target); ip != nil && ip.To4() != nil {
		return ip.String(), checkTarget(ip)
	}
	ips, err := net.LookupIP(target)
	if err != nil {
		return "", err
	}
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip.String(), checkTarget(ip)
		}
	}
	return "", fmt.Errorf("%s has no IPv4 address", target)
}
