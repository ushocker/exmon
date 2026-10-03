package baseline

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"exmon/internal/event"
)

// reservoirSize caps how many values per metric are kept for percentiles, so
// memory stays bounded however long baseline mode runs.
const reservoirSize = 10_000

// Directions tells the summarizer which way is "bad" for a metric. *event.Bus has
// this method, so the bus can be passed in directly.
type Directions interface {
	Direction(source, metric string) event.Direction
}

type metricKey struct{ source, subject, metric string }
type kindKey struct{ source, kind string }

// stat accumulates one metric. It uses "reservoir sampling": once the sample is
// full, each new value replaces a random old one with probability size/count, which
// keeps the sample a fair random pick of everything seen.
type stat struct {
	count    int
	min, max float64
	sample   []float64
}

func (s *stat) add(v float64) {
	s.count++
	if s.count == 1 || v < s.min {
		s.min = v
	}
	if s.count == 1 || v > s.max {
		s.max = v
	}
	if len(s.sample) < reservoirSize {
		s.sample = append(s.sample, v)
	} else if j := rand.IntN(s.count); j < reservoirSize {
		s.sample[j] = v
	}
}

// Summarizer is a bus consumer that writes baseline-summary.txt.
type Summarizer struct {
	path    string
	every   time.Duration
	events  <-chan event.Event
	dirs    Directions
	started time.Time
	metrics map[metricKey]*stat
	kinds   map[kindKey]int
	done    chan struct{}
}

// SummaryPath is where the summary for a given baseline file goes.
func SummaryPath(baselinePath string) string {
	return filepath.Join(filepath.Dir(baselinePath), "baseline-summary.txt")
}

// StartSummarizer launches the summarizer goroutine. It reads until the channel is
// closed, then writes the summary one last time.
func StartSummarizer(cfg Config, events <-chan event.Event, dirs Directions) *Summarizer {
	s := &Summarizer{
		path:    SummaryPath(cfg.Path),
		every:   time.Duration(cfg.SummaryMinutes) * time.Minute,
		events:  events,
		dirs:    dirs,
		started: time.Now(),
		metrics: map[metricKey]*stat{},
		kinds:   map[kindKey]int{},
		done:    make(chan struct{}),
	}
	go s.loop()
	return s
}

// Path returns where the summary is written.
func (s *Summarizer) Path() string { return s.path }

// Wait blocks until the final summary is written.
func (s *Summarizer) Wait() { <-s.done }

func (s *Summarizer) loop() {
	defer close(s.done)
	ticker := time.NewTicker(s.every)
	defer ticker.Stop()
	for {
		select {
		case e, ok := <-s.events:
			if !ok {
				s.write()
				return
			}
			s.add(e)
		case <-ticker.C:
			s.write()
		}
	}
}

func (s *Summarizer) add(e event.Event) {
	if !e.IsMetrics() {
		s.kinds[kindKey{e.Source, e.Kind}]++
		return
	}
	for name, v := range e.Metrics {
		k := metricKey{e.Source, e.Subject, name}
		st := s.metrics[k]
		if st == nil {
			st = &stat{}
			s.metrics[k] = st
		}
		st.add(v)
	}
}

// write replaces the summary file atomically (temp file + rename).
func (s *Summarizer) write() {
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(s.Render(time.Now())), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "baseline summary:", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		fmt.Fprintln(os.Stderr, "baseline summary:", err)
	}
}

// Render builds the summary text.
func (s *Summarizer) Render(now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "exmon baseline summary\r\n")
	fmt.Fprintf(&b, "covering %s to %s (%v)\r\n\r\n",
		s.started.Format("2006-01-02 15:04:05"), now.Format("2006-01-02 15:04:05"), now.Sub(s.started).Round(time.Second))

	// Sort keys so the output is stable. slices.SortFunc takes a compare function
	// that returns <0, 0 or >0, like strings.Compare.
	mkeys := make([]metricKey, 0, len(s.metrics))
	for k := range s.metrics {
		mkeys = append(mkeys, k)
	}
	slices.SortFunc(mkeys, func(a, c metricKey) int {
		return strings.Compare(a.source+"\x00"+a.subject+"\x00"+a.metric, c.source+"\x00"+c.subject+"\x00"+c.metric)
	})

	// tabwriter lines up columns separated by \t.
	fmt.Fprintf(&b, "METRICS\r\n")
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "source\tsubject\tmetric\tcount\tmin\tp5\tp50\tp95\tp99\tmax\t\r\n")
	var suggestions []string
	suggested := map[string]bool{}
	for _, k := range mkeys {
		st := s.metrics[k]
		sorted := slices.Clone(st.sample)
		slices.Sort(sorted)
		p := func(q float64) float64 { return percentile(sorted, q) }
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t\r\n", k.source, k.subject, k.metric, st.count,
			num(st.min), num(p(5)), num(p(50)), num(p(95)), num(p(99)), num(st.max))
		// One suggestion per metric is enough (the first subject's); the rule covers all subjects.
		if line := suggest(k, s.dirs.Direction(k.source, k.metric), p); line != "" && !suggested[k.source+"|"+k.metric] {
			suggested[k.source+"|"+k.metric] = true
			suggestions = append(suggestions, line)
		}
	}
	tw.Flush()

	kkeys := make([]kindKey, 0, len(s.kinds))
	for k := range s.kinds {
		kkeys = append(kkeys, k)
	}
	slices.SortFunc(kkeys, func(a, c kindKey) int { return strings.Compare(a.source+a.kind, c.source+c.kind) })
	fmt.Fprintf(&b, "\r\nEVENTS\r\n")
	tw = tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "source\tkind\tcount\t\r\n")
	for _, k := range kkeys {
		fmt.Fprintf(tw, "%s\t%s\t%d\t\r\n", k.source, k.kind, s.kinds[k])
	}
	tw.Flush()

	fmt.Fprintf(&b, "\r\nSUGGESTED TRIGGERS (a starting point from the observed tail; paste into \"incident\".\"triggers\")\r\n")
	if len(suggestions) == 0 {
		fmt.Fprintf(&b, "  (none yet)\r\n")
	} else {
		fmt.Fprintf(&b, "  %s\r\n", strings.Join(suggestions, ",\r\n  "))
	}
	return b.String()
}

// suggest proposes a trigger at the 5th percentile for "low is bad" metrics and the
// 99th for "high is bad" ones.
func suggest(k metricKey, dir event.Direction, p func(float64) float64) string {
	var op, side string
	var th float64
	switch dir {
	case event.LowIsBad:
		op, side, th = "<", "low", p(5)
	case event.HighIsBad:
		op, side, th = ">", "high", p(99)
	default:
		return ""
	}
	// No "subject": adapter names differ between machines, and a rule without one
	// applies to every subject.
	return fmt.Sprintf(`{ "name": "%s-%s-%s", "source": %q, "metric": %q, "op": %q, "threshold": %s, "forSamples": 3, "severity": 3 }`,
		k.source, k.metric, side, k.source, k.metric, op, num(th))
}

// percentile uses the nearest-rank method on sorted data.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	i := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	return sorted[min(max(i, 0), len(sorted)-1)]
}

// num formats a value with at most one decimal place.
func num(v float64) string {
	if math.IsNaN(v) {
		return "-"
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0")
}
