// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// stdinGate is the one reader of stdin for the life of a run.
//
// 🔴 THE PROGRAM'S READER CAN OUTLIVE THE PROGRAM. When Bubble Tea is killed, or recovers
// a panic, it returns without waiting for its input loop, and a read that loop already
// started is still under way. If the plain prompt then read stdin as well, two readers
// would race for one answer: whichever got the "y" would decide, and the other would
// block. So the program never reads stdin itself. It reads through this gate, every read
// of stdin goes through one lock, and handOver closes the program's side: from then on it
// reads nothing, and whatever its last read was given is passed to the plain prompt.
//
// One goroutine can outlive the program and is left, because nothing outside Bubble Tea
// can reach it: Ultraviolet's input decoder sends each decoded event on the program's
// message channel with an unconditional send. If the program stops while a batch of
// keys is being delivered, the rest of that send blocks forever. The leak is bounded:
// one goroutine and the events of one batch, already read (they were typed to the
// screen), for the life of the process; it never reads stdin again, so it cannot take
// the plain prompt's answer. No release up to the pins fixes it.
//
// On Unix the program reads through the gate, and the gate, not Bubble Tea, owns the
// cancelable reader under that read (see programFile). On a Windows console Bubble Tea
// reads console input records from the console handle directly, which no reader can stand
// in for without losing resize and key events, so there the gate serialises the plain
// prompt's reads only.
type stdinGate struct {
	r      io.Reader
	readMu sync.Mutex // one read of r at a time

	mu         sync.Mutex
	handedOver bool
	stash      []byte // read by the program after the handover; the plain prompt's
	refused    error  // set: the plain prompt may not read at all

	// cr, when set, is what the program's reads wait on r through: the gate's own, created
	// with the program's input and closed once no read of it can be under way.
	cr      cancelreader.CancelReader
	stopped bool // the program reads nothing more

	// input, when set, is stamped with every read that brings the program input: the
	// settling screens' measure of when the keyboard was last used (see settleGate). It
	// is stamped here, on the reader's own goroutine, so input read while the event loop
	// is blocked still counts.
	input *inputClock
}

func newStdinGate(r io.Reader) *stdinGate { return &stdinGate{r: r} }

func (g *stdinGate) over() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.handedOver || g.stopped
}

// programRead is the program's read: nothing after the handover or the stop.
func (g *stdinGate) programRead(p []byte) (int, error) {
	if g.over() {
		return 0, io.EOF
	}
	g.readMu.Lock()
	defer g.readMu.Unlock()
	src, err := g.programSource()
	if src == nil || err != nil {
		return 0, err
	}
	n, err := src.Read(p)
	if n > 0 {
		g.input.stamp()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.handedOver {
		// The answer arrived for the plain prompt while this read was waiting.
		g.stash = append(g.stash, p[:n]...)
		return 0, io.EOF
	}
	return n, err
}

// programSource is what the program's next read reads, nil (with io.EOF) once it may read
// nothing. It is called under readMu.
func (g *stdinGate) programSource() (io.Reader, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.handedOver || g.stopped {
		return nil, io.EOF
	}
	if g.cr != nil {
		return g.cr, nil
	}
	return g.r, nil
}

// stopProgram ends the program's reading, once its Run has returned: a read under way is
// cancelled without taking any input, and every later one reads nothing.
//
// 🔴 THE READER IS CLOSED ONLY AFTER ITS READ HAS RETURNED. Bubble Tea v2.1.1, killed or
// recovering a panic, cancels its cancelable reader and closes it at once, while its input
// loop may still be inside the reader's wait, reading the very descriptors the close tears
// down (a data race inside cancelreader, between its Close and its wait's Fd calls). So the
// program is never given a reader Bubble Tea can close: here the cancel comes first, then
// the read lock, which a read under way holds until it has returned, and only then the
// close. A reader that cannot cancel (a platform without one) is left as it is: a read it
// has under way takes nothing from the plain prompt, to which it passes what it reads.
func (g *stdinGate) stopProgram() {
	g.mu.Lock()
	g.stopped = true
	cr := g.cr
	g.cr = nil
	g.mu.Unlock()
	if cr == nil || !cr.Cancel() {
		return
	}
	g.readMu.Lock()
	defer g.readMu.Unlock()
	_ = cr.Close()
}

func (g *stdinGate) refusal() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.refused
}

// refuse makes every plain read fail with err: for a console the program read past the
// gate, whose reader may still be live, so a second reader must not start beside it.
func (g *stdinGate) refuse(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refused = err
}

// plainRead is the plain prompt's read: what the program was given first, then stdin.
func (g *stdinGate) plainRead(p []byte) (int, error) {
	if err := g.refusal(); err != nil {
		return 0, err // checked before the lock: a live program read may be holding it
	}
	g.readMu.Lock()
	defer g.readMu.Unlock()
	g.mu.Lock()
	if len(g.stash) > 0 {
		n := copy(p, g.stash)
		g.stash = g.stash[n:]
		g.mu.Unlock()
		return n, nil
	}
	g.mu.Unlock()
	return g.r.Read(p)
}

// errNoHiddenRead is a secret question after a fallback that cannot be asked safely.
var errNoHiddenRead = fmt.Errorf("%w: after the terminal UI stopped, a secret is read only from the terminal, "+
	"with nothing typed to the screen still unread; re-run with --ui=plain to answer it", installflow.ErrPromptUnavailable)

// plainReadHidden is the plain prompt's read of a secret: one line from the terminal the
// program was given, with echo off.
//
// 🔴 IT NEVER READS os.Stdin. While the engine runs, os.Stdin is the null device (see
// processStreams), so a hidden read of the process's stdin fails, and a person at the
// terminal could not answer. The terminal is the gate's own file, read under the gate's
// lock. It fails closed, never asking, where an answer could be wrong or seen: a console
// the program may still be reading (refused), a stdin that is not a terminal, or keys typed
// to the screen that the program had read and not used, which may be half a passphrase
// and are wiped.
func (g *stdinGate) plainReadHidden() ([]byte, error) {
	if err := g.refusal(); err != nil {
		return nil, err
	}
	g.readMu.Lock()
	defer g.readMu.Unlock()
	g.mu.Lock()
	typed := len(g.stash) > 0
	clear(g.stash)
	g.stash = nil
	g.mu.Unlock()
	if typed {
		return nil, errNoHiddenRead
	}
	read, ok := terminalHiddenRead(g.r)
	if !ok {
		return nil, errNoHiddenRead
	}
	return read()
}

// terminalHiddenRead is the read of one line with echo off from r's terminal, when r is
// one. Tests replace it.
var terminalHiddenRead = func(r io.Reader) (func() ([]byte, error), bool) {
	f, ok := r.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return nil, false
	}
	return func() ([]byte, error) { return term.ReadPassword(int(f.Fd())) }, true
}

// handOver ends the program's reading and returns the plain prompt's reader.
func (g *stdinGate) handOver() io.Reader {
	g.mu.Lock()
	g.handedOver = true
	g.mu.Unlock()
	return g.handOverLater()
}

// handOverLater is the plain prompt's reader, for a prompt that reads only after
// handOver. Its reads share the gate's lock, so even before then they never overlap the
// program's.
func (g *stdinGate) handOverLater() io.Reader { return readerFunc(g.plainRead) }

// programInput is what the program is given to read, made once, before the program runs.
// Over a terminal file it is still a file to Bubble Tea (it has the descriptor, so the
// program can put it in raw mode and learn the terminal's size), but its reads go through
// the gate. It fails when the reader under it cannot start: the program is then not run.
func (g *stdinGate) programInput() (io.Reader, error) {
	if f, ok := g.r.(*os.File); ok {
		return programFile(g, f)
	}
	return readerFunc(g.programRead), nil
}

// startCancelable gives the program's reads the gate's own cancelable reader over f.
func (g *stdinGate) startCancelable(f *os.File) error {
	cr, err := cancelreader.NewReader(f)
	if err != nil {
		return fmt.Errorf("starting the terminal input reader: %w", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cr = cr
	return nil
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// gatedFile is a terminal file whose reads go through the gate. Closing it closes nothing:
// stdin belongs to the process.
type gatedFile struct {
	g *stdinGate
	f *os.File
}

func (f gatedFile) Read(p []byte) (int, error)  { return f.g.programRead(p) }
func (f gatedFile) Write(p []byte) (int, error) { return f.f.Write(p) }
func (f gatedFile) Close() error                { return nil }
func (f gatedFile) Fd() uintptr                 { return f.f.Fd() }
