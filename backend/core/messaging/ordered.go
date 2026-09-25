// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

// maxOrderedWindow bounds NewOrderedWriter's window well under the client library's own
// per-context ceiling on unacknowledged publishes (4000), which the writer leaves in place as
// a backstop rather than as its window. A caller-deadline give-up frees the writer's slot
// while the library still holds that publish for up to publishWait, so a library ceiling
// equal to the window would turn short caller deadlines into ErrTooManyStalledMsgs.
const maxOrderedWindow = 1024

const (
	// orderedBackoffStart is how long the settle loop holds a window slot after the first
	// publish the broker failed or did not answer. It matches what the synchronous writer
	// spends on one failing publish: nats.go retries a no-responders publish twice, 250 ms
	// apart, before giving up.
	orderedBackoffStart = 500 * time.Millisecond
	// orderedBackoffMax caps the doubling. The backoff resets on the first success.
	orderedBackoffMax = 2 * time.Second
)

// orderedWriter is the NATS OrderedWriter. See the interface for the contract; the notes
// here are about how it keeps it.
//
// ONE settle goroutine reads pending in FIFO order and runs every done, so outcomes are
// reported in submission order however the broker's replies arrive. slots bounds the
// window: Publish and Fail take one before submitting, and the settle loop frees it only
// after that submission's done has returned (and after any backoff), so pending can never
// hold more than the window and the send into it never blocks.
type orderedWriter struct {
	nmgr   *NatsManager
	suffix string
	// js is this writer's OWN JetStream context, not the manager's shared one. It carries
	// PublishAsyncTimeout so the library forgets a publish nobody answered instead of
	// holding it forever — the manager's context has no such timeout, and an async publish
	// on it with a lost PubAck would stay pending for the life of the connection. Being
	// private also keeps this writer's reply subscription and its reconnect reset (which
	// fails every pending publish of the CONTEXT) away from every other writer's.
	js nats.JetStreamContext

	slots   chan struct{}
	pending chan orderedPending

	busy   atomic.Bool
	closed atomic.Bool

	// The settle goroutine starts on the first submission, not at construction, so a writer
	// that is built and never used — one built by a service start that then failed and was
	// retried — owns no goroutine.
	start   sync.Once
	started atomic.Bool
	settled chan struct{}

	// backoff is the current failure backoff; 0 after a success. Touched only by the settle
	// goroutine.
	backoff time.Duration

	// draining is closed by Draining; a closed channel ends every backoff at once.
	draining     chan struct{}
	drainingOnce sync.Once
}

// orderedPending is one submission on its way to the settle goroutine.
type orderedPending struct {
	// fut is the publish in flight; nil for an outcome decided without a PubAck to wait
	// for, which err then holds.
	fut nats.PubAckFuture
	err error
	// brokerFailed marks an err the broker side produced (the send itself failed), as
	// opposed to a refusal decided locally; only the former is backed off.
	brokerFailed bool
	sent         time.Time
	// deadline is min(caller deadline, sent+publishWait); callerBound says which.
	deadline    time.Time
	callerBound bool
	done        func(error)
}

// NewOrderedWriter builds an OrderedWriter for suffix with up to window publishes awaiting
// their acknowledgement at once. The stream backing the suffix is created if needed.
//
// It refuses a window outside [1, 1024] and a suffix with no tenant-wide publish path (a
// per-device suffix, a device-events or advisory capture) — at construction, where the
// mistake is a startup error, rather than on every publish.
//
// The writer lives as long as the connection it was built on; Close settles what it has in
// flight. It does not call the context's CleanupPublisher: that closes a channel nats.go's
// status fan-out may be sending on, and the reply subscription and reconnect goroutine it
// would release end anyway when the connection closes.
func (nmgr *NatsManager) NewOrderedWriter(suffix string, window int) (OrderedWriter, error) {
	if window < 1 || window > maxOrderedWindow {
		return nil, fmt.Errorf("messaging: ordered writer window %d is outside [1, %d]", window, maxOrderedWindow)
	}
	if err := refuseTenantWide(suffix); err != nil {
		return nil, err
	}
	if _, err := nmgr.ensureStream(suffix); err != nil {
		return nil, err
	}
	js, err := nmgr.nc.JetStream(nats.PublishAsyncTimeout(publishWait))
	if err != nil {
		return nil, err
	}
	nmgr.metrics.initPublish(suffix, publishModePipelined)
	log.Info().Str("suffix", suffix).Int("window", window).Msg("Added new ordered NATS writer")
	return &orderedWriter{
		nmgr:     nmgr,
		suffix:   suffix,
		js:       js,
		slots:    make(chan struct{}, window),
		pending:  make(chan orderedPending, window),
		settled:  make(chan struct{}),
		draining: make(chan struct{}),
	}, nil
}

// enter claims the writer for one submission, panicking on misuse. The caller releases it
// with o.busy.Store(false).
func (o *orderedWriter) enter(op string) {
	if o.closed.Load() {
		panic("messaging: OrderedWriter." + op + " called after Close")
	}
	if !o.busy.CompareAndSwap(false, true) {
		panic("messaging: OrderedWriter." + op + " called concurrently; an ordered writer has ONE submitter, " +
			"because the order outcomes are reported in is the order of the calls")
	}
	o.start.Do(func() {
		o.started.Store(true)
		go o.settleLoop()
	})
}

// Publish sends msg to the tenant's subject and reports its outcome to done, in order.
func (o *orderedWriter) Publish(ctx context.Context, msg Message, done func(error)) {
	if done == nil {
		panic("messaging: OrderedWriter.Publish needs a done callback: it is the only place the outcome goes")
	}
	o.enter("Publish")
	defer o.busy.Store(false)

	// The slot is taken before anything else, so the ceiling below is measured from the
	// send and not charged for time spent waiting for room in the window. ctx is not read
	// here: see the interface doc.
	o.slots <- struct{}{}

	subject, err := o.nmgr.tenantSubject(ctx, o.suffix, "")
	if err != nil {
		o.pending <- orderedPending{err: err, done: done}
		return
	}
	sent := time.Now()
	deadline, callerBound := sent.Add(publishWait), false
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline, callerBound = d, true
	}
	if callerBound && !sent.Before(deadline) {
		// An expired caller deadline publishes nothing, as it does for MessageWriter.
		o.pending <- orderedPending{err: context.DeadlineExceeded, done: done}
		return
	}
	// 🔴 NO PUBLISH OPTIONS, EVER. nats.go retries an async no-responders publish with a
	// time.AfterFunc, which re-sends it BEHIND whatever was published meanwhile — a retry
	// here is a reorder. A failure is instead reported in order and backed off in the settle
	// loop. (The library also refuses a context or a per-publish timeout on this path; the
	// deadline is enforced by awaitPubAck.)
	fut, err := o.js.PublishMsgAsync(natsMsg(subject, msg))
	if err != nil {
		o.pending <- orderedPending{err: err, brokerFailed: true, done: done}
		return
	}
	o.pending <- orderedPending{fut: fut, sent: sent, deadline: deadline, callerBound: callerBound, done: done}
}

// Fail reports err to done, in order, without publishing anything.
func (o *orderedWriter) Fail(err error, done func(error)) {
	if err == nil {
		panic("messaging: OrderedWriter.Fail needs a non-nil error: a success without a PubAck is not reportable")
	}
	if done == nil {
		panic("messaging: OrderedWriter.Fail needs a done callback: it is the only place the outcome goes")
	}
	o.enter("Fail")
	defer o.busy.Store(false)
	o.slots <- struct{}{}
	o.pending <- orderedPending{err: err, done: done}
}

// Draining ends failure backoff for the rest of this writer's life; see the interface.
func (o *orderedWriter) Draining() {
	o.drainingOnce.Do(func() { close(o.draining) })
}

// Close waits for every submission to settle. Each is bounded by its own deadline (at most
// publishWait from its send), plus any failure backoff ahead of it unless Draining was called.
func (o *orderedWriter) Close() {
	if o.busy.Load() {
		panic("messaging: OrderedWriter.Close called while a submission is in progress")
	}
	if o.closed.Swap(true) {
		return
	}
	close(o.pending)
	if o.started.Load() {
		<-o.settled
	}
}

// settleLoop is the ONLY goroutine that ever calls a done.
func (o *orderedWriter) settleLoop() {
	defer close(o.settled)
	for p := range o.pending {
		err, brokerFailed := p.err, p.brokerFailed
		if p.fut != nil {
			err = awaitPubAck(p.fut, p.deadline, p.callerBound)
			o.nmgr.metrics.observePublish(o.suffix, publishModePipelined, time.Since(p.sent))
			brokerFailed = err != nil
		}
		if err != nil {
			log.Error().Err(err).Str("suffix", o.suffix).Msg("nats write operation failed")
		}
		p.done(err)
		o.pace(p.fut != nil && err == nil, brokerFailed)
		<-o.slots
	}
}

// pace applies the failure backoff before the slot of a settled submission is freed.
//
// 🔑 WITHOUT IT A FAILING STREAM IS DRAINED AT CPU SPEED. A publish the broker fails at once —
// no responders while a stream leader is being elected, or the whole window failed together
// by a reconnect — settles in microseconds and frees its slot, so the submitter pulls the
// next source, which fails too. Each such source is left unacked and spends one of its
// deliveries on the redelivery, so an outage of a few AckWaits would dead-letter the whole
// inbound backlog instead of the handful a synchronous writer (which spends about 500 ms on
// each failing publish) gets through. Holding the slot keeps the failure rate at or below
// that, and doing it here keeps settlement in order. A success resets it, so a healthy
// stream never pays; and once the submitter is draining (Draining) there is no backlog left
// to protect, so it stops.
func (o *orderedWriter) pace(succeeded, brokerFailed bool) {
	switch {
	case succeeded:
		o.backoff = 0
	case brokerFailed:
		if o.backoff == 0 {
			o.backoff = orderedBackoffStart
		} else {
			o.backoff = min(2*o.backoff, orderedBackoffMax)
		}
		t := time.NewTimer(o.backoff)
		defer t.Stop()
		select {
		case <-t.C:
		case <-o.draining:
		}
	}
}

// awaitPubAck waits for fut's outcome until deadline.
//
// 🔑 A RESULT ALREADY IN HAND WINS, WHATEVER THE CLOCK SAYS. A future whose PubAck arrived
// while the settle loop was waiting on an EARLIER one may be past its own deadline by the
// time it is looked at, and a single select over the PubAck and the timer would pick at
// random between them — failing a message the broker has stored, which is a redelivery and
// a duplicate for nothing. So the result is checked without blocking first.
func awaitPubAck(fut nats.PubAckFuture, deadline time.Time, callerBound bool) error {
	select {
	case <-fut.Ok():
		return nil
	case err := <-fut.Err():
		return mapAsyncErr(err)
	default:
	}
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case <-fut.Ok():
		return nil
	case err := <-fut.Err():
		return mapAsyncErr(err)
	case <-t.C:
		if callerBound {
			return context.DeadlineExceeded
		}
		return nats.ErrTimeout
	}
}

// mapAsyncErr reports the library's own async-publish timeout as the broker timeout every
// other publish path reports; everything else is passed through.
func mapAsyncErr(err error) error {
	if errors.Is(err, nats.ErrAsyncPublishTimeout) {
		return nats.ErrTimeout
	}
	return err
}
