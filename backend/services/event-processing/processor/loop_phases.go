// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// loopPhase is one of the places the live loop's wall time goes. The phases partition the loop's
// time: at every instant exactly one is current, so across a window their seconds add up to the
// window, and each one's rate is its share of the loop's wall time (a loop that is fully busy
// shows its non-wait phases summing to one second per second).
//
// They exist to say WHICH limit the loop is up against before anyone changes it. A loop that is
// behind while mostly in fetch_wait is waiting on the broker, not working; one that is behind and
// mostly in apply is bound by the engine; one spending its time in publish, save or ack is bound
// by what each checkpoint costs.
type loopPhase uint8

const (
	// phaseWait is the loop parked in its select with nothing to do: waiting for the next
	// message, rule update or tick. It is the only phase that is not work.
	phaseWait loopPhase = iota
	// phaseDecode is a message's gap check, tenant parse and protobuf decode, and the same for a
	// message held back by a gap once the gap has been read.
	phaseDecode
	// phasePlan is the fan-out: resolving the attribute and fence views and selecting and
	// evaluating the applicable rules' predicates.
	phasePlan
	// phaseApply is the engine itself: descopes, the watermark advance and the per-rule events,
	// and draining what they emitted.
	phaseApply
	// phasePublish is a checkpoint handing its buffered detections to the broker.
	phasePublish
	// phaseSave is a checkpoint serializing the engine and committing the snapshot.
	phaseSave
	// phaseAck is a checkpoint acknowledging the messages it made durable.
	phaseAck
	// phaseGapFill is reading a range of stream sequences the loop never received. Applying the
	// messages it finds is counted under plan and apply, as it is for any other message.
	phaseGapFill
	// phaseControl is everything else the loop does between messages: rule, roster, attribute
	// and fence updates, tenant purges, and the ticker's housekeeping (idle advance, sampling,
	// rechecks). A checkpoint that one of these triggers is counted under its own phases.
	phaseControl
	// phaseParked is the loop waiting with live messages switched off: held on an uncommitted
	// idle advance or on a sequence gap it could not read. Unlike fetch_wait this is not the
	// broker being slow; it is the loop refusing work until a retry succeeds.
	phaseParked
	// phaseProbe is a round trip to the broker the ticker makes for its own sake: the consumer
	// backlog probe behind the idle-advance gate and the lag gauge. Time here is the broker
	// answering, not the engine working.
	phaseProbe

	numLoopPhases
)

// loopPhaseNames are the values of the phase label, in loopPhase order.
var loopPhaseNames = [numLoopPhases]string{
	"fetch_wait", "decode", "plan", "apply", "publish", "save", "ack", "gapfill", "control", "parked", "probe",
}

// loopPhases is the stopwatch the live loop runs. It is deliberately cheap enough for a loop that
// spends tens of microseconds on a message: moving between phases costs one clock read and an
// addition into a local array, and the Prometheus counters are written only when flushed (every
// checkpoint and every tick), never per message.
//
// Only the goroutine running the loop may use it, which is why every method is a no-op until
// start has been called: replay and the end-of-term flush (which runs after the loop has exited)
// use the same checkpoint code outside the loop, and must not touch the stopwatch. The checkpoint
// the loop itself makes on its way out, whether for shutdown or for the end of the stream, runs
// inside it and is measured. The zero value is unarmed, as is a nil pointer, so a processor built
// without metrics runs unmeasured.
type loopPhases struct {
	live atomic.Bool
	// now is the clock. It is time.Now unless a test has supplied one before start.
	now  func() time.Time
	cur  loopPhase
	mark time.Time
	acc  [numLoopPhases]time.Duration
	out  [numLoopPhases]prometheus.Counter
}

// start arms the stopwatch for one run of the loop. It stays unarmed when there are no metrics.
func (l *loopPhases) start(m *DetectMetrics) {
	if l == nil || m == nil {
		return
	}
	l.out = m.loopSeconds
	l.acc = [numLoopPhases]time.Duration{}
	if l.now == nil {
		l.now = time.Now
	}
	l.cur, l.mark = phaseWait, l.now()
	l.live.Store(true)
}

// enter makes p the current phase, crediting the time since the last change to the phase that was
// current, and returns that phase so a caller that borrows the stopwatch for a stretch (a
// checkpoint) can hand it back.
func (l *loopPhases) enter(p loopPhase) loopPhase {
	if l == nil || !l.live.Load() {
		return p
	}
	now := l.now()
	prev := l.cur
	l.acc[prev] += now.Sub(l.mark)
	l.cur, l.mark = p, now
	return prev
}

// flush adds the accumulated seconds to the counters. The time still running in the current
// phase is credited first, so a loop parked in fetch_wait is seen to be waiting at the next tick
// rather than only once it wakes.
func (l *loopPhases) flush() {
	if l == nil || !l.live.Load() {
		return
	}
	l.enter(l.cur)
	for i := range l.acc {
		if l.acc[i] > 0 {
			l.out[i].Add(l.acc[i].Seconds())
			l.acc[i] = 0
		}
	}
}

// stop flushes and disarms the stopwatch when the loop exits.
func (l *loopPhases) stop() {
	if l == nil || !l.live.Load() {
		return
	}
	l.flush()
	l.live.Store(false)
}
