package incident

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"exmon/internal/event"
	"exmon/internal/eventlog"
)

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// save writes one incident file, prunes old ones and announces the result.
// It runs on its own goroutine, not the engine loop, so a blocking Publish is fine.
func (e *Engine) save(c *capture) {
	path, err := e.writeFile(c)
	if err != nil {
		e.pub.Publish(event.Event{Source: SourceName, Kind: "INCIDENT/save_failed", Subject: c.subject,
			Detail: fmt.Sprintf("rule=%s error=%q", c.rule.Name, err.Error())})
		return
	}
	e.prune()
	e.pub.Publish(event.Event{Source: SourceName, Kind: "INCIDENT/saved", Subject: c.subject,
		Detail: fmt.Sprintf("rule=%s path=%q lines=%d", c.rule.Name, path, len(c.lines))})
}

// writeFile writes incident-<time>-<rule>.log.gz: a header, then the captured lines
// in the same format as the normal log.
func (e *Engine) writeFile(c *capture) (string, error) {
	name := fmt.Sprintf("incident-%s-%s.log.gz",
		c.at.Format("20060102-150405.000"), unsafeChars.ReplaceAllString(c.rule.Name, "-"))
	path := filepath.Join(e.cfg.Dir, name)

	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	// Writers stack like layers: our text -> bufio (batches small writes) -> gzip -> file.
	zw := gzip.NewWriter(f)
	zw.Name = name[:len(name)-len(".gz")] // file name shown when the archive is extracted
	w := bufio.NewWriter(zw)

	host, _ := os.Hostname()
	fmt.Fprintf(w, "# exmon incident\r\n")
	fmt.Fprintf(w, "# rule:      %s (%s, forSamples %d)\r\n", c.rule.Name, c.rule, c.rule.samplesNeeded())
	fmt.Fprintf(w, "# subject:   %s\r\n", c.subject)
	fmt.Fprintf(w, "# value:     %g\r\n", c.value)
	fmt.Fprintf(w, "# triggered: %s\r\n", c.at.Format("2006-01-02 15:04:05.000"))
	fmt.Fprintf(w, "# captured:  %v before, until %s\r\n", c.window, time.Now().Format("2006-01-02 15:04:05.000"))
	fmt.Fprintf(w, "# host:      %s\r\n", host)
	fmt.Fprintf(w, "# lines:     %d\r\n\r\n", len(c.lines))
	for _, ev := range c.lines {
		fmt.Fprintf(w, "%s\r\n", eventlog.FormatLine(ev.Time, ev.Kind, ev.Detail))
	}

	// Close in reverse order; keep the first error.
	err = w.Flush()
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// prune deletes the oldest incident files beyond MaxIncidents. The names start with
// the time, so sorting by name sorts by age.
func (e *Engine) prune() {
	if e.cfg.MaxIncidents <= 0 {
		return
	}
	files, err := filepath.Glob(filepath.Join(e.cfg.Dir, "incident-*.log.gz"))
	if err != nil || len(files) <= e.cfg.MaxIncidents {
		return
	}
	sort.Strings(files)
	for _, f := range files[:len(files)-e.cfg.MaxIncidents] {
		os.Remove(f)
	}
}
