// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// step is one scripted return from scriptedReader.ReadMessage.
type step struct {
	msg Message
	err error
}

// scriptedReader hands out a fixed sequence of read results and records what the loop did
// with them. Past the end of the script it reports io.EOF, so a test that miscounts ends
// rather than hanging.
//
// It deliberately IGNORES ctx: that is what lets a test pin the loop's own cancellation
// check rather than the production reader's. The real reader checks ctx itself and returns
// io.EOF, which would make the loop's check untestable through it.
type scriptedReader struct {
	steps   []step
	reads   int
	handled []error // the errors passed to HandleResponse, in order
	onRead  func(n int)
}

func (r *scriptedReader) ReadMessage(context.Context) (Message, error) {
	if r.onRead != nil {
		r.onRead(r.reads)
	}
	if r.reads >= len(r.steps) {
		r.reads++
		return Message{}, io.EOF
	}
	s := r.steps[r.reads]
	r.reads++
	return s.msg, s.err
}

func (r *scriptedReader) HandleResponse(err error) { r.handled = append(r.handled, err) }

// countingHandler accepts every message and records how many it saw.
func countingHandler(n *int) ConsumerHandler {
	return func(Message) bool { *n++; return true }
}

// testPacer is a pacer on a clock the test drives: it never really sleeps, and time only
// moves when the test moves it.
func testPacer(clock *time.Time, slept *int) *core.ReadPacer {
	return core.NewReadPacer(nil, "test").UseClock(
		func() time.Time { return *clock },
		func(context.Context, time.Duration) bool { *slept++; return true },
	)
}

func TestTheLoopEndsWhenTheReaderReportsEndOfStream(t *testing.T) {
	r := &scriptedReader{steps: []step{{msg: Message{Subject: "a"}}, {msg: Message{Subject: "b"}}}}
	handled := 0
	clock, slept := time.Now(), 0

	RunConsumer(context.Background(), r, testPacer(&clock, &slept), countingHandler(&handled))

	if handled != 2 {
		t.Fatalf("the loop handled %d messages, want the 2 in the script before its EOF", handled)
	}
}

func TestTheLoopEndsWhenTheHandlerSaysStop(t *testing.T) {
	r := &scriptedReader{steps: []step{
		{msg: Message{Subject: "a"}}, {msg: Message{Subject: "b"}}, {msg: Message{Subject: "c"}},
	}}
	handled := 0
	clock, slept := time.Now(), 0

	RunConsumer(context.Background(), r, testPacer(&clock, &slept), func(Message) bool {
		handled++
		return handled < 2 // stop while handling the second
	})

	if handled != 2 {
		t.Fatalf("the handler ran %d times, want 2: returning false must end the loop, not be ignored", handled)
	}
	if r.reads != 2 {
		t.Fatalf("the reader was read %d times, want 2: the loop read again after the handler said stop", r.reads)
	}
}

func TestATransientReadErrorIsReportedToTheReaderAndPacedBeforeTheNextRead(t *testing.T) {
	boom := errors.New("fetch refused")
	r := &scriptedReader{steps: []step{{err: boom}, {msg: Message{Subject: "a"}}}}
	handled := 0
	clock, slept := time.Now(), 0

	RunConsumer(context.Background(), r, testPacer(&clock, &slept), countingHandler(&handled))

	if len(r.handled) != 1 || !errors.Is(r.handled[0], boom) {
		t.Fatalf("the reader was handed %v, want exactly the one read error: the loop must report it "+
			"to the reader as well as pacing it", r.handled)
	}
	if slept != 1 {
		t.Fatalf("the pacer paused %d times, want 1: an error the loop retries without pausing is the "+
			"hot spin the pacer exists to prevent", slept)
	}
	if handled != 1 {
		t.Fatalf("the loop handled %d messages, want 1: a paced error must be RETRIED, not terminal", handled)
	}
}

// 🔴 THIS IS THE TEST THE REPOSITORY GUARD CANNOT WRITE, and the reason RunConsumer exists
// rather than a checklist. The guard checks that a loop PAUSES; nothing checks that it
// RESETS. A loop that pauses but never calls Succeeded accumulates one blip an hour across
// a long uptime into an exhausted budget, and ends a healthy process for faults it
// recovered from hours earlier.
//
// The clock here moves far past the pacer's budget, but only ever BETWEEN runs of failures.
// With the reset, every error starts a fresh run and none of them outlasts anything. Drop
// the Succeeded call from the loop and the single run spans the whole scripted sequence,
// the budget goes, and the loop gives up part-way through.
func TestAGoodReadEndsTheRunOfFailuresSoASlowTrickleNeverExhaustsTheBudget(t *testing.T) {
	const rounds = 30
	var steps []step
	for i := 0; i < rounds; i++ {
		steps = append(steps, step{err: errors.New("blip")}, step{msg: Message{Subject: "ok"}})
	}
	clock, slept := time.Now(), 0
	r := &scriptedReader{steps: steps}
	// One minute of wall clock per read, so the sequence spans half an hour — far longer
	// than any run of failures is allowed to last.
	r.onRead = func(int) { clock = clock.Add(time.Minute) }
	handled := 0

	RunConsumer(context.Background(), r, testPacer(&clock, &slept), countingHandler(&handled))

	if handled != rounds {
		t.Fatalf("the loop handled %d of %d good reads before stopping. It gave up inside its retry "+
			"budget on a stream that succeeded between every failure, which means the run of failures "+
			"is never being reset", handled, rounds)
	}
}

func TestTheLoopDoesNotReadAgainOnceTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// A reader with an endless supply of messages that ignores cancellation entirely, which
	// is what makes this about the LOOP's check and not the reader's.
	r := &scriptedReader{}
	for i := 0; i < 50; i++ {
		r.steps = append(r.steps, step{msg: Message{Subject: "a"}})
	}
	handled := 0
	clock, slept := time.Now(), 0

	RunConsumer(ctx, r, testPacer(&clock, &slept), func(Message) bool {
		handled++
		if handled == 3 {
			cancel()
		}
		return true
	})

	if handled != 3 {
		t.Fatalf("the loop handled %d messages, want 3: it kept reading a buffered stream after its "+
			"context was cancelled, which is work done on behalf of a shutting-down process", handled)
	}
}

// 🔴 THE CANCELLATION HAS TO ARRIVE DURING THE READ, and an earlier version of this test
// did not arrange that. It cancelled up front, which meant the loop's PRE-READ check
// returned before the error path was ever reached — so the two assertions below, both of
// which are phrased as "the bad thing did not happen", were unreachable and passed on a
// loop that would have paced a cancellation. A mutant that dropped ctx from the terminal
// set survived it. The read itself is what blocks across a shutdown, so the read is where
// the cancellation has to land.
func TestACancellationArrivingAsAReadErrorIsNotPacedAsAFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// A reader that is cancelled while blocked in the read and surfaces that as an
	// ordinary-looking error rather than EOF, which is the case the terminal set exists for.
	r := &scriptedReader{steps: []step{{err: errors.New("closed while shutting down")}}}
	r.onRead = func(int) { cancel() }
	handled := 0
	clock, slept := time.Now(), 0

	RunConsumer(ctx, r, testPacer(&clock, &slept), countingHandler(&handled))

	if slept != 0 {
		t.Fatalf("the pacer paused %d times during shutdown. A cancellation treated as a retryable "+
			"error holds the loop open through the backoff while shutdown waits on it", slept)
	}
	if len(r.handled) != 0 {
		t.Fatalf("the reader was handed %v during shutdown; a cancellation is not a read failure to "+
			"report", r.handled)
	}
}

// The pacer stops a loop for two reasons — the retry budget is spent, or the pause itself
// was cancelled — and both arrive as the same `true`. Nothing else in this file pins that
// the loop obeys it: the pacing tests all watch a pacer that says carry on.
func TestTheLoopEndsWhenThePacerSaysStop(t *testing.T) {
	var steps []step
	for i := 0; i < 20; i++ {
		steps = append(steps, step{err: errors.New("not clearing")})
	}
	r := &scriptedReader{steps: steps}
	clock := time.Now()
	// A pause that reports it was cut short, which is how the pacer signals shutdown.
	pacer := core.NewReadPacer(nil, "test").UseClock(
		func() time.Time { return clock },
		func(context.Context, time.Duration) bool { return false },
	)
	handled := 0

	RunConsumer(context.Background(), r, pacer, countingHandler(&handled))

	if r.reads != 1 {
		t.Fatalf("the reader was read %d times, want 1: the pacer reported stop after the first "+
			"error and the loop went back for more. Past the retry budget that is a loop that has "+
			"already declared its own process unfit and is still consuming", r.reads)
	}
}
