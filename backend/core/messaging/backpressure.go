// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// Backpressure on the ingest path: a stream that must not discard a message its reader has
// not read REFUSES new ones instead, while that reader is far behind.
//
// Every stream discards its oldest message when full (DiscardOld). On the two streams whose
// unread loss is device data (streams.Stream.BackpressureReaders), that eviction took events a
// device had already been told were accepted. The broker cannot make that refusal itself:
// what these streams hold at their ceiling is mostly a week of history every reader has
// ALREADY read, so discard-new would refuse on read history and stop ingest for good, and the
// retentions that would make "full" mean "full of unread" break replay (see the field's
// comment). So the platform's own writers make the refusal, from the quantity that decides
// whether anything is lost: each declared reader's UNREAD backlog against the stream's
// ceiling, and, on a full stream, how long the history it has already read will keep
// DiscardOld away from the messages it has not (see runwayNext). The broker keeps discarding
// old as a counted backstop.
//
// The gate is per stream and per process. Every service that writes to a gated stream, or
// forwards into one, keeps its own copy, sampled from the broker, so none of them depends on
// another service's view or version.

// ErrStreamBackpressure is what every backpressure refusal matches (errors.Is). It is
// retryable: the stream is not broken, a reader is behind, and the refusal lifts once it
// catches up. A transport advises BackpressureRetryAfter.
var ErrStreamBackpressure = errors.New("messaging: stream is applying backpressure")

// BackpressureError says which stream refused and why.
type BackpressureError struct {
	// Stream is the full stream name.
	Stream string
	// Durable is the reader whose backlog closed the gate, "" when Stale.
	Durable string
	// Ratio is the highest gating reader's unread backlog over the stream's capacity at the
	// last sample: Durable's, when the ratio rule closed the gate.
	Ratio float64
	// Stale is set when the backlog could not be measured recently enough to trust: the
	// gate then reads as closed (fail closed), whatever it last measured.
	Stale bool
	// Runway is set when the runway rule closed the gate rather than the ratio: History
	// messages Durable has already read were left ahead of the ones it has not, and the full
	// stream was discarding them at Rate a second (see runwayNext).
	Runway  bool
	History uint64
	Rate    float64
}

func (e *BackpressureError) Error() string {
	if e.Stale {
		return fmt.Sprintf("messaging: stream %s is applying backpressure: its readers' backlog has not "+
			"been measured in the last %s", e.Stream, backpressureStaleAfter)
	}
	if e.Runway {
		return fmt.Sprintf("messaging: stream %s is applying backpressure: %d messages consumer %s has "+
			"already read are left ahead of the ones it has not, and the stream is discarding read messages "+
			"at %.0f a second", e.Stream, e.History, e.Durable, e.Rate)
	}
	return fmt.Sprintf("messaging: stream %s is applying backpressure: consumer %s has not read %.0f%% of "+
		"what the stream can hold", e.Stream, e.Durable, e.Ratio*100)
}

// Is makes a *BackpressureError match ErrStreamBackpressure.
func (e *BackpressureError) Is(target error) bool { return target == ErrStreamBackpressure }

const (
	// backpressureCloseRatio is the unread fraction of the ceiling at which the gate closes.
	// The 10% left above it is the ratio rule's margin before DiscardOld would evict an
	// unread message: what is in flight when the gate closes, what the sample interval lets
	// through, the state transitions that are admitted while it is closed
	// (Message.BypassBackpressure), and the messages the broker has acknowledged but not yet
	// signalled to its consumers all land in it. That last one is why a measurement can read
	// low: a PubAck is sent before the stream signals its consumers, and
	// ConsumerInfo.NumPending is counted from that signal, so a sample reads low by whatever
	// is still queued for it. The runway rule keeps a margin of its own, in time:
	// backpressureRunwayClose of the rate the stream has recently been discarding at.
	backpressureCloseRatio = 0.90
	// backpressureOpenRatio is the fraction below which a closed gate opens again. The gap
	// to the close ratio keeps a reader hovering at the threshold from flapping the gate
	// open and shut on every sample.
	backpressureOpenRatio = 0.80
	// backpressureSampleEvery is how often every registered gate is re-measured.
	backpressureSampleEvery = 5 * time.Second
	// backpressureStaleAfter is how old the last successful measurement may be before the
	// gate reads as closed. A gate that cannot see the backlog cannot say it is safe to add
	// to it.
	backpressureStaleAfter = 30 * time.Second
	// backpressureSampleTimeout bounds one measurement.
	backpressureSampleTimeout = 5 * time.Second

	// backpressureRunwayClose: on a full stream, an open gate closes when the messages a
	// gating durable has already read, still held ahead of its unread ones, would all be
	// discarded within this long at the rate the stream has recently been discarding them.
	// It is the stale bound on purpose: the gate already trusts a measurement for that long,
	// so the history must outlast it.
	backpressureRunwayClose = backpressureStaleAfter
	// backpressureRunwayOpen: a gate the runway rule closed opens again once that history
	// would last this long (hysteresis, as backpressureOpenRatio is for the ratio), or once
	// nothing is unread.
	backpressureRunwayOpen = 2 * backpressureRunwayClose
	// backpressureDrainMemory is how fast a remembered discard rate fades while the gate is
	// open (its e-folding time). While the gate refuses, or cannot measure, it does not fade:
	// a refusing stream discards nothing, and a rate that faded then would reopen the gate on
	// a reader that has not moved.
	backpressureDrainMemory = 60 * time.Second
	// backpressureCeilingShare sets how close to a ceiling a stream must be for the runway
	// rule to apply: within 1/backpressureCeilingShare of it (5%), or within one message of
	// the largest size the stream accepts. Only a stream at its ceiling discards to make
	// room. History that shrinks below it was purged or expired, which frees space rather
	// than consuming it, and refusing would protect nothing.
	backpressureCeilingShare = 20
	// backpressureKickShare: a process measures a gate again as soon as it has written
	// 1/backpressureKickShare of the stream's byte or message ceiling to it since the last
	// measurement started (1 MiB, or about 4,900 messages, at the default ceilings), not only
	// every backpressureSampleEvery. Both rules' margins can be spent faster than a 5 s tick,
	// and volume is what spends them.
	backpressureKickShare = 1024
	// backpressureKickMinGap: a volume-triggered measurement of a gate starts at least this
	// long after the loop's previous one, which bounds the extra StreamInfo and ConsumerInfo
	// calls to 10 a second per gate per process however fast it writes.
	backpressureKickMinGap = 100 * time.Millisecond

	// BackpressureRetryAfter is what a transport advises a refused client to wait: two
	// sample intervals, so a retry lands after the gate has been measured again.
	BackpressureRetryAfter = 2 * backpressureSampleEvery
)

// These are constants, not configuration, on purpose: a knob here is a way to switch a
// fail-closed guard off, and nothing about an install changes what "about to discard an
// unread event" means.

// unreadRatio is one durable's unread backlog (NumPending + NumAckPending) over the stream's
// capacity, in whichever of messages or bytes binds first. 0 for an empty stream.
//
// 🔴 THE BYTE FORM ASSUMES UNREAD MESSAGES ARE THE SIZE OF THE STREAM'S AVERAGE. The broker
// reports the stream's total bytes, not the unread messages' bytes (it cannot report the
// bytes of a range of sequences), so on a full stream this is the unread share of the message
// COUNT: a burst of large unread messages over small history can reach the ceiling while it
// reads about half, however often it is measured. The runway rule (historyAhead, nextDrain,
// runwayNext) is what catches that: it counts the read messages DiscardOld removes first and
// how fast it is removing them, which does not depend on their size.
func unreadRatio(st nats.StreamState, cfg nats.StreamConfig, ci *nats.ConsumerInfo) float64 {
	unread := ci.NumPending + uint64(max(0, ci.NumAckPending))
	r := 0.0
	if cfg.MaxMsgs > 0 {
		r = float64(unread) / float64(cfg.MaxMsgs)
	}
	if cfg.MaxBytes > 0 && st.Msgs > 0 {
		avg := float64(st.Bytes) / float64(st.Msgs)
		r = max(r, float64(unread)*avg/float64(cfg.MaxBytes))
	}
	return r
}

// gateNext is the hysteresis: a gate closes at the close ratio and opens again only below
// the open ratio.
func gateNext(closed bool, ratio float64) bool {
	if closed {
		return ratio >= backpressureOpenRatio
	}
	return ratio >= backpressureCloseRatio
}

// historyAhead is a lower bound on the messages the stream holds ahead of ci's first
// unacknowledged one: what DiscardOld removes before it reaches a message this durable has
// not finished with.
//
// Every message above the ack floor is counted as unread, though some may have been acked
// out of order: at most LastSeq-AckFloor of them are present, so Msgs-(LastSeq-AckFloor)
// never over-counts. It deliberately uses neither NumPending nor NumAckPending. NumPending is
// counted from a signal the broker sends AFTER the PubAck (see backpressureCloseRatio), so it
// reads low straight after a burst, and a low count here would invent history: the unsafe
// direction. A stale AckFloor only lowers the bound. A durable whose filter is narrower than
// the stream, or messages deleted from the middle, make it under-count, which is safe. It is
// 0 once the durable's unacknowledged messages are themselves being discarded.
//
// The caller reads ci BEFORE st, so arrivals in between cancel out of Msgs-LastSeq and
// discards in between are counted.
func historyAhead(st nats.StreamState, ci *nats.ConsumerInfo) uint64 {
	floor := ci.AckFloor.Stream
	if floor >= st.LastSeq {
		return st.Msgs
	}
	above := st.LastSeq - floor
	if above >= st.Msgs {
		return 0
	}
	return st.Msgs - above
}

// hasUnread reports whether anything is above ci's ack floor: a message delivered and not
// acknowledged (one waiting for redelivery included), or not delivered yet. It is the same
// sequence arithmetic as historyAhead, so the two cannot disagree about which messages are
// read.
func hasUnread(st nats.StreamState, ci *nats.ConsumerInfo) bool {
	return st.LastSeq > ci.AckFloor.Stream
}

// atCeiling reports whether the stream is close enough to a ceiling that arrivals make
// DiscardOld remove messages (see backpressureCeilingShare). An unlimited dimension is never
// at its ceiling.
func atCeiling(st nats.StreamState, cfg nats.StreamConfig) bool {
	if cfg.MaxMsgs > 0 && st.Msgs+uint64(cfg.MaxMsgs/backpressureCeilingShare) >= uint64(cfg.MaxMsgs) {
		return true
	}
	if cfg.MaxBytes > 0 {
		margin := max(cfg.MaxBytes/backpressureCeilingShare, int64(cfg.MaxMsgSize))
		if st.Bytes+uint64(margin) >= uint64(cfg.MaxBytes) {
			return true
		}
	}
	return false
}

// drain is one gating durable's read history, as the runway rule follows it between samples.
type drain struct {
	// at is when the sample that measured it started; zero before the first.
	at       time.Time
	history  uint64
	firstSeq uint64
	full     bool
	// rate is how fast the stream has been discarding that history, in messages a second,
	// never negative: a decayed maximum of what each interval measured.
	rate float64
	// closed is this durable's runway hysteresis state.
	closed bool
}

// nextDrain folds a new measurement of one durable into d. refusing is whether the gate was
// refusing (closed by either rule, or stale) when the sample started.
//
//   - nothing unread: the rate is forgotten. Nothing is at risk, and a gate that reopens
//     because its reader caught up must not close again on the rate it closed on.
//   - no earlier measurement: a baseline, rate 0.
//   - at not after d.at (a concurrent sample, or a test's clock that did not move): d.
//   - otherwise the remembered rate fades (unless refusing), and when the stream was at its
//     ceiling at BOTH ends of the interval, what the interval discarded joins it:
//     min(history lost, FirstSeq gained)/dt. History lost bounds it to messages this
//     durable had read; FirstSeq gained bounds it to messages removed from the FRONT, which
//     is where DiscardOld removes them. Deleting a tenant purges its messages from the
//     middle of the stream: the history falls in one step while FirstSeq barely moves, and
//     the space is freed rather than used, so it must not read as a discard rate. An
//     interval that starts or ends below the ceiling (a purge, an expiry, the first fill)
//     adds nothing. 🔴 One purge is indistinguishable from discarding: a tenant whose
//     messages are the OLDEST a full stream holds, and few enough to leave it at its ceiling.
//     FirstSeq then moves as far as the history falls, the interval reads as a high rate, and
//     the gate can close until the reader catches up. That refuses for a while; it loses
//     nothing.
func nextDrain(d drain, at time.Time, history, firstSeq uint64, full, unread, refusing bool) drain {
	next := drain{at: at, history: history, firstSeq: firstSeq, full: full}
	switch {
	case !unread:
		return next
	case d.at.IsZero():
		return next
	case !at.After(d.at):
		return d
	}
	dt := at.Sub(d.at).Seconds()
	next.closed = d.closed
	next.rate = d.rate
	if !refusing {
		next.rate *= math.Exp(-dt / backpressureDrainMemory.Seconds())
	}
	if full && d.full && history < d.history && firstSeq > d.firstSeq {
		discarded := min(d.history-history, firstSeq-d.firstSeq)
		next.rate = max(next.rate, float64(discarded)/dt)
	}
	return next
}

// runwayNext is the runway rule's hysteresis for one durable: true means "closed". It applies
// only while the durable has something unread, the stream is at its ceiling, and read history
// is being discarded at all. An open gate then closes when that history would last less than
// backpressureRunwayClose at the rate; a closed one opens once it would last
// backpressureRunwayOpen.
func runwayNext(closed bool, history uint64, rate float64, full, unread bool) bool {
	if !unread || !full || rate <= 0 {
		return false
	}
	if closed {
		return float64(history) < rate*backpressureRunwayOpen.Seconds()
	}
	return float64(history) < rate*backpressureRunwayClose.Seconds()
}

// runwaySeconds is what jetstream_backpressure_history_runway_seconds exports: how long the
// history would last at the rate; +Inf while the rule does not apply.
func runwaySeconds(history uint64, rate float64, full, unread bool) float64 {
	if !unread || !full || rate <= 0 {
		return math.Inf(1)
	}
	return float64(history) / rate
}

// gateState is one gated stream's last measurement. Everything is guarded by
// backpressureGates.mu, except the publish-volume counters, which are atomics a writer
// touches on every publish without taking the lock.
type gateState struct {
	suffix string
	stream string
	// closed is the ratio rule's hysteresis state, and only a successful sample moves it.
	// Staleness is NOT written into it: it is applied when the gate is consulted, so a gate
	// that was open before a measurement outage opens straight away when measurement resumes
	// below the close ratio, rather than waiting for the open ratio as if it had really
	// closed.
	closed bool
	// worst and ratio are the durable with the highest unread ratio and that ratio.
	worst string
	ratio float64
	// runwayClosed is set while the runway rule holds the gate closed for some durable. It is
	// kept apart from closed so that each rule's hysteresis is its own: a gate the ratio
	// closed reopens below 80% whatever the runway, and one the runway closed reopens at
	// backpressureRunwayOpen whatever the ratio. runwayDurable, runwayHistory and runwayRate
	// are the durable that holds it closed and its numbers.
	runwayClosed  bool
	runwayDurable string
	runwayHistory uint64
	runwayRate    float64
	// drains follows each gating durable's read history for the runway rule.
	drains map[string]drain
	// sampledAt is the last SUCCESSFUL sample; zero until the first.
	sampledAt time.Time
	// started numbers the samples begun; applied is the number of the last one whose result
	// was applied. A sample that finishes after a later-started one was applied is discarded
	// whole: an older broker reading must not overwrite a newer verdict, and
	// volume-triggered samples make overlapping ones routine.
	started, applied uint64
	// failing edge-triggers the failed-sample warning.
	failing bool
	// measured is the set of durables whose series this gate last exported.
	measured []string

	// pubBytes and pubMsgs are what this process has published to the stream since the last
	// measurement started; kickBytes and kickMsgs are the volumes at which it asks for the
	// next one (0 until a sample succeeds, so that any publish asks). kickQueued is set while
	// a request waits in kick, which is nil when volume-triggered measurement is off.
	pubBytes, pubMsgs   atomic.Int64
	kickBytes, kickMsgs atomic.Int64
	kickQueued          atomic.Bool
	kick                chan<- string
}

// refusing reports whether either rule holds the gate closed, staleness aside.
func (gs *gateState) refusing() bool { return gs.closed || gs.runwayClosed }

// notePublished counts one message of n bytes this process wrote to the gate's stream, and
// asks the sampling loop to measure the gate once the volume since the last measurement
// started reaches a threshold. A nil gate (a stream that applies no backpressure) counts
// nothing. Transitions admitted past a closed gate count too: they fill the same margin.
func (gs *gateState) notePublished(n int) {
	if gs == nil || gs.kick == nil {
		return
	}
	b := gs.pubBytes.Add(int64(n))
	m := gs.pubMsgs.Add(1)
	if b < gs.kickBytes.Load() && m < gs.kickMsgs.Load() {
		return
	}
	if gs.kickQueued.CompareAndSwap(false, true) {
		select {
		case gs.kick <- gs.suffix:
		default:
			gs.kickQueued.Store(false)
		}
	}
}

// kickDue reports whether the volume since the last measurement started has reached a
// threshold.
func (gs *gateState) kickDue() bool {
	return gs.pubBytes.Load() >= gs.kickBytes.Load() || gs.pubMsgs.Load() >= gs.kickMsgs.Load()
}

// kickThreshold is the volume in one dimension at which a gate asks to be measured again:
// 1/share of the ceiling and at least 1; never, for an unlimited dimension.
func kickThreshold(ceiling, share int64) int64 {
	if ceiling <= 0 {
		return math.MaxInt64
	}
	return max(1, ceiling/share)
}

// backpressureGates is one manager's set of gates, keyed by suffix.
type backpressureGates struct {
	nmgr *NatsManager
	mu   sync.Mutex
	// now is the clock staleness is judged by; a seam for the staleness test.
	now func() time.Time
	// enabledAreas reports the functional areas the instance deploys; see gatingDurables.
	enabledAreas func() ([]string, error)
	// consumerInfo reads one durable's ConsumerInfo from the broker; a seam so a test can fail
	// that call alone while StreamInfo still answers. It is set before the sampling loop
	// starts and never written afterwards.
	consumerInfo func(ctx context.Context, stream, durable string) (*nats.ConsumerInfo, error)
	gates        map[string]*gateState
	// tick is the sampling loop's period, backpressureSampleEvery; and kickShare is
	// backpressureKickShare, 0 to switch volume-triggered measurement off. Both are seams for
	// tests, written before the first registration and never afterwards.
	tick      time.Duration
	kickShare int64
	// kick carries the suffix of a gate whose publish volume asks for a measurement.
	kick chan string
	// loop starts the sampling loop, once, at the first registration.
	loop sync.Once
	// loopDone is closed when the sampling loop returns.
	loopDone chan struct{}
}

// backpressure returns this manager's gates, creating them on first use so that a manager
// assembled as a struct literal (as tests outside this package do) gates like one built by
// NewNatsManager.
func (nmgr *NatsManager) backpressure() *backpressureGates {
	nmgr.bpOnce.Do(func() {
		nmgr.bp = &backpressureGates{
			nmgr:         nmgr,
			now:          time.Now,
			enabledAreas: core.EnabledFunctionalAreas,
			consumerInfo: func(ctx context.Context, stream, durable string) (*nats.ConsumerInfo, error) {
				return nmgr.js.ConsumerInfo(stream, durable, nats.Context(ctx))
			},
			gates:     map[string]*gateState{},
			tick:      backpressureSampleEvery,
			kickShare: backpressureKickShare,
			// At most one request per gate waits at a time (gateState.kickQueued), and a
			// process gates at most the streams that apply backpressure.
			kick:     make(chan string, 8),
			loopDone: make(chan struct{}),
		}
	})
	return nmgr.bp
}

// registerBackpressure starts gating suffix's stream in this process and takes one
// synchronous sample, so a writer built on a healthy broker is not born refusing. It is
// idempotent: the connection callback that builds writers and readers can run more than
// once, and a second registration changes nothing.
//
// A failed first sample does NOT fail the caller: it leaves the gate unmeasured, which reads
// as closed until a sample succeeds (fail closed). Failing NewWriter instead would crash-loop
// a service's start on one slow JetStream API answer during a rolling restart.
//
// It returns the gate, for a writer to count its publishes against (notePublished); nil for
// a stream that applies no backpressure.
func (nmgr *NatsManager) registerBackpressure(suffix string) *gateState {
	if !streams.AppliesBackpressure(suffix) {
		return nil
	}
	g := nmgr.backpressure()
	g.mu.Lock()
	gs, exists := g.gates[suffix]
	if !exists {
		gs = &gateState{suffix: suffix, stream: StreamName(nmgr.Microservice.InstanceId, suffix),
			drains: map[string]drain{}}
		if g.kickShare > 0 {
			gs.kick = g.kick
		}
		g.gates[suffix] = gs
	}
	g.mu.Unlock()
	if exists {
		return gs
	}
	ctx, cancel := context.WithTimeout(context.Background(), backpressureSampleTimeout)
	defer cancel()
	nmgr.sampleBackpressure(ctx, suffix)
	g.loop.Do(func() { go nmgr.runBackpressure() })
	return gs
}

// runBackpressure measures every registered gate every backpressureSampleEvery, and a gate
// whose publish volume asks for it (notePublished) as soon as backpressureKickMinGap has
// passed since the loop last measured it, for as long as the manager's connection is open.
//
// 🔑 IT FOLLOWS THE CONNECTION, NOT THE LIFECYCLE, deliberately. The gates are consulted by
// writers and readers, which belong to the connection (see NewNatsManager), and a stop's drain
// stage still publishes on it after the metrics sampler has been cancelled: a gate whose loop
// ended with that sampler would go stale thirty seconds into a long drain and refuse messages
// whose source was already acknowledged. Tying it to the connection also means a manager a test
// assembles by hand, which never runs ExecuteStart, is measured like a real one. The loop ends
// at the first tick or measurement request after the connection closes, which a stop always
// reaches.
//
// A request inside the gap WAITS out the rest of it (at most backpressureKickMinGap, holding
// up the other gate by as much) rather than being dropped: a dropped request would leave the
// last burst written to a stream unmeasured until the next tick.
//
// A tick that cannot measure leaves a gate's last measurement in place, and the gate reads as
// refusing once no sample has succeeded for backpressureStaleAfter.
func (nmgr *NatsManager) runBackpressure() {
	g := nmgr.backpressure()
	defer close(g.loopDone)
	ticker := time.NewTicker(g.tick)
	defer ticker.Stop()
	last := map[string]time.Time{}
	measure := func(s string) {
		last[s] = time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), backpressureSampleTimeout)
		nmgr.sampleBackpressure(ctx, s)
		cancel()
	}
	for {
		var kicked string
		select {
		case <-ticker.C:
		case kicked = <-g.kick:
		}
		if nmgr.nc == nil || nmgr.nc.IsClosed() {
			return
		}
		if kicked == "" {
			g.mu.Lock()
			suffixes := make([]string, 0, len(g.gates))
			for s := range g.gates {
				suffixes = append(suffixes, s)
			}
			g.mu.Unlock()
			for _, s := range suffixes {
				measure(s)
			}
			continue
		}
		g.mu.Lock()
		gs := g.gates[kicked]
		g.mu.Unlock()
		if wait := backpressureKickMinGap - time.Since(last[kicked]); wait > 0 {
			time.Sleep(wait)
		}
		gs.kickQueued.Store(false)
		// A measurement since the request (a tick's, or an earlier request's) may already
		// have counted the volume that made it.
		if gs.kickDue() {
			measure(kicked)
		}
	}
}

// Backpressure reports whether suffix's stream is refusing new messages: nil when it is
// not (or never refuses), else a *BackpressureError, which matches ErrStreamBackpressure.
// A gated stream this process has not registered, or has not measured within
// backpressureStaleAfter, reads as refusing.
//
// It never waits on the broker: it reads the last measurement runBackpressure made.
func (nmgr *NatsManager) Backpressure(suffix string) error {
	if !streams.AppliesBackpressure(suffix) {
		return nil
	}
	g := nmgr.backpressure()
	g.mu.Lock()
	defer g.mu.Unlock()
	gs, ok := g.gates[suffix]
	if !ok {
		return &BackpressureError{Stream: StreamName(nmgr.Microservice.InstanceId, suffix), Stale: true}
	}
	switch {
	case gs.sampledAt.IsZero() || g.now().Sub(gs.sampledAt) > backpressureStaleAfter:
		return &BackpressureError{Stream: gs.stream, Stale: true}
	case gs.closed:
		return &BackpressureError{Stream: gs.stream, Durable: gs.worst, Ratio: gs.ratio}
	case gs.runwayClosed:
		return &BackpressureError{Stream: gs.stream, Durable: gs.runwayDurable, Ratio: gs.ratio,
			Runway: true, History: gs.runwayHistory, Rate: gs.runwayRate}
	}
	return nil
}

// sampleBackpressure measures one gated stream: the ConsumerInfo of each durable that gates
// it, then its StreamInfo. It updates the per-durable series, applies both rules' hysteresis
// and logs a transition (Warn on close, Info on open). Any failure leaves the last successful
// measurement in place, so the gate goes stale — and reads as closed — once failures have
// lasted backpressureStaleAfter.
//
// A durable that does not exist yet (its service has not started on this instance) has
// nothing unread and gates nothing. Any other ConsumerInfo error fails the whole sample:
// the gate's answer is the MAXIMUM over its durables, and a maximum over the ones that
// happened to answer can say "open" while the one that did not is full.
//
// The publish volume is counted from the START of a sample, so nothing written while it is in
// flight is left out of the next one.
func (nmgr *NatsManager) sampleBackpressure(ctx context.Context, suffix string) {
	g := nmgr.backpressure()
	g.mu.Lock()
	gs, ok := g.gates[suffix]
	if !ok {
		g.mu.Unlock()
		return
	}
	gs.started++
	seq := gs.started
	at := g.now()
	g.mu.Unlock()
	gs.pubBytes.Store(0)
	gs.pubMsgs.Store(0)
	m, err := nmgr.measureBackpressure(ctx, gs.stream, suffix)

	g.mu.Lock()
	defer g.mu.Unlock()
	if seq < gs.applied {
		return
	}
	gs.applied = seq
	if err != nil {
		// The series are withdrawn, not left at their last value: a gauge frozen at an old
		// reading is exported as a current one (see consumerPending on streamMetrics). The
		// gate itself keeps its last measurement, and each durable's drain, until it goes
		// stale; a measurement that resumes folds over the outage.
		for _, d := range gs.measured {
			nmgr.metrics.forgetBackpressure(gs.stream, d)
		}
		gs.measured = nil
		if !gs.failing {
			gs.failing = true
			sampleFailureLog(ctx).Err(err).Str("stream", gs.stream).
				Msg("Could not measure a stream's unread backlog; if this lasts 30 s the stream is treated as full " +
					"and new events are refused")
		}
		return
	}
	if gs.failing {
		gs.failing = false
		log.Info().Str("stream", gs.stream).Msg("Measuring the stream's unread backlog again")
	}
	measured := make([]string, 0, len(m.readings))
	for _, r := range m.readings {
		measured = append(measured, r.durable)
	}
	for _, d := range gs.measured {
		if !slices.Contains(measured, d) {
			nmgr.metrics.forgetBackpressure(gs.stream, d)
		}
	}
	for d := range gs.drains {
		if !slices.Contains(measured, d) {
			delete(gs.drains, d)
		}
	}
	gs.measured = measured

	was := gs.refusing()
	refusing := was || gs.sampledAt.IsZero() || at.Sub(gs.sampledAt) > backpressureStaleAfter
	worst, ratio := "", 0.0
	gs.runwayClosed, gs.runwayDurable, gs.runwayHistory, gs.runwayRate = false, "", 0, 0
	for _, r := range m.readings {
		nmgr.metrics.setUnreadRatio(gs.stream, r.durable, r.ratio)
		if worst == "" || r.ratio > ratio {
			worst, ratio = r.durable, r.ratio
		}
		d := nextDrain(gs.drains[r.durable], at, r.history, m.firstSeq, m.full, r.unread, refusing)
		d.closed = runwayNext(d.closed, r.history, d.rate, m.full, r.unread)
		gs.drains[r.durable] = d
		nmgr.metrics.setHistoryRunway(gs.stream, r.durable, runwaySeconds(r.history, d.rate, m.full, r.unread))
		if d.closed && !gs.runwayClosed {
			gs.runwayClosed, gs.runwayDurable, gs.runwayHistory, gs.runwayRate = true, r.durable, r.history, d.rate
		}
	}
	gs.closed = gateNext(gs.closed, ratio)
	gs.worst, gs.ratio = worst, ratio
	gs.sampledAt = g.now()
	gs.kickBytes.Store(kickThreshold(m.maxBytes, max(1, g.kickShare)))
	gs.kickMsgs.Store(kickThreshold(m.maxMsgs, max(1, g.kickShare)))
	switch now := gs.refusing(); {
	case now && !was && gs.closed:
		log.Warn().Str("stream", gs.stream).Str("durable", worst).Str("cause", "unread").Float64("unreadRatio", ratio).
			Msg("Refusing new messages on this stream: a consumer's unread backlog is near the stream's ceiling, " +
				"and accepting more would discard messages it has not read")
	case now && !was:
		log.Warn().Str("stream", gs.stream).Str("durable", gs.runwayDurable).Str("cause", "history").
			Uint64("history", gs.runwayHistory).Float64("ratePerSecond", gs.runwayRate).
			Msg("Refusing new messages on this stream: the messages a consumer has already read, still held ahead " +
				"of its unread ones, would all be discarded within 30 s at the rate the full stream is discarding them")
	case !now && was:
		log.Info().Str("stream", gs.stream).Float64("unreadRatio", ratio).
			Msg("Accepting new messages on this stream again: no consumer's unread messages are close to being discarded")
	}
}

// MeasureBackpressureForTesting measures suffix's gate now, synchronously, as a tick of
// runBackpressure does. Production never calls it: a gate is measured every few seconds, and a
// test that fills a stream faster than that needs to say when each measurement happens.
//
// It first waits, for up to backpressureSampleTimeout, until the broker has counted every
// message the stream holds against each durable that gates it (see awaitConsumerCounts), so the
// measurement reflects every publish that has returned; then it measures with a fresh
// backpressureSampleTimeout of its own. When the broker never catches up, or cannot be read, it
// fails the test and does not measure: a measurement of a count the wait could not trust is the
// plausible answer the wait exists to prevent.
//
// It takes a test's T as an interface of the two methods it calls, Helper and Fatalf, so this
// package does not import testing, as SetAckWaitForTesting does.
func (nmgr *NatsManager) MeasureBackpressureForTesting(tb interface {
	Helper()
	Fatalf(format string, args ...any)
}, suffix string) {
	tb.Helper()
	wait, cancelWait := context.WithTimeout(context.Background(), backpressureSampleTimeout)
	err := nmgr.awaitConsumerCounts(wait, suffix)
	cancelWait()
	if err != nil {
		tb.Fatalf("measuring %s's backpressure: %v", suffix, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), backpressureSampleTimeout)
	defer cancel()
	nmgr.sampleBackpressure(ctx, suffix)
}

// awaitConsumerCounts waits until the broker has counted every message suffix's stream holds
// against each durable that gates it, or ctx ends.
//
// A PubAck is sent BEFORE the stream signals its consumers, and a consumer's NumPending is a
// counter kept from that signal, so a ConsumerInfo read straight after a publish can miss the
// message just acknowledged. Production does not care (it measures every few seconds, and the
// close ratio leaves a margin for it); a test that asserts the gate closed at exactly one
// message does.
//
// The count it waits for is LastSeq - max(Delivered.Stream, FirstSeq-1): every message after
// the last one delivered. That holds when every message in the stream matches the durable's
// filter and none was deleted from the middle, which is true of every fixture that measures
// through MeasureBackpressureForTesting. When it does not hold the wait times out and says so;
// it does not return a plausible answer.
//
// It reads ConsumerInfo through the gates' consumerInfo seam, the same call a sample makes.
// On ctx's end it reports the last thing it actually observed (a durable still behind, or a
// read that failed for a reason of its own), not the deadline cutting short the read in
// flight, which says nothing about the broker.
func (nmgr *NatsManager) awaitConsumerCounts(ctx context.Context, suffix string) error {
	if !streams.AppliesBackpressure(suffix) {
		return nil
	}
	if nmgr.js == nil {
		return errors.New("messaging: not connected")
	}
	stream := StreamName(nmgr.Microservice.InstanceId, suffix)
	g := nmgr.backpressure()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	var last error
	for {
		behind, err := nmgr.uncountedBy(ctx, g, stream, suffix)
		switch {
		case err == nil && behind == "":
			return nil
		case err == nil:
			last = fmt.Errorf("messaging: %s: the broker had not counted every stored message against its "+
				"consumers before the deadline: %s", stream, behind)
		case ctx.Err() == nil:
			last = fmt.Errorf("messaging: %s: could not read the consumers' counts: %w", stream, err)
		}
		select {
		case <-ctx.Done():
			if last == nil {
				return fmt.Errorf("messaging: %s: could not read the consumers' counts: %w", stream, ctx.Err())
			}
			return last
		case <-tick.C:
		}
	}
}

// uncountedBy names the first gating durable whose NumPending is not yet what the stream holds
// past its delivered sequence, as "<durable>: pending N, want M"; "" when none is. A durable
// that does not exist is skipped, as measureBackpressure skips it.
//
// It compares with !=, not <: an over-count is equally a state the gate must not be measured in.
func (nmgr *NatsManager) uncountedBy(ctx context.Context, g *backpressureGates, stream, suffix string) (string, error) {
	info, err := nmgr.js.StreamInfo(stream, nats.Context(ctx))
	if err != nil {
		return "", err
	}
	st := info.State
	// Every gated stream declares exactly one BackpressureReader today, so nothing tests that
	// this loop checks each durable rather than only the first: the first stream to declare a
	// second reader should add that test.
	for _, durable := range nmgr.gatingDurables(suffix) {
		ci, cerr := g.consumerInfo(ctx, stream, durable)
		if errors.Is(cerr, nats.ErrConsumerNotFound) {
			continue
		}
		if cerr != nil {
			return "", cerr
		}
		// FirstSeq-1 is the floor because a message DiscardOld has evicted is no longer
		// pending to anyone, delivered or not. A stream that has never held a message reports
		// FirstSeq 0 (and LastSeq 0); the guard only keeps FirstSeq-1 from wrapping there, since
		// want is 0 either way.
		from := ci.Delivered.Stream
		if st.FirstSeq > 0 {
			from = max(from, st.FirstSeq-1)
		}
		want := uint64(0)
		if st.LastSeq > from {
			want = st.LastSeq - from
		}
		if ci.NumPending != want {
			return fmt.Sprintf("%s: pending %d, want %d", durable, ci.NumPending, want), nil
		}
	}
	return "", nil
}

// durableReading is one gating durable as one sample measured it.
type durableReading struct {
	durable string
	ratio   float64
	history uint64 // historyAhead
	unread  bool   // hasUnread
}

// backpressureMeasurement is the broker half of a sample: one reading per gating durable
// (those that exist), and the stream's position and limits from the StreamInfo read after
// them.
type backpressureMeasurement struct {
	readings          []durableReading
	firstSeq          uint64
	full              bool
	maxBytes, maxMsgs int64
}

// measureBackpressure reads every gating durable's ConsumerInfo, then the StreamInfo, in that
// order (see historyAhead).
//
// The ratio rule reads its unread count from the ConsumerInfo and the stream's size from the
// StreamInfo after it, so arrivals in between are missing from the unread count: that is the
// same few milliseconds' under-count the PubAck-before-signal lag already gives it, and the
// close ratio's margin is for both.
func (nmgr *NatsManager) measureBackpressure(ctx context.Context, stream, suffix string) (backpressureMeasurement, error) {
	var m backpressureMeasurement
	if nmgr.js == nil {
		return m, errors.New("messaging: not connected")
	}
	type found struct {
		durable string
		ci      *nats.ConsumerInfo
	}
	var cis []found
	for _, durable := range nmgr.gatingDurables(suffix) {
		ci, cerr := nmgr.backpressure().consumerInfo(ctx, stream, durable)
		if errors.Is(cerr, nats.ErrConsumerNotFound) {
			continue
		}
		if cerr != nil {
			return m, cerr
		}
		cis = append(cis, found{durable, ci})
	}
	info, err := nmgr.js.StreamInfo(stream, nats.Context(ctx))
	if err != nil {
		return m, err
	}
	m.firstSeq = info.State.FirstSeq
	m.full = atCeiling(info.State, info.Config)
	m.maxBytes, m.maxMsgs = info.Config.MaxBytes, info.Config.MaxMsgs
	for _, f := range cis {
		m.readings = append(m.readings, durableReading{
			durable: f.durable,
			ratio:   unreadRatio(info.State, info.Config, f.ci),
			history: historyAhead(info.State, f.ci),
			unread:  hasUnread(info.State, f.ci),
		})
	}
	return m, nil
}

// gatingDurables names the durables whose backlog gates suffix's stream: one per area the
// stream declares in BackpressureReaders, less any area the instance does not deploy.
//
// 🔴 THE DEPLOYED-AREA FILTER IS WHAT KEEPS A REMOVED SERVICE FROM STOPPING INGEST. A durable
// outlives its service: an instance moved to a narrower profile keeps the durables of the
// areas it dropped, and nothing reads them again, so their backlog grows by every message the
// stream takes. Counted, such a durable would close the gate for good some time after the
// profile change. The deployed set is read from the per-area configuration mount every pod
// carries (core.EnabledFunctionalAreas), the same directory the chart writes one key per
// deployed area into.
//
// When that set cannot be read, or does not include this service's own area (so it is not
// the mount this assumes it is), every declared reader counts: a gate that cannot tell which
// readers are live errs toward protecting all of them.
func (nmgr *NatsManager) gatingDurables(suffix string) []string {
	declared := streams.BackpressureReadersFor(suffix)
	areas := declared
	if enabled, err := nmgr.backpressure().enabledAreas(); err == nil && slices.Contains(enabled, nmgr.Microservice.FunctionalArea) {
		areas = nil
		for _, a := range declared {
			if slices.Contains(enabled, a) {
				areas = append(areas, a)
			}
		}
	}
	out := make([]string, 0, len(areas))
	for _, a := range areas {
		out = append(out, DurableName(nmgr.Microservice.InstanceId, a, suffix))
	}
	return out
}

// gatesItsStream reports whether area's durable on suffix's stream is one whose unread
// backlog makes the stream refuse its writers: area is declared in the stream's
// BackpressureReaders. The reader's side uses it to leave that durable's unread ratio to
// the writers, who measure it for the gate (see consumerUnreadRatio).
//
// It does not apply gatingDurables' deployed-area filter, and need not: a reader is only
// built by a running service, so its area is deployed.
func gatesItsStream(suffix, area string) bool {
	return slices.Contains(streams.BackpressureReadersFor(suffix), area)
}

// bypassesBackpressure reports whether a batch is admitted past a closed gate: every message
// in it carries Message.BypassBackpressure.
func bypassesBackpressure(msgs []Message) bool {
	for i := range msgs {
		if !msgs[i].BypassBackpressure {
			return false
		}
	}
	return len(msgs) > 0
}

// countRefused records n messages a writer refused on stream.
func (nmgr *NatsManager) countRefused(stream string, n int) {
	if nmgr.metrics != nil {
		nmgr.metrics.countRefused(stream, n)
	}
}

// engagedCollector exports jetstream_backpressure_engaged at SCRAPE time, by consulting each
// gate. A gauge the sampler set would read 0 exactly when it matters most: a sample that
// fails leaves the last value in place while the gate, gone stale, refuses everything. Read
// at scrape, the series says what a writer would be told at that moment, staleness included.
type engagedCollector struct {
	desc *prometheus.Desc
	mu   sync.Mutex
	nmgr *NatsManager
}

func (c *engagedCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *engagedCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	nmgr := c.nmgr
	c.mu.Unlock()
	if nmgr == nil {
		return
	}
	g := nmgr.backpressure()
	g.mu.Lock()
	suffixes := make([]string, 0, len(g.gates))
	for s := range g.gates {
		suffixes = append(suffixes, s)
	}
	g.mu.Unlock()
	slices.Sort(suffixes)
	for _, s := range suffixes {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue,
			boolGauge(nmgr.Backpressure(s) != nil), StreamName(nmgr.Microservice.InstanceId, s))
	}
}

// bind attaches the collector to the manager whose gates it reports.
func (c *engagedCollector) bind(nmgr *NatsManager) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.nmgr = nmgr
	c.mu.Unlock()
}
