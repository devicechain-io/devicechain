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

// MaxOrderedWindow bounds NewOrderedWriter's window well under the client library's own
// per-context ceiling on unacknowledged publishes (4000), which the writer leaves in place as
// a backstop rather than as its window. A caller-deadline give-up frees the writer's slot
// while the library still holds that publish for up to publishWait, so a library ceiling
// equal to the window would turn short caller deadlines into ErrTooManyStalledMsgs.
const MaxOrderedWindow = 1024

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
// hold more than the window and the send into it never blocks. During a failure episode the
// settle loop also keeps some slots back (held); see pace.
type orderedWriter struct {
	nmgr   *NatsManager
	suffix string
	// writer is the producer this writer publishes for: the writer label on its window and
	// duplicate counters.
	writer PublishWriter
	// gate is the stream's backpressure gate, nil for a stream that applies none. The writer
	// never refuses on it (see NewOrderedWriter), but it counts what it publishes against it:
	// a forwarding hop's volume spends the same margin as any other writer's, and brings the
	// next measurement forward in the same way.
	gate *gateState
	// js is this writer's OWN JetStream context, not the manager's shared one. It carries
	// PublishAsyncTimeout so the library forgets a publish nobody answered instead of
	// holding it forever — the manager's context has no such timeout, and an async publish
	// on it with a lost PubAck would stay pending for the life of the connection. Being
	// private also keeps this writer's reply subscription and its reconnect reset (which
	// fails every pending publish of the CONTEXT) away from every other writer's.
	js nats.JetStreamContext

	// Leader affinity (affinity.go). stream is the suffix's stream name; target is the
	// connection its writers should publish on, set by the manager's affinity loop and nil for
	// a writer built by hand; cur is the one this writer publishes on now, touched only by the
	// submitter; mainJS is the context on the manager's own connection and leaderJS one per
	// leader connection; conn is cur's connection (nil: the manager's) for the sampler to
	// read; main is the affinity code's sentinel for the manager's own connection; nextFollow
	// holds off a swap after a window that would not settle.
	stream     string
	target     *atomic.Pointer[pubTarget]
	main       *pubTarget
	cur        *pubTarget
	mainJS     nats.JetStreamContext
	leaderJS   map[*nats.Conn]nats.JetStreamContext
	conn       atomic.Pointer[nats.Conn]
	nextFollow time.Time

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

	// The failure episode, touched only by the settle goroutine (see pace). backoff is the
	// current backoff, 0 after a success; backedOff is when the last backoff ENDED, and a
	// failure of a publish sent before then belongs to it; held counts the slots of such
	// failures kept from the submitter until a success or Draining.
	backoff   time.Duration
	backedOff time.Time
	held      int
	// inEpisode mirrors "a failure episode is open" (a backoff running or set, or slots held)
	// for the submitter, which reads it in follow; written only by the settle goroutine.
	inEpisode atomic.Bool

	// draining is closed by Draining; a closed channel ends every backoff at once.
	draining     chan struct{}
	drainingOnce sync.Once
}

// orderedPending is one submission on its way to the settle goroutine.
type orderedPending struct {
	// fut is the publish in flight; nil for an outcome decided without a PubAck to wait
	// for, which err then holds.
	fut nats.PubAckFuture
	// msg is what fut was sent, kept so a publish whose id was still in process can be sent
	// again (settle).
	msg *nats.Msg
	err error
	// brokerFailed marks an err the broker side produced (the send itself failed), as
	// opposed to a refusal decided locally; only the former is backed off.
	brokerFailed bool
	// reported marks an err the submitter decided and owns (Fail): the settle loop does not
	// log it as a broker failure, because the caller that supplied it says what it was.
	reported bool
	// sent is when the publish was attempted; zero for an outcome decided locally.
	sent time.Time
	// deadline is min(caller deadline, sent+publishWait); callerBound says which.
	deadline    time.Time
	callerBound bool
	done        func(error)
}

// NewOrderedWriter builds an OrderedWriter for suffix with up to window publishes awaiting
// their acknowledgement at once, counting its window and duplicates under writer (see
// PublishWriter). The stream backing the suffix is created if needed.
//
// It refuses a window outside [1, 1024], a writer name outside the PublishWriter vocabulary
// and a suffix with no tenant-wide publish path (a per-device suffix, a device-events or
// advisory capture) — at construction, where the mistake is a startup error, rather than on
// every publish.
//
// 🔴 IT NEVER REFUSES FOR BACKPRESSURE, even on a stream that applies it. Its callers are
// forwarding hops (capture to inbound, inbound to resolved) whose source message is already
// fetched: a refusal here would leave that message unacked to spend another of its
// MaxDeliver deliveries, and a message that spends them all is routed away as poison. Those
// hops stop FETCHING instead — NewReader parks a reader whose area forwards into a gated
// stream (streams.Stream.Forwards) — so what reaches this writer was fetched while the gate
// was open, and at most a fetch batch lands in the margin the gate keeps below the ceiling.
//
// The writer lives as long as the connection it was built on; Close settles what it has in
// flight. It does not call the context's CleanupPublisher: that closes a channel nats.go's
// status fan-out may be sending on, and the reply subscription and reconnect goroutine it
// would release end anyway when the connection closes.
func (nmgr *NatsManager) NewOrderedWriter(suffix string, writer PublishWriter, window int) (OrderedWriter, error) {
	if window < 1 || window > MaxOrderedWindow {
		return nil, fmt.Errorf("messaging: ordered writer window %d is outside [1, %d]", window, MaxOrderedWindow)
	}
	if err := refuseUnknownWriter(writer); err != nil {
		return nil, err
	}
	if err := nmgr.requireWriter(suffix); err != nil {
		return nil, err
	}
	if err := refuseTenantWide(suffix); err != nil {
		return nil, err
	}
	stream, err := nmgr.ensureStream(suffix)
	if err != nil {
		return nil, err
	}
	js, err := nmgr.nc.JetStream(nats.PublishAsyncTimeout(publishWait))
	if err != nil {
		return nil, err
	}
	nmgr.metrics.initPublish(suffix, publishModePipelined)
	nmgr.metrics.initWindow(suffix, string(writer))
	log.Info().Str("suffix", suffix).Str("writer", string(writer)).Int("window", window).Msg("Added new ordered NATS writer")
	w := &orderedWriter{
		nmgr:   nmgr,
		suffix: suffix,
		writer: writer,
		// Registering is idempotent, and a service that forwards into a gated stream has
		// normally registered its gate already, through the reader that parks on it; here it
		// is fetched so the writer can count its volume.
		gate:     nmgr.registerBackpressure(suffix),
		js:       js,
		slots:    make(chan struct{}, window),
		pending:  make(chan orderedPending, window),
		settled:  make(chan struct{}),
		draining: make(chan struct{}),
		stream:   stream,
		leaderJS: map[*nats.Conn]nats.JetStreamContext{},
	}
	w.target = nmgr.aff().registerWriter(w)
	w.main = nmgr.aff().main
	w.cur = w.main
	return w, nil
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
	o.publishGuarded(ctx, msg, done, nil)
}

// publishGuarded is Publish with a last word: guard, when not nil, runs once msg holds its
// window slot and its tenant is known, immediately before the send, and a non-nil answer is
// reported to done, in order, in place of publishing. AsyncWriter decides there both its
// backpressure refusal and whether a piece follows a sibling that already failed, because
// that is the latest moment either can be decided: before the slot, a wait for room would
// sit between the decision and the send.
func (o *orderedWriter) publishGuarded(ctx context.Context, msg Message, done func(error), guard func() error) {
	if done == nil {
		panic("messaging: OrderedWriter.Publish needs a done callback: it is the only place the outcome goes")
	}
	o.enter("Publish")
	defer o.busy.Store(false)

	o.follow()

	// The slot is taken before anything else, so the ceiling below is measured from the
	// send and not charged for time spent waiting for room in the window. ctx is not read
	// here: see the interface doc.
	o.acquire()

	subject, err := o.nmgr.tenantSubject(ctx, o.suffix, "")
	if err != nil {
		o.pending <- orderedPending{err: err, done: done}
		return
	}
	if guard != nil {
		if err := guard(); err != nil {
			o.pending <- orderedPending{err: err, reported: true, done: done}
			return
		}
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
	// deadline is enforced by awaitPubAck.) The one re-send the writer makes, of a publish
	// whose id is still in process, is settle's, which says when that reorders and why it is
	// the lesser cost.
	nm := natsMsg(subject, msg)
	fut, err := o.js.PublishMsgAsync(nm)
	if err != nil {
		o.pending <- orderedPending{err: err, brokerFailed: true, sent: sent, done: done}
		return
	}
	// Counted when sent, not when acknowledged: the measurement it paces is of what the
	// broker holds, and a publish in flight is about to be held.
	// Subject, headers and data, as sent: what natsWriter counts.
	o.gate.notePublished(wireSize(nm))
	o.pending <- orderedPending{fut: fut, msg: nm, sent: sent, deadline: deadline, callerBound: callerBound, done: done}
}

// follow moves the writer onto the connection its stream's target names (affinity.go), when
// that is not the one it publishes on. Only the submitter calls it, before taking a slot.
//
// 🔴 IT CHANGES CONNECTION ONLY WITH AN EMPTY WINDOW. Publishes on one connection reach the
// stream in the order they were sent; publishes on two connections do not. So the submitter
// first takes every slot of the window, which it can only do once every earlier publish has
// settled, then changes connection, then gives the slots back. That costs one round trip per
// leader change. A window that does not settle within affinitySwapSettle is in a failure
// episode (a held slot is never freed until a success); the writer stays where it is and
// tries again on a later submission.
//
// The one exception is a connection that has CLOSED: nothing more can be stored through it,
// what it had in flight is reported failed at its deadline, in order, as on any connection
// loss, and the writer moves at once rather than wait for publishes that cannot settle.
func (o *orderedWriter) follow() {
	if o.target == nil {
		return
	}
	want := o.target.Load()
	curClosed := o.cur.nc != nil && o.cur.nc.IsClosed()
	// Its own connection closed: back to the manager's at once, without waiting for the
	// affinity code's close handler (which runs on the client's callback goroutine, a few
	// publishes later) to say so.
	if curClosed && (want.nc == nil || want.nc.IsClosed()) {
		want = o.main
	}
	if want == o.cur {
		return
	}
	if want.nc != nil && want.nc.IsClosed() {
		return
	}
	if !curClosed {
		if time.Now().Before(o.nextFollow) {
			return
		}
		// 🔴 NOT DURING A FAILURE EPISODE. An episode keeps slots back until a publish
		// succeeds (pace), so the window cannot settle until this submitter publishes again,
		// and waiting for it here would block the very probe that ends the episode: every
		// election that failed publishes would cost a full affinitySwapSettle stall with
		// nothing in flight. The submission goes on the current connection (through a route,
		// which works), and the swap is tried on the next one, after the episode.
		if o.inEpisode.Load() {
			return
		}
		if !o.settleWindow(affinitySwapSettle) {
			o.nextFollow = time.Now().Add(affinitySwapRetry)
			return
		}
	}
	if o.cur.nc == nil {
		o.mainJS = o.js
	}
	js := o.mainJS
	if want.nc != nil {
		var ok bool
		if js, ok = o.leaderJS[want.nc]; !ok {
			var err error
			if js, err = want.nc.JetStream(nats.PublishAsyncTimeout(publishWait)); err != nil {
				return
			}
			for nc := range o.leaderJS {
				if nc.IsClosed() {
					delete(o.leaderJS, nc)
				}
			}
			o.leaderJS[want.nc] = js
		}
	}
	from := o.cur.server
	o.js, o.cur = js, want
	o.conn.Store(want.nc)
	log.Info().Str("suffix", o.suffix).Str("from", from).Str("to", want.server).
		Msg("Ordered writer changed connection (an empty server is this service's own connection)")
}

// settleWindow waits, up to d, until every slot of the window is free, which is when every
// earlier submission has settled. It holds the slots only while it counts them.
func (o *orderedWriter) settleWindow(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	n, ok := 0, true
	for n < cap(o.slots) && ok {
		select {
		case o.slots <- struct{}{}:
			n++
		case <-t.C:
			ok = false
		}
	}
	for ; n > 0; n-- {
		<-o.slots
	}
	return ok
}

// Fail reports err to done, in order, without publishing anything. The settle loop does not
// log err: the caller supplied it, and names what it was.
func (o *orderedWriter) Fail(err error, done func(error)) {
	if err == nil {
		panic("messaging: OrderedWriter.Fail needs a non-nil error: a success without a PubAck is not reportable")
	}
	if done == nil {
		panic("messaging: OrderedWriter.Fail needs a done callback: it is the only place the outcome goes")
	}
	o.enter("Fail")
	defer o.busy.Store(false)
	o.acquire()
	o.pending <- orderedPending{err: err, reported: true, done: done}
}

// acquire takes a window slot, charging any time spent waiting for one to the writer's
// window-full counter. A free slot is taken without reading the clock, so a window with room
// pays nothing for the instrument.
func (o *orderedWriter) acquire() {
	select {
	case o.slots <- struct{}{}:
		return
	default:
	}
	start := time.Now()
	o.slots <- struct{}{}
	o.nmgr.metrics.addWindowFull(o.suffix, string(o.writer), time.Since(start))
}

// Window is how many publishes the writer keeps awaiting their acknowledgement at once.
func (o *orderedWriter) Window() int { return cap(o.slots) }

// Draining ends failure backoff for the rest of this writer's life; see the interface.
func (o *orderedWriter) Draining() {
	o.drainingOnce.Do(func() { close(o.draining) })
}

// Close waits for every submission to settle. Each is bounded by its own deadline (at most
// publishWait from its send), plus the one backoff (at most orderedBackoffMax) a failure
// ahead of it can start, unless Draining was called.
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
			var ack *nats.PubAck
			ack, err = o.settle(p)
			if err == nil && ack != nil && ack.Duplicate {
				o.nmgr.metrics.countDuplicate(o.suffix, string(o.writer))
			}
			o.nmgr.metrics.observePublish(o.suffix, publishModePipelined, time.Since(p.sent))
			brokerFailed = err != nil
		}
		if err != nil && !p.reported {
			log.Error().Err(err).Str("suffix", o.suffix).Msg("nats write operation failed")
		}
		p.done(err)
		n := o.pace(p.fut != nil && err == nil, brokerFailed, p.sent)
		o.inEpisode.Store(o.backoff != 0 || o.held > 0)
		for ; n > 0; n-- {
			<-o.slots
		}
	}
}

// settle waits for p's PubAck, sending p.msg again while the broker answers that its id is
// still in process (duplicateInProcess), until the id resolves or p's deadline passes.
//
// The re-send goes out behind the publishes sent since, which is the reorder the publish path
// refuses nats.go's retry for — but here it moves nothing in the usual case: the id's
// original, which the broker is replicating, was sent before them, and when it commits the
// re-send is answered as its duplicate and stores nothing. Only when the original is dropped
// does the re-send store the message behind them, and reporting a failure instead would have
// the source redelivered an AckWait later and stored later still.
func (o *orderedWriter) settle(p orderedPending) (*nats.PubAck, error) {
	fut := p.fut
	var delay time.Duration
	for {
		ack, err := awaitPubAckResult(fut, p.deadline, p.callerBound)
		if !duplicateInProcess(err) {
			return ack, err
		}
		var again bool
		if delay, again = waitToResendInProcess(delay, p.deadline); !again {
			return nil, err
		}
		if fut, err = o.js.PublishMsgAsync(p.msg); err != nil {
			return nil, err
		}
	}
}

// pace applies the failure backoff to a settled submission and returns how many window
// slots to free: its own, those it releases, or none when it keeps its own back.
//
// 🔑 WITHOUT A BACKOFF A FAILING STREAM IS DRAINED AT CPU SPEED. A publish the broker fails
// at once — no responders while a stream leader is being elected — settles in microseconds
// and frees its slot, so the submitter pulls the next source, which fails too. Each such
// source is left unacked and spends one of its deliveries on the redelivery, so an outage of
// a few AckWaits would dead-letter the whole inbound backlog instead of the handful a
// synchronous writer (which spends about 500 ms on each failing publish) gets through.
//
// 🔑 AND THE BACKOFF IS PAID ONCE PER EPISODE, NOT ONCE PER FAILED PUBLISH. A reconnect fails
// every publish of the window at the same instant. Waiting again for each of them would
// hold the window's outcomes — including those of publishes sent afterwards, which the
// broker stores in milliseconds — for a backoff apiece: minutes at a window of 128, far
// past AckWait, so stored sources would be redelivered. So:
//
//   - a failure of a publish sent AFTER the last backoff ended starts (or escalates) one:
//     it waits, then frees its slot, so exactly one new publish — a probe — can go;
//   - a failure of a publish sent BEFORE it ended was already in flight when the episode
//     began: it is reported without waiting, and its slot is held back, so the submitter
//     does not refill the window behind a broker that is still failing;
//   - a success ends the episode and frees every held slot.
//
// So a failing stream is consumed at one publish per backoff, like the synchronous path,
// plus what was already in flight when it began, and a publish sent after the failures is
// settled within about one backoff of its PubAck.
//
// The residual: a stored publish is reported only after everything ahead of it settles, so
// its source is held here for up to the publish ceiling of the publishes ahead of it plus a
// backoff — seconds, against an AckWait of a minute. A source held past AckWait anyway (a
// caller that stalls in done, a teardown cut short) is redelivered, possibly to another
// pod, while still in flight here; what saves it from a second copy is the caller's
// Message.DedupID and the stream's duplicate window, not this writer. A held slot is never the last one: the
// publish whose failure started the latest backoff freed its own, so the probe always has
// room. Once the submitter is draining (Draining) there is no backlog left to protect, so
// nothing waits and nothing is held.
func (o *orderedWriter) pace(succeeded, brokerFailed bool, sent time.Time) int {
	select {
	case <-o.draining:
		return o.release()
	default:
	}
	switch {
	case succeeded:
		o.backoff, o.backedOff = 0, time.Time{}
		return o.release()
	case !brokerFailed:
		return 1
	case sent.Before(o.backedOff):
		o.held++
		return 0
	}
	o.inEpisode.Store(true)
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
		return o.release()
	}
	o.backedOff = time.Now()
	return 1
}

// release ends the episode's hold: the settled submission's own slot plus every held one.
func (o *orderedWriter) release() int {
	n := o.held + 1
	o.held = 0
	return n
}

// awaitPubAck waits for fut's outcome until deadline.
//
// 🔑 A RESULT ALREADY IN HAND WINS, WHATEVER THE CLOCK SAYS. A future whose PubAck arrived
// while the settle loop was waiting on an EARLIER one may be past its own deadline by the
// time it is looked at, and a single select over the PubAck and the timer would pick at
// random between them — failing a message the broker has stored, which is a redelivery and
// a duplicate for nothing. So the result is checked without blocking first.
func awaitPubAck(fut nats.PubAckFuture, deadline time.Time, callerBound bool) error {
	_, err := awaitPubAckResult(fut, deadline, callerBound)
	return err
}

// awaitPubAckResult is awaitPubAck that also returns the PubAck the broker sent, so the
// caller can read what it says (Duplicate) and not only that it arrived.
func awaitPubAckResult(fut nats.PubAckFuture, deadline time.Time, callerBound bool) (*nats.PubAck, error) {
	select {
	case ack := <-fut.Ok():
		return ack, nil
	case err := <-fut.Err():
		return nil, mapAsyncErr(err)
	default:
	}
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case ack := <-fut.Ok():
		return ack, nil
	case err := <-fut.Err():
		return nil, mapAsyncErr(err)
	case <-t.C:
		if callerBound {
			return nil, context.DeadlineExceeded
		}
		return nil, nats.ErrTimeout
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
