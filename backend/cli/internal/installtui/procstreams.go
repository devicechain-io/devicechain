// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"io"
	"os"
	"sync"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// processStreamDrain bounds how long ending the takeover waits for the pipe to drain:
// a child that still holds the pipe must not hold the run hostage.
var processStreamDrain = 2 * time.Second

// processLogSource is the log source of a line a child wrote to the process's streams.
const processLogSource = "process"

// processStreams is the process's own stdin, stdout and stderr taken for the run.
//
// 🔴 THE ENGINE'S CONSOLE IS NOT EVERYTHING THAT WRITES. A kubeconfig exec credential
// plugin is started by client-go with the process's os.Stderr, and with its os.Stdin
// whenever that is a terminal. On screen that is a second writer drawing over the
// program, its diagnostics unredacted, and a second reader beside the program's. So for
// the run os.Stdout and os.Stderr are a pipe into the hub's log ring, redacted like every
// other line, and os.Stdin is the null device: never a terminal, always at EOF. A plugin
// that may ask runs without asking; one that insists on a terminal fails, saying so. The
// program keeps the real terminal: it was handed the files before the swap.
//
// 🔴 THE TAKEOVER LASTS AS LONG AS THE ENGINE, NOT THE SCREEN. client-go keeps the
// streams it finds when it builds an authenticator, so after a renderer failure the run
// goes on writing to these files: they are never closed or replaced while the engine may
// still run, and the globals are put back only after it has returned. The pipe is closed
// and drained BEFORE the run's summary, so a child's last line, even one with no newline,
// is in the log; a plain fallback prints those lines on stderr.
type processStreams struct {
	in, out, errOut *os.File // the process's own, put back at the end
	null, r, w      *os.File
	log             io.WriteCloser
	drained         chan struct{}

	once    sync.Once
	dropped bool
}

func takeProcessStreams(hub *installflow.Hub) (*processStreams, error) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}
	log, err := hub.LogWriter("", processLogSource, installflow.LogWarn)
	if err != nil {
		_ = null.Close()
		return nil, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		_ = null.Close()
		return nil, err
	}
	s := &processStreams{in: os.Stdin, out: os.Stdout, errOut: os.Stderr, null: null, r: r, w: w, log: log,
		drained: make(chan struct{})}
	go func() {
		defer close(s.drained)
		_, _ = io.Copy(log, r)
	}()
	os.Stdin, os.Stdout, os.Stderr = null, w, w
	return s, nil
}

// end gives the streams back, once the engine has returned: what is in the pipe is
// drained into the log, the last partial line flushed, then the globals restored. It
// reports whether output was dropped because a child still held the pipe.
func (s *processStreams) end() (dropped bool) {
	s.once.Do(func() {
		_ = s.w.Close()
		select {
		case <-s.drained:
		case <-time.After(processStreamDrain):
			// A child still holds the pipe. Closing the read end releases the copy; what
			// it had not read by then is lost, and said so.
			_ = s.r.Close()
			<-s.drained
			s.dropped = true
		}
		_ = s.log.Close() // flushes a final line with no newline
		os.Stdin, os.Stdout, os.Stderr = s.in, s.out, s.errOut
		_ = s.r.Close()
		_ = s.null.Close()
	})
	return s.dropped
}

// streamsEngine runs the engine and ends the takeover the moment it returns, before the
// hub sends the summary. A panic ends it too, on its way to the hub's recovery.
type streamsEngine struct {
	installflow.Engine
	streams *processStreams
	dropped *bool
}

func (e streamsEngine) Run(ctx context.Context, req installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
	defer func() { *e.dropped = e.streams.end() }()
	return e.Engine.Run(ctx, req, rt)
}

// screenOutput is what the program writes to: the latching writer, still a terminal file
// to Bubble Tea when stdout is one. Hiding the descriptor would leave the program
// treating the terminal as a pipe: no size, no resize, and on Windows no VT mode.
func screenOutput(l *latchWriter, stdout io.Writer) io.Writer {
	if f, ok := stdout.(*os.File); ok {
		return latchFile{latchWriter: l, f: f}
	}
	return l
}

// latchFile is a latching writer over a terminal file. Closing it closes nothing.
type latchFile struct {
	*latchWriter
	f *os.File
}

func (l latchFile) Read(p []byte) (int, error) { return l.f.Read(p) }
func (l latchFile) Close() error               { return nil }
func (l latchFile) Fd() uintptr                { return l.f.Fd() }
