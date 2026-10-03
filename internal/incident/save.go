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
			Severity: c.rule.severity(),
			Detail:   fmt.Sprintf("id=%s sev=%d rule=%s error=%q", c.id, c.rule.severity(), c.rule.Name, err.Error())})
		return
	}
	e.prune(c.rule.severity())
	e.pub.Publish(event.Event{Source: SourceName, Kind: "INCIDENT/saved", Subject: c.subject,
		Severity: c.rule.severity(),
		Detail:   fmt.Sprintf("id=%s sev=%d rule=%s path=%q lines=%d", c.id, c.rule.severity(), c.rule.Name, path, len(c.lines))})
}

// writeFile writes incident-<time>-sev<N>-<rule>.log.gz: a header, then the captured lines
// in the same format as the normal log.
func (e *Engine) writeFile(c *capture) (string, error) {
	name := c.id + ".log.gz"
	path := filepath.Join(e.cfg.Dir, name)

	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	// Writers stack like layers: our text -> bufio (batches small writes) -> gzip -> file.
	zw := gzip.NewWriter(f)
	zw.Name = name[:len(name)-len(".gz")] // file name shown when the archive is extracted
	w := bufio.NewWriter(zw)

	fmt.Fprintf(w, "# exmon incident %s\r\n", c.id)
	fmt.Fprintf(w, "# severity:  %d (1 = most serious)\r\n", c.rule.severity())
	fmt.Fprintf(w, "# rule:      %s (%s, forSamples %d)\r\n", c.rule.Name, c.rule, c.rule.samplesNeeded())
	fmt.Fprintf(w, "# subject:   %s\r\n", c.subject)
	fmt.Fprintf(w, "# value:     %g\r\n", c.value)
	fmt.Fprintf(w, "# triggered: %s\r\n", c.at.Format("2006-01-02 15:04:05.000"))
	fmt.Fprintf(w, "# captured:  %v before, until %s\r\n", c.window, time.Now().Format("2006-01-02 15:04:05.000"))
	fmt.Fprintf(w, "# device:    %s\r\n", e.cfg.Device)
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

// sevInName finds the severity in an incident file name ("...-sev2-...").
var sevInName = regexp.MustCompile(`-sev([1-5])-`)

// fileSeverity is the severity of an incident file. Files written before severities
// existed have no "-sevN-" and count as DefaultSeverity.
func fileSeverity(name string) int {
	if m := sevInName.FindStringSubmatch(name); m != nil {
		return int(m[1][0] - '0') // the single digit as a number
	}
	return DefaultSeverity
}

// prune deletes the oldest files of one severity beyond that severity's limit, so
// a flood of low-severity incidents never pushes out the rarer serious ones. The
// names start with the time, so sorting by name sorts by age.
func (e *Engine) prune(sev int) {
	limit := e.cfg.limitFor(sev)
	if limit <= 0 {
		return // unlimited
	}
	all, err := filepath.Glob(filepath.Join(e.cfg.Dir, "incident-*.log.gz"))
	if err != nil {
		return
	}
	var files []string
	for _, f := range all {
		if fileSeverity(filepath.Base(f)) == sev {
			files = append(files, f)
		}
	}
	if len(files) <= limit {
		return
	}
	sort.Strings(files)
	for _, f := range files[:len(files)-limit] {
		os.Remove(f)
	}
}
