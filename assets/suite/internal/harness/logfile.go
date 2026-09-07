package harness

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// LogFile is a cursor-based reader over the service's log.
//
// Treat the log as a stream with a read position: a wait scans forward from
// the cursor and, on a match, moves the cursor past the matched line. That is
// what stops one scenario matching an earlier scenario's line — a scenario can
// only ever see what happened after it started.
type LogFile struct {
	path   string
	cursor int64
	poll   time.Duration
}

// Line is one matched log line and the offset just past it.
type Line struct {
	Text string
	End  int64
}

// OpenLog returns a reader positioned at the end of an existing file, or at
// the start of one the service has not created yet.
func OpenLog(path string) (*LogFile, error) {
	l := &LogFile{path: path, poll: 20 * time.Millisecond}
	if info, err := os.Stat(path); err == nil {
		l.cursor = info.Size()
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat log %s: %w", path, err)
	}
	return l, nil
}

// Path is the file being read.
func (l *LogFile) Path() string { return l.path }

// Cursor saves the current read position, for Rewind.
func (l *LogFile) Cursor() int64 { return l.cursor }

// Rewind moves the read position back to a saved offset. Use it when several
// lines may arrive in any order: save the cursor, wait for one, rewind, then
// scan the whole window rather than chaining waits in a guessed order.
func (l *LogFile) Rewind(offset int64) { l.cursor = offset }

// Drain jumps to the end, discarding anything not yet read.
func (l *LogFile) Drain() error {
	info, err := os.Stat(l.path)
	if os.IsNotExist(err) {
		l.cursor = 0
		return nil
	}
	if err != nil {
		return err
	}
	l.cursor = info.Size()
	return nil
}

// Since returns the lines written after the cursor, leaving it unmoved.
func (l *LogFile) Since() ([]string, error) {
	text, _, err := l.read()
	if err != nil {
		return nil, err
	}
	return splitLines(text), nil
}

// WaitFor scans forward for the first line containing every fragment, and
// moves the cursor past it. A missing line is an error naming what was waited
// for, never a silent timeout.
func (l *LogFile) WaitFor(ctx context.Context, timeout time.Duration, fragments ...string) (Line, error) {
	deadline := time.Now().Add(timeout)
	for {
		text, base, err := l.read()
		if err != nil {
			return Line{}, err
		}
		offset := base
		for _, raw := range splitLinesKeepEnds(text) {
			end := offset + int64(len(raw))
			line := strings.TrimRight(raw, "\r\n")
			if containsAll(line, fragments) {
				l.cursor = end
				return Line{Text: line, End: end}, nil
			}
			offset = end
		}
		if time.Now().After(deadline) {
			return Line{}, fmt.Errorf("no log line containing %v within %s (searched %s from offset %d)",
				fragments, timeout, l.path, base)
		}
		select {
		case <-ctx.Done():
			return Line{}, ctx.Err()
		case <-time.After(l.poll):
		}
	}
}

// Absent asserts no matching line appears within the window, and leaves the
// cursor where it was. Use it only after a marker proving the service already
// handled the input — otherwise it races the service and passes by luck.
func (l *LogFile) Absent(ctx context.Context, window time.Duration, fragments ...string) error {
	saved := l.cursor
	defer func() { l.cursor = saved }()

	timer := time.NewTimer(window)
	defer timer.Stop()
	for {
		text, _, err := l.read()
		if err != nil {
			return err
		}
		for _, line := range splitLines(text) {
			if containsAll(line, fragments) {
				return fmt.Errorf("log line containing %v was written but should not have been: %s", fragments, line)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case <-time.After(l.poll):
		}
	}
}

func (l *LogFile) read() (string, int64, error) {
	f, err := os.Open(l.path)
	if os.IsNotExist(err) {
		return "", l.cursor, nil
	}
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	if _, err := f.Seek(l.cursor, io.SeekStart); err != nil {
		return "", 0, err
	}
	body, err := io.ReadAll(f)
	if err != nil {
		return "", 0, err
	}
	return string(body), l.cursor, nil
}

func containsAll(line string, fragments []string) bool {
	for _, f := range fragments {
		if !strings.Contains(line, f) {
			return false
		}
	}
	return len(fragments) > 0
}

func splitLines(text string) []string {
	var out []string
	for _, raw := range splitLinesKeepEnds(text) {
		if line := strings.TrimRight(raw, "\r\n"); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func splitLinesKeepEnds(text string) []string {
	var out []string
	for len(text) > 0 {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			break // a partial line: leave it for the next read
		}
		out = append(out, text[:i+1])
		text = text[i+1:]
	}
	return out
}
