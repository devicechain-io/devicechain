// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"sync/atomic"
	"time"
)

// A CAPACITY READER fetches only as many messages as its workers can start.
//
// 🔴 THE CLOCK IT EXISTS FOR IS THE BROKER'S, AND IT STARTS AT FETCH. JetStream starts a
// message's redelivery clock (AckWait) the moment it hands the message to a pull request —
// not when a worker picks it up. A reader that fetches a full batch in front of a small pool
// of slow workers therefore holds the tail of that batch in process for longer than the
// clock: the broker redelivers it while the first copy is still queued, and both copies are
// handled. For a consumer whose handling is an EXTERNAL SEND — a page to a human, a webhook
// to someone else's server — that is a duplicate nobody can take back.
//
// So a capacity reader carries a fixed number of SLOTS, one per worker. A fetch asks for no
// more messages than there are free slots; each message holds its slot from fetch until its
// disposition; and every message carries the time it was fetched, so the code handling it
// can measure its remaining budget from the moment the broker's clock actually started
// (Message.AckDeadline) rather than from when it happened to be dequeued.
//
// Only readers that ask for it (ReaderWithCapacity) behave this way. Every other reader
// fetches full batches exactly as before, and its messages carry no AckDeadline — so no
// consumer that buffers a full batch can re-base a budget onto a fetch time that does not
// describe its queue.

// slot states. A slot leaves slotHeld exactly once, by whichever of Release and the
// AckWait reclaim gets there first; the loser's call is a no-op.
const (
	slotHeld int32 = iota
	slotReleased
	slotReclaimed
)

// Where a slot's message was when its AckWait ran out, which is the label the
// held-past-AckWait counter carries. A message still in the reader's own buffer was dropped
// there and never handed out; one in a worker was being handled when the broker gave up on it.
const (
	stageBuffer = "buffer"
	stageWorker = "worker"
)

// capacity is a capacity reader's pool of slots. A free slot is a token in the channel, so
// acquiring blocks on an empty channel and returning a slot can never over-fill it: the
// channel's capacity IS the slot count, and each slot returns its token at most once.
type capacity struct {
	tokens  chan struct{}
	ackWait time.Duration

	// heldPastAckWait counts a slot reclaimed by its AckWait timer, by stage. Nil when the
	// manager has no metrics (a manager assembled by hand in a unit test).
	heldPastAckWait func(stage string)
}

func newCapacity(slots int, ackWait time.Duration, heldPastAckWait func(stage string)) *capacity {
	c := &capacity{tokens: make(chan struct{}, slots), ackWait: ackWait, heldPastAckWait: heldPastAckWait}
	for i := 0; i < slots; i++ {
		c.tokens <- struct{}{}
	}
	return c
}

// acquire blocks until at least one slot is free, then takes every other free slot up to max
// without blocking further. It reports false when ctx ends first.
func (c *capacity) acquire(ctx context.Context, max int) (int, bool) {
	select {
	case <-ctx.Done():
		return 0, false
	case <-c.tokens:
	}
	n := 1
	for n < max {
		select {
		case <-c.tokens:
			n++
		default:
			return n, true
		}
	}
	return n, true
}

// put returns n slots to the pool.
func (c *capacity) put(n int) {
	for i := 0; i < n; i++ {
		c.tokens <- struct{}{}
	}
}

// free reports how many slots are free right now.
func (c *capacity) free() int { return len(c.tokens) }

// hold issues a slot for one fetched message stamped at the given fetch time. Its AckWait
// timer starts now and fires at stamp + ackWait — immediately, when that is already past.
func (c *capacity) hold(stamp time.Time) *slot {
	s := &slot{owner: c, deadline: stamp.Add(c.ackWait)}
	s.timer = time.AfterFunc(time.Until(s.deadline), s.reclaim)
	return s
}

// slot is one message's claim on a capacity reader's pool, from fetch to disposition.
//
// 🔴 RELEASE IS STRUCTURAL, NOT REMEMBERED. Every path out of a worker returns the slot:
// Process releases it when the handler returns by any route, Ack releases it after the ack
// is sent (errored or not), and the AckWait timer reclaims it when neither has happened by
// the time the broker has redelivered the message anyway. A disposition branch that forgets
// to release therefore costs at most one AckWait of one slot — never a slot lost for good,
// which would silently shrink the pool one forgotten branch at a time until the reader
// fetched nothing at all.
type slot struct {
	owner    *capacity
	deadline time.Time
	timer    *time.Timer
	state    atomic.Int32
	inWorker atomic.Bool
}

// release returns the slot if it is still held. A second call, or a call after the timer
// reclaimed it, is a no-op — the token was already returned once.
func (s *slot) release() {
	if s.state.CompareAndSwap(slotHeld, slotReleased) {
		s.timer.Stop()
		s.owner.put(1)
	}
}

// reclaim is the AckWait timer: the broker has redelivered (or is about to redeliver) this
// message, so the slot it held is no longer protecting anything. It is returned and counted.
func (s *slot) reclaim() {
	if s.state.CompareAndSwap(slotHeld, slotReclaimed) {
		s.owner.put(1)
		if s.owner.heldPastAckWait != nil {
			stage := stageBuffer
			if s.inWorker.Load() {
				stage = stageWorker
			}
			s.owner.heldPastAckWait(stage)
		}
	}
}

// handOut decides, as a buffered message is popped, whether it may still be given to a
// worker. It may not once its AckWait has run out — the broker is already redelivering it,
// and handing out this copy is exactly the duplicate a capacity reader exists to prevent —
// and then its slot is reclaimed here (counted at stage buffer) if the timer has not already
// done it. Otherwise the slot is marked as in a worker, so a later reclaim is counted there.
func (s *slot) handOut() bool {
	if s.state.Load() != slotHeld {
		return false
	}
	if !time.Now().Before(s.deadline) {
		s.reclaim()
		return false
	}
	s.inWorker.Store(true)
	// The timer may have fired between the two checks; its copy is being redelivered too.
	return s.state.Load() == slotHeld
}

// ReaderWithCapacity bounds the reader to at most slots messages between fetch and
// disposition. It fetches only free slots; each message carries one slot and its fetch time
// (Message.AckDeadline), and returns the slot when it is acked, when the worker handling it
// returns (Process), or when its AckWait runs out — whichever comes first.
//
// Use it for a consumer whose workers make SLOW EXTERNAL SENDS, where a message queued
// behind them past AckWait is redelivered and sent twice. Size slots to the worker count: a
// capacity reader holds no more messages than it has workers to start, which is the point.
// It changes nothing in the durable's broker-side configuration, so it can be adopted or
// dropped on a running instance.
//
// It panics on slots < 1, because a reader that may hold nothing can read nothing.
func ReaderWithCapacity(slots int) ReaderOption {
	if slots < 1 {
		panic("messaging: ReaderWithCapacity needs at least one slot")
	}
	return func(r *natsReader) { r.slots = slots }
}

// Process runs handle(msg) and returns msg's slot when handle returns, by whatever path —
// including a panic, which it lets continue after the release. It is the worker boundary of
// a capacity reader's consumer: a disposition branch never has to remember to release.
// It is harmless on a message with no slot.
func Process(msg Message, handle func(Message)) {
	defer msg.Release()
	handle(msg)
}

// ackDeadlineKey carries a message's AckDeadline on a context.
type ackDeadlineKey struct{}

// WithAckDeadline returns ctx carrying msg's AckDeadline, for code below the worker that
// sizes a budget but never sees the Message. It returns ctx unchanged when msg has none —
// every message not read by a capacity reader — so a caller can apply it unconditionally.
//
// 🔴 IT SETS NO DEADLINE ON ctx, deliberately. It is a VALUE a budget can be measured from,
// not a cancellation: work that must run after the send regardless (recording that a page
// went out, writing a dead letter) keeps running on the same context, where a real deadline
// would cut it off at exactly the moment its record matters.
func WithAckDeadline(ctx context.Context, msg Message) context.Context {
	d := msg.AckDeadline()
	if d.IsZero() {
		return ctx
	}
	return context.WithValue(ctx, ackDeadlineKey{}, d)
}

// AckDeadlineFrom returns the AckDeadline WithAckDeadline put on ctx, and false when there
// is none.
func AckDeadlineFrom(ctx context.Context) (time.Time, bool) {
	d, ok := ctx.Value(ackDeadlineKey{}).(time.Time)
	if !ok || d.IsZero() {
		return time.Time{}, false
	}
	return d, true
}
