// Package eventlog writes events from the bus to a rotating file.
//
// One background goroutine owns the file: it takes events off its bus subscription,
// writes them through a bufio.Writer, rotates the file when it gets too big and
// flushes on a timer. Because only that goroutine touches the file, no mutex is needed.
//
// The same FileSink writes both the normal text log and the JSONL baseline file.
package eventlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"exmon/internal/event"
)

// FileSink writes events to a file.
type FileSink struct {
	cfg    Config
	events <-chan event.Event // our bus subscription; "<-chan" means receive-only
	done   chan struct{}      // closed by the writer goroutine when it has finished

	// Everything below is used only by the writer goroutine.
	file *os.File
	buf  *bufio.Writer
	size int64 // current file size, tracked so we don't stat on every line
}

// StartFileSink opens the file and launches the writer goroutine, which runs until
// the events channel is closed (by Bus.Close).
func StartFileSink(cfg Config, events <-chan event.Event) (*FileSink, error) {
	if cfg.FlushIntervalMs <= 0 {
		cfg.FlushIntervalMs = 1000
	}
	if cfg.MaxFiles < 1 {
		cfg.MaxFiles = 1
	}
	abs, err := filepath.Abs(cfg.Path)
	if err != nil {
		return nil, err
	}
	cfg.Path = abs
	// Rotation renames and deletes files next to the log, so refuse to start on
	// anything that isn't an ordinary file (a folder, a link, a device...).
	// Lstat looks at the entry itself without following links.
	if st, err := os.Lstat(cfg.Path); err == nil && !st.Mode().IsRegular() {
		return nil, fmt.Errorf("log file %s exists but is not a regular file", cfg.Path)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o755); err != nil {
		return nil, err
	}

	s := &FileSink{cfg: cfg, events: events, done: make(chan struct{})}
	if err := s.open(); err != nil {
		return nil, err
	}
	// "go f()" runs f in a new goroutine: a lightweight thread managed by the Go runtime.
	go s.writer()
	return s, nil
}

// Path returns the absolute path of the file.
func (s *FileSink) Path() string { return s.cfg.Path }

// Wait blocks until every event has been written and the file is closed.
// Call it after Bus.Close.
func (s *FileSink) Wait() {
	<-s.done // receiving from a channel blocks until a value arrives or it is closed
}

// writer is the only goroutine that touches the file.
func (s *FileSink) writer() {
	// defer runs this when writer returns, however it returns.
	defer close(s.done)

	ticker := time.NewTicker(time.Duration(s.cfg.FlushIntervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		// select waits on several channels at once and runs whichever case is ready first.
		select {
		case e, ok := <-s.events:
			if !ok { // channel closed and fully drained: the bus is shutting down
				s.flush()
				if s.file != nil {
					s.file.Close()
				}
				return
			}
			s.write(e)
			if len(s.events) == 0 { // caught up: push to disk now rather than waiting for the tick
				s.flush()
			}
		case <-ticker.C:
			s.flush()
		}
	}
}

// FormatLine builds one text log line: "2006-01-02 15:04:05.000  <kind padded to 28> <detail>".
func FormatLine(at time.Time, kind, detail string) string {
	return fmt.Sprintf("%s  %-28s %s", at.Format("2006-01-02 15:04:05.000"), kind, detail)
}

func (s *FileSink) format(e event.Event) string {
	if s.cfg.Format == "jsonl" {
		b, err := json.Marshal(e)
		if err != nil {
			return FormatLine(e.Time, "ERROR", "json: "+err.Error())
		}
		return string(b)
	}
	detail := e.Detail
	if e.Held {
		detail += " [held]" // logged while starting, waking or shutting down; see Event.Held
	}
	return FormatLine(e.Time, e.Kind, detail)
}

func (s *FileSink) write(e event.Event) {
	if e.IsMetrics() && !s.cfg.IncludeMetrics {
		return
	}
	line := s.format(e)
	if s.cfg.Echo {
		fmt.Println(line)
	}
	s.rotateIfNeeded()
	if s.buf == nil { // the file could not be (re)opened; keep echoing, try again next line
		if err := s.open(); err != nil {
			return
		}
	}
	n, err := s.buf.WriteString(line + "\r\n")
	s.size += int64(n)
	if err != nil {
		s.fail("write", err)
	}
}

func (s *FileSink) flush() {
	if s.buf == nil {
		return
	}
	if err := s.buf.Flush(); err != nil {
		s.fail("flush", err)
	}
}

func (s *FileSink) open() error {
	f, err := os.OpenFile(s.cfg.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "log open failed:", err)
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	s.file, s.buf, s.size = f, bufio.NewWriter(f), st.Size()
	return nil
}

// fail reports a file error on stderr and drops the file so the next line reopens it.
// File problems never stop the program.
func (s *FileSink) fail(op string, err error) {
	fmt.Fprintf(os.Stderr, "log %s failed (%s): %v\n", op, s.cfg.Path, err)
	if s.file != nil {
		s.file.Close()
	}
	s.file, s.buf = nil, nil
}

// rotateIfNeeded renames exmon.log -> exmon.1.log -> exmon.2.log ... keeping MaxFiles copies.
func (s *FileSink) rotateIfNeeded() {
	maxBytes := int64(s.cfg.MaxSizeMB) * 1024 * 1024
	if maxBytes <= 0 || s.size < maxBytes || s.buf == nil {
		return
	}
	s.flush()
	if s.file != nil {
		s.file.Close()
	}
	s.file, s.buf = nil, nil

	os.Remove(s.rotated(s.cfg.MaxFiles))
	for i := s.cfg.MaxFiles - 1; i >= 1; i-- {
		os.Rename(s.rotated(i), s.rotated(i+1)) // missing files just return an error we ignore
	}
	if err := os.Rename(s.cfg.Path, s.rotated(1)); err != nil {
		fmt.Fprintln(os.Stderr, "log rotate failed:", err)
	}
	s.open()
}

func (s *FileSink) rotated(n int) string {
	ext := filepath.Ext(s.cfg.Path)
	base := strings.TrimSuffix(s.cfg.Path, ext)
	return fmt.Sprintf("%s.%d%s", base, n, ext)
}
