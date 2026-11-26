// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"bytes"
	"io"
	"sync"
	"time"
)

// LogEntry is one line held in the hub's log ring.
type LogEntry struct {
	Time   time.Time   `json:"time"`
	Step   StepID      `json:"step,omitempty"`
	Level  LogLevel    `json:"level"`
	Source string      `json:"source"`
	Code   MessageCode `json:"code,omitempty"`
	Text   SafeText    `json:"text"`
}

// LogSnapshot is a copy of the ring. Evicted counts lines that aged out; a
// presenter shows it rather than keeping a copy of its own.
type LogSnapshot struct {
	Lines   []LogEntry `json:"lines"`
	Evicted uint64     `json:"evicted"`
}

// logRing is the single sanitised log store: at most maxLines lines and
// maxBytes bytes of text, oldest first out.
type logRing struct {
	maxLines, maxBytes int
	lines              []LogEntry
	bytes              int
	evicted            uint64
}

func (r *logRing) add(e LogEntry) {
	r.lines = append(r.lines, e)
	r.bytes += len(e.Text.s)
	for len(r.lines) > r.maxLines || r.bytes > r.maxBytes {
		r.bytes -= len(r.lines[0].Text.s)
		r.lines[0] = LogEntry{}
		r.lines = r.lines[1:]
		r.evicted++
	}
}

// Logs returns a copy of the log ring.
func (h *Hub) Logs() LogSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return LogSnapshot{Lines: append([]LogEntry(nil), h.ring.lines...), Evicted: h.ring.evicted}
}

// LogWriter returns a writer that splits tool output into log lines for step.
// Lines are assembled across writes before they are sanitised and redacted,
// so a secret split across two writes is still caught. A line longer than
// MaxTextBytes is cut there and the rest of it discarded. Write never blocks
// on the presenter and always reports the whole write as taken, so a tool's
// output keeps draining whatever the presenter does. Close flushes a final
// unterminated line.
func (h *Hub) LogWriter(step StepID, source string, level LogLevel) (io.WriteCloser, error) {
	if err := checkOptionalID(h.red, "step id", string(step)); err != nil {
		return nil, err
	}
	if err := (LogLine{Level: level, Source: source}).validate(h.red); err != nil {
		return nil, err
	}
	return &lineWriter{h: h, step: step, source: source, level: level}, nil
}

type lineWriter struct {
	h      *Hub
	step   StepID
	source string
	level  LogLevel

	mu       sync.Mutex
	buf      []byte
	skipping bool // discarding the rest of an over-long line
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		chunk := p
		if i >= 0 {
			chunk = p[:i]
		}
		if !w.skipping {
			w.buf = append(w.buf, chunk...)
			if len(w.buf) > MaxTextBytes {
				// Keep one byte over the cap so sanitize marks the truncation.
				w.buf = w.buf[:MaxTextBytes+1]
				w.emit()
				w.skipping = true
			}
		}
		if i < 0 {
			break
		}
		if !w.skipping {
			w.emit()
		}
		w.skipping = false
		p = p[i+1:]
	}
	return n, nil
}

func (w *lineWriter) emit() {
	line := bytes.TrimRight(w.buf, "\r")
	_ = w.h.Emit(Event{Step: w.step, Payload: LogLine{Level: w.level, Source: w.source, Text: Text(string(line))}})
	w.buf = w.buf[:0]
}

func (w *lineWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 && !w.skipping {
		w.emit()
	}
	w.buf, w.skipping = nil, false
	return nil
}

// LogWindow is a run of consecutive lines from the log ring, addressed by
// absolute line number: the first line the run ever logged is 0, and a line keeps
// its number as older lines age out. A presenter keeps a position, never a copy.
type LogWindow struct {
	Lines []LogEntry `json:"lines"`
	// From is the absolute number of Lines[0] (or where it would be).
	From uint64 `json:"from"`
	// First is the number of the oldest line still kept: everything before it has
	// aged out of the ring. End is one past the newest line.
	First uint64 `json:"first"`
	End   uint64 `json:"end"`
}

// LogWindow copies at most n lines of the ring. With tail it returns the newest
// n; otherwise the n starting at from, moved forward to the oldest line still
// kept and back so the window is never past the newest. It copies only the
// window, so a presenter drawing a few rows of a full ring copies a few rows.
func (h *Hub) LogWindow(from uint64, n int, tail bool) LogWindow {
	h.mu.Lock()
	defer h.mu.Unlock()
	first := h.ring.evicted
	end := first + uint64(len(h.ring.lines))
	if n < 0 {
		n = 0
	}
	last := first // the latest start that still fills the window
	if end-first > uint64(n) {
		last = end - uint64(n)
	}
	if tail || from > last {
		from = last
	}
	if from < first {
		from = first
	}
	stop := min(from+uint64(n), end)
	lines := append([]LogEntry(nil), h.ring.lines[from-first:stop-first]...)
	return LogWindow{Lines: lines, From: from, First: first, End: end}
}
