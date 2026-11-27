// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"io"
	"os"
	"sync"
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
// On Unix Bubble Tea's cancelable reader reads through the gate. On a Windows console it
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
}

func newStdinGate(r io.Reader) *stdinGate { return &stdinGate{r: r} }

func (g *stdinGate) over() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.handedOver
}

// programRead is the program's read: nothing after the handover.
func (g *stdinGate) programRead(p []byte) (int, error) {
	if g.over() {
		return 0, io.EOF
	}
	g.readMu.Lock()
	defer g.readMu.Unlock()
	if g.over() {
		return 0, io.EOF
	}
	n, err := g.r.Read(p)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.handedOver {
		// The answer arrived for the plain prompt while this read was waiting.
		g.stash = append(g.stash, p[:n]...)
		return 0, io.EOF
	}
	return n, err
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

// programInput is what the program is given to read. Over a terminal file it is still a
// file to Bubble Tea (it has the descriptor, so the program can put it in raw mode and
// watch it for input), but its reads go through the gate.
func (g *stdinGate) programInput() io.Reader {
	if f, ok := g.r.(*os.File); ok {
		return gatedFile{g: g, f: f}
	}
	return readerFunc(g.programRead)
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

// Name makes it a file to the cancelable reader too, so the program's input loop can be
// stopped (it waits on the descriptor, and reads through Read once input is ready).
func (f gatedFile) Name() string { return f.f.Name() }
