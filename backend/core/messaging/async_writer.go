// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// ErrAsyncWriterClosed is what a Submit or Settle made after Close reports.
var ErrAsyncWriterClosed = errors.New("messaging: async writer is closed")

// errSettled is the internal outcome of a submission that publishes nothing (a Settle marker,
// an empty Submit). It is only ever a vehicle for OrderedWriter.Fail, which takes a non-nil
// error, and is turned into a nil before a caller sees it.
var errSettled = errors.New("messaging: internal: nothing to publish")

// asyncWriter is the AsyncWriter: a fan-in in front of one orderedWriter.
//
// Any number of goroutines send into ONE unbuffered channel; one internal goroutine receives
// and is the orderedWriter's single submitter. So the orderedWriter's "one submitter" rule
// holds without the callers having to honour it, a send returns only once the internal
// goroutine has taken the item (the item is then ahead of anything the same caller sends
// later: that is the per-goroutine FIFO), and a caller is held back by the window through
// the channel instead of by a panic.
type asyncWriter struct {
	nmgr   *NatsManager
	suffix string
	ow     *orderedWriter
	refuse bool

	// mu makes Close wait for every send in progress and keeps a send from reaching a closed
	// channel: Submit and Settle hold it for reading around their send only.
	mu     sync.RWMutex
	closed bool
	in     chan asyncItem

	// The internal goroutine starts on first use, so a writer built and never used owns none.
	start    sync.Once
	started  atomic.Bool
	loopDone chan struct{}
}

type asyncItem struct {
	ctx  context.Context
	msgs []Message
	done func(error)
}

// NewAsyncWriter builds an AsyncWriter for suffix with up to window publishes awaiting their
// acknowledgement at once, on the same terms as NewOrderedWriter (window in [1, 1024], a
// suffix with a tenant-wide publish path; the stream is created if needed).
//
// refuseOnBackpressure makes it consult the stream's gate as MessageWriter does: a Submit
// whose messages do not ALL carry Message.BypassBackpressure is refused whole with a
// *BackpressureError while the gate is closed. False never refuses (the forwarding-hop rule
// NewOrderedWriter documents).
//
// 🔑 BUILD ONE FOR THE LIFE OF THE PROCESS, NOT ONE PER LEASE TERM. Each writer holds a
// reply subscription (and a reconnect goroutine) until the connection closes; Settle is the
// barrier that makes reuse across terms possible.
func (nmgr *NatsManager) NewAsyncWriter(suffix string, window int, refuseOnBackpressure bool) (AsyncWriter, error) {
	ow, err := nmgr.NewOrderedWriter(suffix, window)
	if err != nil {
		return nil, err
	}
	return newAsyncWriter(nmgr, suffix, ow.(*orderedWriter), refuseOnBackpressure), nil
}

func newAsyncWriter(nmgr *NatsManager, suffix string, ow *orderedWriter, refuse bool) *asyncWriter {
	return &asyncWriter{
		nmgr:     nmgr,
		suffix:   suffix,
		ow:       ow,
		refuse:   refuse,
		in:       make(chan asyncItem),
		loopDone: make(chan struct{}),
	}
}

// send hands it to the internal goroutine, or reports false when the writer is closed.
func (w *asyncWriter) send(it asyncItem) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return false
	}
	w.start.Do(func() {
		w.started.Store(true)
		go w.loop()
	})
	w.in <- it
	return true
}

// Submit queues msgs. See the interface.
func (w *asyncWriter) Submit(ctx context.Context, msgs []Message, done func(error)) {
	if done == nil {
		panic("messaging: AsyncWriter.Submit needs a done callback: it is the only place the outcome goes")
	}
	if !w.send(asyncItem{ctx: ctx, msgs: msgs, done: done}) {
		done(ErrAsyncWriterClosed)
	}
}

// Settle waits for every submission made before it to have had its done run.
func (w *asyncWriter) Settle(ctx context.Context) error {
	settled := make(chan struct{})
	sent := make(chan bool, 1)
	go func() {
		// The send can block behind a full window, so it is raced against ctx rather than
		// made inline. An abandoned marker is harmless: it publishes nothing.
		sent <- w.send(asyncItem{ctx: ctx, done: func(error) { close(settled) }})
	}()
	select {
	case ok := <-sent:
		if !ok {
			return ErrAsyncWriterClosed
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-settled:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Draining ends failure backoff; see OrderedWriter.Draining.
func (w *asyncWriter) Draining() { w.ow.Draining() }

// Close refuses further work, settles what the writer holds and stops its goroutine.
func (w *asyncWriter) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	close(w.in)
	w.mu.Unlock()
	if w.started.Load() {
		<-w.loopDone
	}
	w.ow.Close()
}

// loop is the orderedWriter's ONE submitter.
func (w *asyncWriter) loop() {
	defer close(w.loopDone)
	for it := range w.in {
		w.dispatch(it)
	}
}

func (w *asyncWriter) dispatch(it asyncItem) {
	if len(it.msgs) == 0 {
		// Nothing to publish, but the outcome still takes its place in the order.
		w.ow.Fail(errSettled, func(error) { it.done(nil) })
		return
	}
	// The whole call is refused or none of it is, as natsWriter.publish does, and the refusal
	// is reported through Fail so it reaches done in order with everything else.
	if w.refuse && !bypassesBackpressure(it.msgs) {
		if err := w.nmgr.Backpressure(w.suffix); err != nil {
			w.nmgr.countRefused(StreamName(w.nmgr.Microservice.InstanceId, w.suffix), len(it.msgs))
			w.ow.Fail(err, it.done)
			return
		}
	}
	// done fires on the last piece's outcome and carries the first error. Every piece is
	// published even after an earlier one fails: they are already in flight together, which is
	// the point of the window.
	remaining := len(it.msgs)
	var first error
	piece := func(err error) {
		if err != nil && first == nil {
			first = err
		}
		if remaining--; remaining == 0 {
			it.done(first)
		}
	}
	for i := range it.msgs {
		w.ow.Publish(it.ctx, it.msgs[i], piece)
	}
}
