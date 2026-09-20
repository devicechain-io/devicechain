// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

// Pacing for a message-read loop whose reads keep failing with something other than
// EOF. They are vars rather than constants so a test can compress them; nothing in
// production writes them.
var (
	// readErrorBackoffBase is the pause after the first failed read of a run.
	readErrorBackoffBase = 200 * time.Millisecond

	// readErrorBackoffMax caps the doubling, so a long outage still leaves the loop
	// retrying often enough to pick up promptly when the broker comes back.
	readErrorBackoffMax = 5 * time.Second

	// readErrorBudget is how long an UNBROKEN run of failures may last before the loop
	// stops retrying and the process declares itself unfit.
	//
	// It has to outlast the failures a consumer genuinely recovers from on its own — a
	// broker failover, a broker restart, a burst of max_ack_pending refusals — which is
	// why it is minutes rather than seconds. RetryInfraConnect's budget
	// (infraConnectAttempts x infraConnectDelay) is the comparable number for the same
	// dependency at startup, and this is deliberately the longer of the two: a service
	// that is already running has messages in flight, so ending it costs more than
	// delaying a bring-up does.
	readErrorBudget = 2 * time.Minute
)

// ReadPacer paces a message-read loop after a non-EOF read error, and ends the loop
// once the errors have stopped looking transient.
//
// 🔴 IT EXISTS BECAUSE "LOG IT AND READ AGAIN" HAS TWO FAILURE MODES, AND THE SECOND ONE
// IS THE REASON THIS IS NOT JUST A SLEEP. A read error that returns instantly and keeps
// returning turns the loop into a spin: it burns a core and writes one log line per
// iteration, flooding the log pipeline at the exact moment an operator needs to read it.
// Pausing fixes that. But a pause alone leaves the OTHER failure mode untouched — an error
// that is never going to clear now spins slowly instead of quickly, and the pod goes on
// reporting ready while consuming nothing. Spinning slower is not making progress.
//
// 🔴 WHICH ERRORS THOSE ACTUALLY ARE IS NARROWER THAN THIS COMMENT ONCE CLAIMED, and the
// wrong version was copied into four other files before anyone checked it against the
// reader. It named "a consumer deleted and un-recreatable" and "a credential revoked".
// Neither reaches a pacer through messaging.natsReader:
//
//   - A DELETED CONSUMER never gets here. ReadMessage recognises it (isConsumerGone) and
//     calls rebindWithBackoff, which by its own doc "never gives up on its own" — it
//     retries inside the read, and the few conditions that do end it (shutdown, a closed
//     or draining connection, an unbound term) surface as io.EOF, which every loop treats
//     as a clean exit. So that case is either healed or quiet; it is never paced.
//   - A REVOKED CREDENTIAL arrives as an async -ERR rather than a Fetch return, so the
//     fetch simply times out and reads as an IDLE stream.
//
// What does reach a pacer is everything natsReader hands back rather than healing: the
// JetStream API errors, the 409 family (a stream at its MaxAckPending or MaxWaiting
// ceiling), a consumer whose leadership keeps moving, a subscription it cannot rebuild.
// Those are worth bounding — a run of them that outlasts the budget is a real outage — but
// do not write the wider claim back in. If a pacer should cover a deleted consumer too,
// that is a change to natsReader's rebind, not to this comment.
//
// So a pacer does both: it spaces the retries out, and it puts a ceiling on how long a
// single unbroken run of them may last. Past that ceiling the loop stops and the process
// says so through FailNow, which exits non-zero — so the failure is REPORTED (a restart
// count, a container-exit alert) rather than being a healthy pod doing nothing. The restart
// is also the only remedy available to several of these errors: it re-dials the broker,
// re-creates the durable consumer, and re-reads the mounted credential.
//
// A ReadPacer is used from ONE goroutine — the loop's own — which is the same constraint
// messaging.MessageReader.ReadMessage already places on its caller. It holds no lock.
type ReadPacer struct {
	// what names the stream in log lines and in the error handed to the fail sink.
	what string

	// fail reports an exhausted retry budget, which in production ends the process.
	//
	// It is a FUNCTION rather than the *Microservice it is derived from, and that is what
	// makes the reporting half of this type testable at all. The production sink ends the
	// process, so a test that called it would take the test binary with it — which is why
	// the branch went untested at every adopter until it was given this shape.
	//
	// It MAY BE NIL, and NewReadPacer leaves it nil when there is no microservice: these
	// processors are assembled by struct literal in their own tests, where there is
	// nothing to fail. A nil sink still stops the loop — the loop's behaviour is the part
	// under test — and says so at error level.
	fail func(error)

	// now and sleep are the clock, replaceable by UseClock. sleep reports false when
	// the wait ended in cancellation rather than elapsing.
	now   func() time.Time
	sleep func(context.Context, time.Duration) bool

	// delay is the next pause; failures and runStart describe the current unbroken run.
	delay    time.Duration
	failures int
	runStart time.Time
}

// NewReadPacer builds a pacer for a read loop, naming the stream it drains for the log.
// ms is where an exhausted retry budget is reported; see the fail field for why nil is
// tolerated.
//
// The sink is resolved HERE rather than at the point of failure, so the nil question is
// answered once, against the concrete *Microservice the caller passed. Storing the
// microservice and deciding later would mean a typed-nil pointer behind an interface
// could pass a nil check and then panic inside the give-up — in the one code path that
// only ever runs when the service is already in trouble.
func NewReadPacer(ms *Microservice, what string) *ReadPacer {
	p := &ReadPacer{what: what, now: time.Now, sleep: sleepUntilCancelled}
	if ms != nil {
		p.fail = ms.FailNow
	}
	return p
}

// reportTo replaces the sink an exhausted budget is reported to, and returns the pacer so
// it can be installed in one expression.
//
// It is a test seam and it is unexported on purpose: no service should be choosing where
// its own give-up goes. It exists because the production sink ENDS THE PROCESS, so the
// only way to assert that a give-up is actually reported — rather than merely logged — is
// to stand somewhere else in its place.
func (p *ReadPacer) reportTo(f func(error)) *ReadPacer {
	p.fail = f
	return p
}

// UseClock replaces the pacer's clock and its sleep, and returns the pacer so it can be
// installed in one expression.
//
// It is a test seam, and it is the only way one can be written: a pacer left on the real
// clock makes a test that measures the retry budget take the retry budget. sleep must
// report false when the wait was cut short by cancellation, since that is how the pacer
// tells a shutdown apart from an elapsed pause.
func (p *ReadPacer) UseClock(now func() time.Time, sleep func(context.Context, time.Duration) bool) *ReadPacer {
	p.now, p.sleep = now, sleep
	return p
}

// Succeeded records a successful read, ending the current run of failures.
//
// 🔴 A LOOP THAT PAUSES BUT NEVER CALLS THIS EVENTUALLY KILLS ITS OWN PROCESS. The budget
// bounds an UNBROKEN run; without the reset, one error an hour accumulates across a day
// into an exhausted budget, and a healthy service is torn down for a fault it recovered
// from twenty-three hours earlier.
func (p *ReadPacer) Succeeded() {
	p.failures = 0
	p.delay = 0
}

// PauseAfterError paces the loop after one non-EOF read error, and reports whether the
// loop should STOP. It returns true in exactly two cases, and a caller does the same thing
// in both — leave the loop:
//
//   - the context was cancelled during the pause, which is shutdown; and
//   - the run of failures has outlasted the budget, in which case the process has already
//     been told to end.
//
// It does not swallow the error. Callers hand the error to the reader's own HandleResponse
// (which logs it) before calling this; the point here is the rate, not the silence.
func (p *ReadPacer) PauseAfterError(ctx context.Context, err error) (stop bool) {
	p.failures++
	if p.failures == 1 {
		p.runStart = p.now()
		p.delay = readErrorBackoffBase
	}
	if elapsed := p.now().Sub(p.runStart); elapsed >= readErrorBudget {
		p.giveUp(err, elapsed)
		return true
	}
	if !p.sleep(ctx, p.delay) {
		return true
	}
	if p.delay *= 2; p.delay > readErrorBackoffMax {
		p.delay = readErrorBackoffMax
	}
	return false
}

// giveUp ends the process because this loop is not going to recover.
func (p *ReadPacer) giveUp(err error, elapsed time.Duration) {
	fatal := fmt.Errorf("core: the %s read loop failed continuously for %s over %d consecutive reads "+
		"and is not recovering; last error: %w", p.what, elapsed.Round(time.Second), p.failures, err)
	if p.fail == nil {
		log.Error().Err(fatal).Msg("A message-read loop exhausted its retry budget with no microservice " +
			"to report it to; the loop is stopping.")
		return
	}
	// 🔴 ON ITS OWN GOROUTINE, AND THAT IS NOT STYLE. FailNow tears the process down, and
	// teardown runs this component's ExecuteStop, which waits on the read goroutine —
	// the one calling this. Inline, that is a self-deadlock: the shutdown waits for a
	// loop that is waiting inside the shutdown. The caller returns true immediately
	// afterwards, which is what lets that wait complete.
	go p.fail(fatal)
}

// sleepUntilCancelled waits for d, reporting false if ctx was cancelled first. A
// non-positive d still honours an already-cancelled context, so a paused loop cannot
// out-live a shutdown by one more read.
func sleepUntilCancelled(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// VirtualClock returns a now/sleep pair whose sleeps are instantaneous and whose clock
// advances by exactly the interval it was asked to wait, for use with ReadPacer.UseClock.
//
// It lives here rather than in each service's tests because every read loop this paces
// needs the same seam to be measurable without spending the backoff, and because a clock
// each caller writes for itself is a clock each caller can get subtly wrong — one that
// does not advance turns a budget test into an endless loop.
//
// The returned pair is used from one goroutine, which is the pacer's own contract.
func VirtualClock() (now func() time.Time, sleep func(context.Context, time.Duration) bool) {
	t := time.Unix(0, 0).UTC()
	return func() time.Time { return t },
		func(ctx context.Context, d time.Duration) bool {
			if ctx.Err() != nil {
				return false
			}
			t = t.Add(d)
			return true
		}
}
