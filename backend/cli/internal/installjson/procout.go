// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installjson

import (
	"bytes"
	"io"
	"os"
	"sync"
	"time"
)

// processOutputDrain bounds how long restoring the process's streams waits for the
// redacting pipe and its destination to drain. Neither a child holding the pipe
// open nor a stalled stderr consumer may hold the run hostage. A variable so a
// test can shorten it.
var processOutputDrain = 2 * time.Second

type processOutputResult struct {
	stopped bool // the pipe reader exited (a destination Write may still be blocked)
	dropped bool // received output was not fully delivered
}

// redirectProcessOutput points os.Stdout and os.Stderr at a pipe whose every line
// is written to the current os.Stderr through redact, and returns what puts them
// back. restore reports whether the reader stopped and whether output was dropped.
//
// 🔴 THIS IS FOR WHAT dcctl DOES NOT HAND OUT ITSELF. The engine's console is
// already events. What remains is anything that reaches the process's streams
// directly — above all a kubeconfig exec credential plugin, which client-go starts
// with os.Stderr as its stderr — and in json mode all of it belongs on stderr, so
// both streams go there. Lines are redacted whole, so a value split across two
// writes of one line is still found.
//
// Every received byte is retained, in order, through the drain bound, including
// a final line without a newline. After the first bound the read end is closed,
// releasing a reader whose pipe a child still holds. A second bound lets the
// destination finish; only then may pending output be dropped, recorded in the
// result. A permanently blocked io.Writer cannot be canceled: at most its one
// in-flight Write is left behind, holding no reader lock.
func redirectProcessOutput(redact func(string) string) (restore func() processOutputResult, err error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	out, errOut := os.Stdout, os.Stderr
	pr := newPipeReader(errOut, redact)
	go pr.run(r)
	os.Stdout, os.Stderr = w, w
	return func() processOutputResult {
		os.Stdout, os.Stderr = out, errOut
		_ = w.Close()
		select {
		case <-pr.done:
			_ = r.Close()
			return pr.result()
		case <-time.After(processOutputDrain):
		}
		// Closing the read end releases Read, independently of the destination.
		// The reader queues its final partial line before it exits.
		_ = r.Close()
		select {
		case <-pr.done:
			return pr.result()
		case <-time.After(processOutputDrain):
			pr.discard()
			return pr.result()
		}
	}, nil
}

// pipeReader queues whole lines for one writer, preserving their order. Neither
// the reader nor stop waits on dest. Redaction and Write run outside mu.
type pipeReader struct {
	dest     io.Writer
	redact   func(string) string
	done     chan struct{}
	readDone chan struct{}
	wake     chan struct{}

	mu        sync.Mutex
	partial   []byte // read, not yet queued: the current line
	pending   []string
	stopped   bool // no more input; the final partial line is queued
	writing   bool // one line has been handed to dest
	dropped   bool
	discarded bool // the drain bound expired; no more queued writes may start
}

func newPipeReader(dest io.Writer, redact func(string) string) *pipeReader {
	return &pipeReader{dest: dest, redact: redact, done: make(chan struct{}),
		readDone: make(chan struct{}), wake: make(chan struct{}, 1)}
}

func (p *pipeReader) run(r io.Reader) {
	go p.write()
	defer func() { p.stop(); close(p.readDone) }()
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			p.take(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// take retains all of b before handing any complete lines to the writer.
func (p *pipeReader) take(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.discarded {
		p.dropped = p.dropped || len(b) > 0
		return
	}
	p.partial = append(p.partial, b...)
	for {
		i := bytes.IndexByte(p.partial, '\n')
		if i < 0 {
			return
		}
		p.pending = append(p.pending, string(p.partial[:i+1]))
		p.partial = p.partial[i+1:]
		p.notify()
	}
}

// stop queues the final partial line and never waits for a destination write.
func (p *pipeReader) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.stopped = true
	if len(p.partial) > 0 && !p.discarded {
		p.pending = append(p.pending, string(p.partial))
		p.partial = nil
	}
	p.notify()
}

func (p *pipeReader) notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *pipeReader) write() {
	defer func() { <-p.readDone; close(p.done) }()
	for {
		p.mu.Lock()
		if p.discarded || (p.stopped && len(p.pending) == 0) {
			p.mu.Unlock()
			return
		}
		if len(p.pending) == 0 {
			p.mu.Unlock()
			<-p.wake
			continue
		}
		line := p.pending[0]
		p.pending[0] = ""
		p.pending = p.pending[1:]
		p.writing = true
		p.mu.Unlock()

		line = p.redact(line)
		n, err := io.WriteString(p.dest, line)

		p.mu.Lock()
		p.writing = false
		p.dropped = p.dropped || err != nil || n != len(line)
		p.mu.Unlock()
	}
}

// discard abandons pending output only after restore's drain bound expires.
// An arbitrary io.Writer has no cancellation API; its current Write may remain.
func (p *pipeReader) discard() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.discarded = true
	p.dropped = p.dropped || p.writing || len(p.pending) > 0 || len(p.partial) > 0
	p.pending, p.partial = nil, nil
	p.notify()
}

func (p *pipeReader) result() processOutputResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := processOutputResult{dropped: p.dropped}
	select {
	case <-p.readDone:
		result.stopped = true
	default:
	}
	return result
}
