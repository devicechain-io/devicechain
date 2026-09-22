// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"io"

	"github.com/devicechain-io/dc-microservice/core"
)

// ConsumerHandler processes one message RunConsumer has read, and reports whether the
// loop should carry on.
//
// Returning false ENDS the loop, and the tree uses it for one thing: the process is going
// down part-way through handling a message. A handoff to a worker pool abandoned on
// ctx.Done, a persist retry cut short by shutdown, a signal that could not be sent. In
// every one of those the message is deliberately left UNACKED so it redelivers on the next
// start, which is why the handler says "stop" rather than returning an error — nothing has
// gone wrong with the message.
//
// 🔴 A HANDLER THAT DOES ITS OWN WORK AND HAS NOTHING TO SAY RETURNS TRUE, and the
// ceremony is deliberate. The alternative considered was a void handler with the loop
// inferring the stop from ctx.Err() afterwards — every false return in the tree today does
// coincide with a cancelled context, so it would work. It was rejected because the
// coincidence is not a contract: the first handler that wants to stop for some other
// reason would compile, return, and be ignored. That is the fail-open direction.
type ConsumerHandler func(msg Message) bool

// RunConsumer is the read loop that sits behind every durable consumer in the tree.
//
// It reads, paces the errors that are worth retrying, leaves on the ones that are not, and
// hands each message to handle. It is written once here because it was written nineteen
// times across ten services, and the copies had already drifted into two different error
// policies before core.ReadPacer brought them back together.
//
// 🔴 WHY THIS EXISTS EVEN THOUGH THE COPIES CURRENTLY AGREE. ReadPacer has a two-call
// contract — PauseAfterError on the error path AND Succeeded after a good read — and a
// loop that honours only the first tears its own process down after a long uptime, because
// the budget bounds an UNBROKEN run and nothing ever resets it. The repository guard in
// core/test checks only the pause, and says so: the reset is a different defect with a
// different fix. Every loop in the tree pairs them correctly today. This is what stops the
// twentieth from being the one that does not, by leaving no copy in which to get it wrong.
//
// THE THREE WAYS IT RETURNS, all of them ordinary:
//
//   - the context is cancelled, which is shutdown;
//   - the reader reports io.EOF, its terminal signal — a closed connection, a drained
//     subscription, a failed rebind;
//   - handle returns false, which is also shutdown, part-way through a message.
//
// A fourth is not ordinary and does not return normally: a run of non-EOF read errors that
// outlasts the pacer's budget. PauseAfterError reports the process unfit before it reports
// stop, so by the time this returns the shutdown is already under way.
//
// 🔴 WHAT IT DELIBERATELY DOES NOT OWN. It does not ack, it does not decode, it does not
// fan out to workers, and it does not know what a message means. Those differ at every
// adopter and several of the differences are argued in comments at the call site. This
// extracts the LOOP, not the pipeline: a consumer that reads into a channel keeps its own
// select in the handler, where the reader's ack contract stays visible next to the handoff
// that decides it.
//
// The pacer is a parameter rather than something built here because it is built ONCE per
// run of the loop and is single-goroutine by contract; handing it in keeps that ownership
// where the caller can see it, and lets a caller that rebuilds its loop rebuild the pacer
// with it rather than inheriting a stale run of failures.
func RunConsumer(ctx context.Context, reader MessageReader, pacer *core.ReadPacer, handle ConsumerHandler) {
	for {
		// Re-check before every read rather than trusting the reader to notice. The NATS
		// reader does check, and returns io.EOF when it does — but that is its choice, not
		// something MessageReader promises, and a reader that has a batch already buffered
		// is exactly the one that could hand out another message after shutdown began.
		// Two of the nineteen loops this replaces guarded it here for that reason; the
		// other seventeen inherited the reader's behaviour without saying so.
		if ctx.Err() != nil {
			return
		}
		msg, err := reader.ReadMessage(ctx)
		if err != nil {
			// The terminal set is the UNION of the spellings the copies used — some
			// checked io.EOF alone, some io.EOF or a cancelled context, one ranged while
			// ctx.Err() was nil. The union is the safe direction: a cancellation treated
			// as a retryable error would call HandleResponse and then PAUSE, holding the
			// loop open through the pacer's backoff while shutdown waits on it.
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
				return
			}
			// The reader logs it and updates its own health; the pacer decides the rate.
			// Both halves, in this order, at every adopter.
			reader.HandleResponse(err)
			if pacer.PauseAfterError(ctx, err) {
				return
			}
			continue
		}
		// 🔴 BEFORE handle, not after. A handler that returns false leaves the loop, so a
		// reset placed after it would be skipped on the shutdown path — harmless today,
		// but it would make the reset conditional on something that has nothing to do with
		// whether the READ succeeded, which is the only thing it records.
		pacer.Succeeded()
		if !handle(msg) {
			return
		}
	}
}
