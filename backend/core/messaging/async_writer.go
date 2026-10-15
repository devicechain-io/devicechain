// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// ErrAsyncWriterClosed is what a Submit or Settle made after Close reports, and what one
// still waiting for the writer to take it when Close is called reports.
var ErrAsyncWriterClosed = errors.New("messaging: async writer is closed")

// errSettled is the internal outcome of a submission that publishes nothing (a Settle marker,
// an empty Submit). It is only ever a vehicle for OrderedWriter.Fail, which takes a non-nil
// error, and is turned into a nil before a caller sees it.
var errSettled = errors.New("messaging: internal: nothing to publish")

// errPieceNotSent is the outcome of a piece of a multi-message call that was not published
// because an earlier piece of the same call had already failed. A caller never sees it: the
// call's done carries that earlier piece's error, which is always reported first.
var errPieceNotSent = errors.New("messaging: internal: an earlier piece of the call failed")

// asyncSubmitter is what the fan-in drives: the orderedWriter, or a recording double in a
// test that has to observe the fan-in itself (one submission at a time, in what order) as an
// assertion rather than as the orderedWriter's misuse panic.
type asyncSubmitter interface {
	publishGuarded(ctx context.Context, msg Message, done func(error), guard func() error)
	Fail(err error, done func(error))
	Draining()
	Close()
}

// asyncWriter is the AsyncWriter: a fan-in in front of one orderedWriter.
//
// Any number of goroutines send into ONE unbuffered channel; one internal goroutine receives
// and is the orderedWriter's single submitter. So the orderedWriter's "one submitter" rule
// holds without the callers having to honour it, a send returns only once the internal
// goroutine has taken the item (the item is then ahead of anything the same caller sends
// later: that is the per-goroutine FIFO), and a caller is held back by the window through
// the channel instead of by a panic.
//
// The channel is never closed. Close closes quit instead, which both stops the internal
// goroutine and releases every caller still waiting to send, so Close never waits on a
// caller parked behind a full window, and such a caller never waits on Close.
type asyncWriter struct {
	nmgr   *NatsManager
	suffix string
	ow     asyncSubmitter
	refuse bool

	in   chan asyncItem
	quit chan struct{}

	// mu orders starting the internal goroutine against Close. The goroutine starts on first
	// use, so a writer built and never used owns none; one started after Close had looked
	// would submit into a closed orderedWriter.
	mu       sync.Mutex
	closed   bool
	started  bool
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
// *BackpressureError while the gate is closed, and its messages are counted in
// jetstream_publish_refused_total. False never refuses (the forwarding-hop rule
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

func newAsyncWriter(nmgr *NatsManager, suffix string, ow asyncSubmitter, refuse bool) *asyncWriter {
	return &asyncWriter{
		nmgr:     nmgr,
		suffix:   suffix,
		ow:       ow,
		refuse:   refuse,
		in:       make(chan asyncItem),
		quit:     make(chan struct{}),
		loopDone: make(chan struct{}),
	}
}

// send hands it to the internal goroutine and returns nil, or returns why it did not: the
// writer is closed (or closes while it waits), or ctx ended first. It is the ONLY way in, so
// an item is either taken, and its outcome then comes from the internal goroutine, or
// refused here; never both and never neither.
func (w *asyncWriter) send(it asyncItem) error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return ErrAsyncWriterClosed
	}
	if !w.started {
		w.started = true
		go w.loop()
	}
	w.mu.Unlock()
	if err := it.ctx.Err(); err != nil {
		return err
	}
	select {
	case w.in <- it:
		return nil
	case <-it.ctx.Done():
		return it.ctx.Err()
	case <-w.quit:
		return ErrAsyncWriterClosed
	}
}

// Submit queues msgs. See the interface.
func (w *asyncWriter) Submit(ctx context.Context, msgs []Message, done func(error)) {
	if done == nil {
		panic("messaging: AsyncWriter.Submit needs a done callback: it is the only place the outcome goes")
	}
	if err := w.send(asyncItem{ctx: ctx, msgs: msgs, done: done}); err != nil {
		done(err)
	}
}

// Settle waits for every submission made before it to have had its done run.
func (w *asyncWriter) Settle(ctx context.Context) error {
	settled := make(chan struct{})
	// The marker publishes nothing, so one taken and then abandoned (ctx ends while it waits
	// for its turn) costs a window slot for an instant and nothing else.
	if err := w.send(asyncItem{ctx: ctx, done: func(error) { close(settled) }}); err != nil {
		return err
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

// Close refuses further work, settles what the writer took and stops its goroutine.
func (w *asyncWriter) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	started := w.started
	w.mu.Unlock()
	close(w.quit)
	// Nothing new will be taken, so there is no backlog left for a failure backoff to
	// protect. Ending it bounds what follows by the publish ceiling, not ceiling + backoff.
	w.ow.Draining()
	if started {
		<-w.loopDone
	}
	w.ow.Close()
}

// loop is the orderedWriter's ONE submitter.
func (w *asyncWriter) loop() {
	defer close(w.loopDone)
	for {
		select {
		case it := <-w.in:
			w.dispatch(it)
		case <-w.quit:
			return
		}
	}
}

func (w *asyncWriter) dispatch(it asyncItem) {
	if len(it.msgs) == 0 {
		// Nothing to publish, but the outcome still takes its place in the order.
		w.ow.Fail(errSettled, func(error) { it.done(nil) })
		return
	}
	// done fires on the last piece's outcome and carries the first error, which is that of
	// the first piece to fail: pieces are reported in order.
	//
	// failed is set by the call's first failure: a piece's outcome (on the settle goroutine)
	// or a refusal (on this one). A piece not yet sent when it is set is not sent. guard runs
	// once the piece holds its window slot, and a slot is freed only after the outcome that
	// held it was reported, so a piece that waited for room behind its failed sibling always
	// sees the failure. Pieces already in flight cannot be recalled.
	var failed atomic.Bool
	remaining := len(it.msgs)
	var first error
	piece := func(err error) {
		if err != nil && first == nil {
			first = err
			failed.Store(true)
		}
		if remaining--; remaining == 0 {
			it.done(first)
		}
	}
	notAfterAFailure := func() error {
		if failed.Load() {
			return errPieceNotSent
		}
		return nil
	}
	for i := range it.msgs {
		guard := notAfterAFailure
		if i == 0 && w.refuse && !bypassesBackpressure(it.msgs) {
			// The whole call is refused or none of it is, as natsWriter.publish does: the gate
			// is read once, as the first piece is about to go (after its tenant is checked),
			// and a refusal there stops every piece.
			guard = func() error {
				if err := w.nmgr.Backpressure(w.suffix); err != nil {
					w.nmgr.countRefused(StreamName(w.nmgr.Microservice.InstanceId, w.suffix), len(it.msgs))
					failed.Store(true)
					return err
				}
				return nil
			}
		}
		w.ow.publishGuarded(it.ctx, it.msgs[i], piece, guard)
	}
}
