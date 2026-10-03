package remote

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// position is a place in the spool: a segment file number and a byte offset in it.
type position struct {
	Segment int   `json:"segment"`
	Offset  int64 `json:"offset"`
}

func (p position) before(q position) bool {
	return p.Segment < q.Segment || (p.Segment == q.Segment && p.Offset < q.Offset)
}

// Spool is an on-disk queue of Records that survives restarts.
//
//	dir/00000001.jsonl   one JSON Record per line; a new file starts at segmentBytes
//	dir/00000002.jsonl
//	dir/cursor.json      {"segment":1,"offset":1234}: the first byte not yet sent
//
// Records are appended to the newest segment and read from the cursor. Fully sent
// segments are deleted. The appender and sender goroutines both use the spool, so a
// sync.Mutex makes them take turns: Lock() waits until no one else holds it.
type Spool struct {
	mu sync.Mutex

	dir          string
	segmentBytes int64
	maxBytes     int64

	cur      position // first unsent byte
	writeSeg int      // segment being appended to
	w        *os.File
	wSize    int64
	badLines int // lines that could not be decoded and were skipped
}

// OpenSpool opens (or creates) the spool in dir and resumes at the saved cursor.
func OpenSpool(dir string, segmentKB, maxMB int) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Spool{
		dir:          dir,
		segmentBytes: int64(max(1, segmentKB)) * 1024,
		maxBytes:     int64(max(1, maxMB)) * 1024 * 1024,
	}
	if err := s.loadCursor(); err != nil {
		return nil, err
	}

	segs := s.segments()
	if len(segs) == 0 {
		s.writeSeg = max(1, s.cur.Segment)
	} else {
		s.writeSeg = segs[len(segs)-1]
	}
	// The cursor can't point before the oldest file we still have.
	if s.cur.Segment == 0 || (len(segs) > 0 && s.cur.Segment < segs[0]) {
		first := s.writeSeg
		if len(segs) > 0 {
			first = segs[0]
		}
		s.cur = position{Segment: first}
	}

	// A crash in the middle of writing can leave half a line at the end of the
	// newest segment. Cut it off so the next record starts on a fresh line.
	if err := truncateTornTail(s.segPath(s.writeSeg)); err != nil {
		return nil, err
	}
	if err := s.openWriter(); err != nil {
		return nil, err
	}
	return s, nil
}

// Append writes records to disk and syncs, so they survive a crash or power loss.
// It returns how many unsent records were dropped to stay under the size limit.
func (s *Spool) Append(recs []Record) (dropped int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock() // defer runs at return, so the lock is released on every path

	for _, r := range recs {
		b, err := json.Marshal(r)
		if err != nil {
			return 0, err
		}
		n, err := s.w.Write(append(b, '\n'))
		s.wSize += int64(n)
		if err != nil {
			return 0, err
		}
		if s.wSize >= s.segmentBytes {
			if err := s.rollover(); err != nil {
				return 0, err
			}
		}
	}
	if err := s.w.Sync(); err != nil {
		return 0, err
	}
	return s.enforceLimit(), nil
}

// Peek reads up to max records from the cursor without removing them. It returns
// the position just after the last record read; pass that to Commit once the
// records have been delivered.
func (s *Spool) Peek(max int) ([]Record, position, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peekLocked(max)
}

func (s *Spool) peekLocked(max int) ([]Record, position, error) {
	pos := s.cur
	var out []Record
	for len(out) < max {
		f, err := os.Open(s.segPath(pos.Segment))
		if errors.Is(err, fs.ErrNotExist) && pos.Segment < s.writeSeg {
			pos = position{Segment: pos.Segment + 1}
			continue
		}
		if err != nil {
			return out, pos, err
		}
		if _, err := f.Seek(pos.Offset, io.SeekStart); err != nil {
			f.Close()
			return out, pos, err
		}
		r := bufio.NewReader(f)
		for len(out) < max {
			line, err := r.ReadBytes('\n')
			if err != nil {
				break // end of file, or an unfinished line still being written
			}
			pos.Offset += int64(len(line))
			var rec Record
			if json.Unmarshal(line, &rec) != nil {
				s.badLines++
				continue
			}
			out = append(out, rec)
		}
		f.Close()

		if len(out) >= max || pos.Segment >= s.writeSeg {
			break
		}
		pos = position{Segment: pos.Segment + 1} // finished this segment; go to the next
	}
	return out, pos, nil
}

// Pending counts the records not yet sent.
func (s *Spool) Pending() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, _, err := s.peekLocked(math.MaxInt)
	return len(recs), err
}

// Commit marks everything before next as sent, saves the cursor and deletes
// segments that are now fully sent.
func (s *Spool) Commit(next position) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if next.before(s.cur) {
		return nil // the size limit already dropped past this point
	}
	s.cur = next
	if err := s.saveCursor(); err != nil {
		return err
	}
	for _, n := range s.segments() {
		if n < s.cur.Segment {
			os.Remove(s.segPath(n))
		}
	}
	return nil
}

// Close closes the segment being written.
func (s *Spool) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Close()
}

// BadLines returns how many unreadable lines were skipped.
func (s *Spool) BadLines() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.badLines
}

// --- helpers; callers hold s.mu ---

func (s *Spool) segPath(n int) string {
	return filepath.Join(s.dir, fmt.Sprintf("%08d.jsonl", n))
}

// segments returns the segment numbers present on disk, oldest first.
func (s *Spool) segments() []int {
	files, _ := filepath.Glob(filepath.Join(s.dir, "*.jsonl"))
	var nums []int
	for _, f := range files {
		if n, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(f), ".jsonl")); err == nil {
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	return nums
}

func (s *Spool) openWriter() error {
	f, err := os.OpenFile(s.segPath(s.writeSeg), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	s.w, s.wSize = f, st.Size()
	return nil
}

func (s *Spool) rollover() error {
	if err := s.w.Sync(); err != nil {
		return err
	}
	s.w.Close()
	s.writeSeg++
	return s.openWriter()
}

// enforceLimit deletes the oldest segments while the spool is over maxBytes and
// returns how many unsent records were lost. The segment being written is never deleted.
func (s *Spool) enforceLimit() int {
	segs := s.segments()
	sizes := map[int]int64{}
	var total int64
	for _, n := range segs {
		if st, err := os.Stat(s.segPath(n)); err == nil {
			sizes[n] = st.Size()
			total += st.Size()
		}
	}
	dropped := 0
	for _, n := range segs {
		if total <= s.maxBytes || n >= s.writeSeg {
			break
		}
		if n >= s.cur.Segment {
			dropped += s.countLines(n)
		}
		os.Remove(s.segPath(n))
		total -= sizes[n]
		if s.cur.Segment <= n {
			s.cur = position{Segment: n + 1}
		}
	}
	if dropped > 0 {
		s.saveCursor()
	}
	return dropped
}

// countLines counts the unsent records in segment n.
func (s *Spool) countLines(n int) int {
	b, err := os.ReadFile(s.segPath(n))
	if err != nil {
		return 0
	}
	if n == s.cur.Segment && s.cur.Offset <= int64(len(b)) {
		b = b[s.cur.Offset:]
	}
	return bytes.Count(b, []byte{'\n'})
}

func (s *Spool) loadCursor() error {
	b, err := os.ReadFile(filepath.Join(s.dir, "cursor.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &s.cur); err != nil {
		s.cur = position{} // unreadable cursor: start from the oldest segment
	}
	return nil
}

// saveCursor writes to a temporary file and renames it over the old one, so a
// crash leaves either the old cursor or the new one, never half a file.
func (s *Spool) saveCursor() error {
	b, _ := json.Marshal(s.cur)
	tmp := filepath.Join(s.dir, "cursor.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, "cursor.json"))
}

// truncateTornTail cuts a file back to just after its last newline.
func truncateTornTail(path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	keep := bytes.LastIndexByte(b, '\n') + 1 // 0 if there is no newline at all
	if keep == len(b) {
		return nil
	}
	return os.Truncate(path, int64(keep))
}
