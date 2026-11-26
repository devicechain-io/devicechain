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
// redacting pipe to reach its end. A child that inherited the pipe (an exec plugin's
// helper still running, say) keeps it open; the run is not held hostage to it. A
// variable so a test can shorten it.
var processOutputDrain = 2 * time.Second

// redirectProcessOutput points os.Stdout and os.Stderr at a pipe whose every line
// is written to the current os.Stderr through redact, and returns what puts them
// back. restore reports whether the reader has stopped.
//
// 🔴 THIS IS FOR WHAT dcctl DOES NOT HAND OUT ITSELF. The engine's console is
// already events. What remains is anything that reaches the process's streams
// directly — above all a kubeconfig exec credential plugin, which client-go starts
// with os.Stderr as its stderr — and in json mode all of it belongs on stderr, so
// both streams go there. Lines are redacted whole, so a value split across two
// writes of one line is still found.
//
// 🔴 RESTORING NEVER LOSES WHAT WAS READ, AND NEVER LEAVES THE READER BEHIND. When the
// pipe reaches its end, a final line with no newline is written as it is. When a
// child keeps the pipe open past processOutputDrain, the partial line already read is
// written, redacted, the read end is closed so the reader's blocked read returns, and
// the reader stops. Only bytes still in the operating system's pipe, which the reader
// never received, are not written.
func redirectProcessOutput(redact func(string) string) (restore func() (stopped bool), err error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	out, errOut := os.Stdout, os.Stderr
	pr := &pipeReader{dest: errOut, redact: redact, done: make(chan struct{})}
	go pr.run(r)
	os.Stdout, os.Stderr = w, w
	return func() bool {
		os.Stdout, os.Stderr = out, errOut
		_ = w.Close()
		select {
		case <-pr.done:
			_ = r.Close()
			return true
		case <-time.After(processOutputDrain):
		}
		// A child still holds the pipe. What was read is written now; the read end
		// is closed, which makes the reader's read return; and the reader stops.
		pr.stop()
		_ = r.Close()
		select {
		case <-pr.done:
			return true
		case <-time.After(processOutputDrain):
			return false
		}
	}, nil
}

// pipeReader copies the pipe to dest a line at a time, redacted.
type pipeReader struct {
	dest   io.Writer
	redact func(string) string
	done   chan struct{}

	mu      sync.Mutex
	partial []byte // read, not yet written: the current line
	stopped bool   // written out at restore; nothing more is written
}

func (p *pipeReader) run(r io.Reader) {
	defer close(p.done)
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			p.take(buf[:n])
		}
		if err != nil {
			p.stop() // the end: a final line with no newline still goes out
			return
		}
	}
}

// take adds b to the current line and writes every complete line.
func (p *pipeReader) take(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.partial = append(p.partial, b...)
	for {
		i := bytes.IndexByte(p.partial, '\n')
		if i < 0 {
			return
		}
		_, _ = io.WriteString(p.dest, p.redact(string(p.partial[:i+1])))
		p.partial = p.partial[i+1:]
	}
}

// stop writes the partial line, redacted, and ends the copying.
func (p *pipeReader) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.stopped = true
	if len(p.partial) > 0 {
		_, _ = io.WriteString(p.dest, p.redact(string(p.partial)))
		p.partial = nil
	}
}
