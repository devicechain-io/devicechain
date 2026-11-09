// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

var viewEpoch = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

// ---- the skew estimator ----

func TestSkewIsTheMinimumOfCaughtUpSamples(t *testing.T) {
	v := newSourceView()
	v.start()
	now := viewEpoch
	// a caught-up reader whose reads take 9, 5 then 7 ms: the estimate is the smallest so far
	want := []time.Duration{9, 5, 5}
	for i, d := range []time.Duration{9, 5, 7} {
		_, skew, clamped := v.observe(now, now.Add(-d*time.Millisecond), uint64(i+1), 0)
		require.False(t, clamped)
		require.Equal(t, want[i]*time.Millisecond, skew)
		now = now.Add(time.Second)
	}
}

func TestABacklogsAgeIsNotASkewSample(t *testing.T) {
	v := newSourceView()
	v.start()
	// a cold start inside a 3 s stall, a deep queue behind each delivery: ages are the backlog's, not the skew
	_, skew, _ := v.observe(viewEpoch, viewEpoch.Add(-3*time.Second), 1, 5000)
	require.Zero(t, skew, "a backlog's age was taken for the broker's clock offset")
	alpha, skew, _ := v.observe(viewEpoch, viewEpoch.Add(-2900*time.Millisecond), 2, 4999)
	require.Zero(t, skew)
	require.Equal(t, 2900*time.Millisecond, alpha)
}

func TestAStampInOurFutureIsASampleWhateverTheBacklog(t *testing.T) {
	v := newSourceView()
	v.start()
	// the leader is ahead and the reader is far behind: the stamp is still in the future of our clock
	_, skew, _ := v.observe(viewEpoch, viewEpoch.Add(1500*time.Millisecond), 1, 9000)
	require.Equal(t, -1500*time.Millisecond, skew, "a stamp from our future proves the broker's clock is at least that far ahead")
}

func TestSkewOnlyUsesIncreasingSequences(t *testing.T) {
	v := newSourceView()
	v.start()
	v.observe(viewEpoch, viewEpoch.Add(-10*time.Millisecond), 10, 0)
	// a lower sequence is out of stream order: it is not a sample even though it would lower the minimum
	_, skew, _ := v.observe(viewEpoch.Add(time.Second), viewEpoch.Add(time.Second).Add(-1*time.Millisecond), 9, 0)
	require.Equal(t, 10*time.Millisecond, skew)
}

func TestSkewIsHeldWhenNoSampleIsLeftInTheWindow(t *testing.T) {
	v := newSourceView()
	v.start()
	v.observe(viewEpoch, viewEpoch.Add(-40*time.Millisecond), 1, 0)
	// ten minutes later, in a stall: no caught-up sample for longer than the window, the estimate is held, not dropped
	got := v.view(viewEpoch.Add(10 * time.Minute))
	require.Equal(t, 40*time.Millisecond, got.Skew)
}

func TestSkewWindowSlides(t *testing.T) {
	v := newSourceView()
	v.start()
	v.observe(viewEpoch, viewEpoch.Add(-10*time.Millisecond), 1, 0)
	// a clock that has drifted: after the old minimum leaves the five-minute window the estimate follows
	late := viewEpoch.Add(6 * time.Minute)
	_, skew, _ := v.observe(late, late.Add(-300*time.Millisecond), 2, 0)
	require.Equal(t, 300*time.Millisecond, skew)
}

func TestSkewIsClampedAndReported(t *testing.T) {
	v := newSourceView()
	v.start()
	_, skew, clamped := v.observe(viewEpoch, viewEpoch.Add(5*time.Second), 1, 0)
	require.Equal(t, -skewClamp, skew)
	require.True(t, clamped)
	v2 := newSourceView()
	v2.start()
	_, skew, clamped = v2.observe(viewEpoch, viewEpoch.Add(-5*time.Second), 1, 0)
	require.Equal(t, skewClamp, skew)
	require.True(t, clamped)
}

// ---- the lag bound ----

func TestAStreamThatIsNotStartedHasNoLag(t *testing.T) {
	// the message-store source does not run unless the fail-closed store is on: it must not credit anyone
	require.Zero(t, newSourceView().view(viewEpoch).Lag)
}

func TestAStartedStreamNothingWasReadFromIsUnknown(t *testing.T) {
	v := newSourceView()
	v.start()
	require.Equal(t, unknownLag, v.view(viewEpoch).Lag)
	v.caughtUp(viewEpoch)
	require.Zero(t, v.view(viewEpoch).Lag, "seen idle: caught up")
}

func TestLagBoundsTheNextUnreadMessage(t *testing.T) {
	v := newSourceView()
	v.start()
	// read a message that was 3 s old with a queue behind it: the next unread one is at most 3 s old now, and 1 s older in a second
	v.observe(viewEpoch, viewEpoch.Add(-3*time.Second), 1, 100)
	require.Equal(t, 3*time.Second, v.view(viewEpoch).Lag)
	require.Equal(t, 4*time.Second, v.view(viewEpoch.Add(time.Second)).Lag)
	// read with nothing queued behind it: the delivery says nothing about what was appended after the broker
	// handed it over (the pod may have held it), so the age of the message still counts
	v.observe(viewEpoch.Add(10*time.Second), viewEpoch.Add(10*time.Second).Add(-5*time.Millisecond), 2, 0)
	require.Equal(t, 2*time.Second, v.view(viewEpoch.Add(12*time.Second)).Lag, "the read's own age is the 5 ms the skew estimate now absorbs")
	// and a delivery that was old when read keeps its age in the bound however little was queued behind it
	v.observe(viewEpoch.Add(20*time.Second), viewEpoch.Add(20*time.Second).Add(-3*time.Second), 3, 0)
	require.GreaterOrEqual(t, v.view(viewEpoch.Add(22*time.Second)).Lag, 4990*time.Millisecond)
}

func TestLagCoversTheMessageBeingAdmitted(t *testing.T) {
	v := newSourceView()
	v.start()
	// a caught-up read of a message that is itself 2 s old (the last of a backlog): the lag bound is not below its age
	v.observe(viewEpoch.Add(-time.Minute), viewEpoch.Add(-time.Minute).Add(-5*time.Millisecond), 1, 0) // a healthy reading first, so the skew is known
	alpha, _, _ := v.observe(viewEpoch, viewEpoch.Add(-2*time.Second), 2, 0)
	require.GreaterOrEqual(t, alpha, 1900*time.Millisecond)
	require.GreaterOrEqual(t, v.view(viewEpoch).Lag, alpha)
}

func TestAnIdleProbeNeverMovesTheLastReadBackwards(t *testing.T) {
	v := newSourceView()
	v.start()
	v.observe(viewEpoch.Add(5*time.Second), viewEpoch.Add(5*time.Second), 1, 0)
	v.caughtUp(viewEpoch) // an older probe answer arriving late
	at, _ := v.lastRead()
	require.Equal(t, viewEpoch.Add(5*time.Second), at)
}

// A message with no append time (metadata unavailable) tells nothing about the broker's clock or the
// reader's lag: it is metered live and must not move either estimate.
func TestAnUnstampedMessageFeedsNeitherEstimate(t *testing.T) {
	lc := NewLagCreditIngest(LagCreditIngestConfig{
		Ceiling: core.StaticCeiling(1000, 2000), Reading: core.StaticCeiling(1000, 2000), Factor: func(string) float64 { return 1 },
		Clock: func() time.Time { return viewEpoch },
	})
	lc.view(StreamCapture).start()
	m, ok := lc.admit("src", "acme", StreamCapture, messaging.Message{StreamSeq: 1, NumDelivered: 1})
	require.True(t, ok)
	require.Equal(t, core.RouteLive, m.route)
	require.Zero(t, lc.view(StreamCapture).readCount(), "an unstamped message was taken for a read of the stream")
	require.Equal(t, unknownLag, lc.view(StreamCapture).view(viewEpoch).Lag, "and it must not have made the stream look current")
	require.Zero(t, lc.view(StreamCapture).view(viewEpoch).Skew)
}

// ---- the reading stage ----

func readingFixture() (*LagCreditIngest, *replayClock) {
	clk := &replayClock{t: viewEpoch}
	lc := NewLagCreditIngest(LagCreditIngestConfig{
		Ceiling: core.StaticCeiling(1000, 2000), Reading: core.StaticCeiling(1000, 2000), Factor: func(string) float64 { return 1 },
		Clock: clk.now,
	})
	lc.view(StreamCapture).start()
	return lc, clk
}

// A message that sat a second between the two stages is charged with a second of credit more than the stream's
// lag gives it: the reader may be current while this message is old.
func TestTheReadingStageLendsTheDecodeDelay(t *testing.T) {
	lc, clk := readingFixture()
	m := ingestMeter{lc: lc, stream: StreamCapture, route: core.RouteFresh, stamp: clk.t, stagedAt: clk.t}
	lc.view(StreamCapture).observe(clk.t, clk.t.Add(-5*time.Millisecond), 1, 0)
	clk.t = clk.t.Add(time.Second)
	lc.view(StreamCapture).observe(clk.t, clk.t.Add(-5*time.Millisecond), 2, 0) // the reader is current
	require.True(t, m.readings("src", "acme", 2900), "2000 burst plus the second the message waited")
	require.False(t, m.readings("src", "other", 3500), "the delay is lent once, not without bound")
}

// The credit the reading stage may grant is the tenant's cover plus the delay, however old the stream says it is.
func TestTheReadingStageCapsItsCreditAtTheCoverPlusTheDelay(t *testing.T) {
	lc, clk := readingFixture()
	clk.t = clk.t.Add(time.Minute)
	// an unknown stream (lag 1h), the message's stamp ten seconds old, staged one second ago
	m := ingestMeter{lc: lc, stream: StreamCapture, route: core.RouteFresh, stamp: clk.t.Add(-10 * time.Second), stagedAt: clk.t.Add(-time.Second)}
	require.True(t, m.readings("src", "acme", 4500), "burst 2000 plus a 2 s cover and a 1 s delay of 1000/s")
	require.False(t, m.readings("src", "other", 5500))
}

// A backlog message is charged at the point of the send timeline it was sent at: the stamp with the broker's
// clock error taken back out.
func TestABacklogMessageIsChargedAtTheStampLessTheSkew(t *testing.T) {
	lc, clk := readingFixture()
	// the leader's clock is 1.5 s ahead: a stamp from our future teaches that
	lc.view(StreamCapture).observe(clk.t, clk.t.Add(1500*time.Millisecond), 1, 0)
	// a message sent 5 s ago is stamped 3.5 s ago by that leader and is read in a backlog
	msg := messaging.Message{AppendTime: clk.t.Add(-3500 * time.Millisecond), StreamSeq: 2, NumPending: 5000, NumDelivered: 1}
	m, ok := lc.admit("src", "acme", StreamCapture, msg)
	require.True(t, ok)
	require.Equal(t, core.RouteBacklog, m.route)
	require.Equal(t, clk.t.Add(-5*time.Second), m.sent)
}

// ---- the rate gate's routing ----

func TestTheLagCreditRateGateRoutesByOrigin(t *testing.T) {
	lc := NewLagCreditIngest(LagCreditIngestConfig{
		Ceiling: core.StaticCeiling(1, 1), Reading: core.StaticCeiling(10, 10), Factor: func(string) float64 { return 1 },
		Clock: func() time.Time { return viewEpoch },
	})
	var legacyCalls []Origin
	legacy := func(_, _ string, _ time.Time, _ bool, origin Origin) bool {
		legacyCalls = append(legacyCalls, origin)
		return true
	}
	gate := NewLagCreditRateGate(lc, legacy)

	require.True(t, gate("g", "acme", time.Time{}, false, OriginPreMetered), "a pre-metered message passes (the lifecycle is composed in front)")
	require.True(t, gate("g", "acme", time.Time{}, true, OriginAuthenticated), "a redelivery paid on delivery 1")
	require.Empty(t, legacyCalls)

	// an authenticated message with no stamp is metered in the lag-credited gate: burst 1, so the second is refused
	require.True(t, gate("mqtt", "acme", time.Time{}, false, OriginAuthenticated))
	require.False(t, gate("mqtt", "acme", time.Time{}, false, OriginAuthenticated))
	require.Empty(t, legacyCalls, "external MQTT must not spend the old limiters")

	// everything else is the existing gate's business
	require.True(t, gate("http", "acme", time.Time{}, false, OriginUntrusted))
	require.True(t, gate("int", "acme", time.Time{}, false, OriginIntegration))
	require.True(t, gate("x", "acme", viewEpoch, false, OriginAuthenticated), "a stamped time from any other caller keeps the old backlog meter")
	require.Equal(t, []Origin{OriginUntrusted, OriginIntegration, OriginAuthenticated}, legacyCalls)
}

func TestThePreMeteredOriginIsRefusedByTheExistingGate(t *testing.T) {
	l := func() *core.TenantRateLimiter { return core.NewTenantRateLimiter(core.StaticCeiling(10, 10)) }
	gate := NewRateGate(l(), l(), l(), l(), nil)
	require.Panics(t, func() { gate("g", "acme", time.Time{}, false, OriginPreMetered) },
		"metering a pre-metered message in the old gate would charge it twice or not at all")
}

func TestTheLagCreditReadingGateRoutesByOrigin(t *testing.T) {
	lc := NewLagCreditIngest(LagCreditIngestConfig{
		Ceiling: core.StaticCeiling(1, 1), Reading: core.StaticCeiling(5, 5), Factor: func(string) float64 { return 1 },
		Clock: func() time.Time { return viewEpoch },
	})
	var legacyCalls int
	gate := NewLagCreditReadingGate(lc, func(string, string, time.Time, bool, Origin, int) bool { legacyCalls++; return true })
	require.True(t, gate("mqtt", "acme", time.Time{}, false, OriginAuthenticated, 5))
	require.False(t, gate("mqtt", "acme", time.Time{}, false, OriginAuthenticated, 1), "the reading ceiling is charged in the new gate")
	require.True(t, gate("mqtt", "acme", time.Time{}, true, OriginAuthenticated, 1), "a redelivery is uncharged")
	require.Zero(t, legacyCalls)
	require.True(t, gate("http", "acme", time.Time{}, false, OriginUntrusted, 1))
	require.Equal(t, 1, legacyCalls)
}

// ---- the source in lagCredit mode (handle and the decode worker) ----

type lcHarness struct {
	*captureHarness
	lc    *LagCreditIngest
	clk   *replayClock
	mu    sync.Mutex
	sheds []core.IngestCause
	rsh   int
}

func newLCHarness(t *testing.T, msgRate float64, msgBurst int, readingBurst int, deleted func(string) bool) *lcHarness {
	t.Helper()
	l := &lcHarness{clk: &replayClock{t: time.Now()}}
	l.lc = NewLagCreditIngest(LagCreditIngestConfig{
		Ceiling: core.StaticCeiling(msgRate, msgBurst), Reading: core.StaticCeiling(msgRate, readingBurst),
		Factor: func(string) float64 { return 1 }, Clock: l.clk.now,
		Hooks: LagCreditHooks{
			Shed: func(_, _, _ string, _ core.IngestRoute, c core.IngestCause) {
				l.mu.Lock()
				l.sheds = append(l.sheds, c)
				l.mu.Unlock()
			},
			ReadingShed: func(string, string, int) { l.mu.Lock(); l.rsh++; l.mu.Unlock() },
		},
	})
	l.captureHarness = newCaptureHarnessWith(t, func(es *GatewayJetStreamSource) {
		l.lc.view(StreamCapture).start()
		es.UseLagCredit(l.lc, StreamCapture)
	})
	if deleted == nil {
		deleted = func(string) bool { return false }
	}
	legacy := func(string, string, time.Time, bool, Origin) bool { return true }
	l.captureHarness.gate = RefuseDeletedTenants(deleted, NewLagCreditRateGate(l.lc, legacy), nil)
	return l
}

func (l *lcHarness) send(seq uint64, deliveries int, stamp time.Time) *recordingAck {
	ack := &recordingAck{}
	m := capturedMsgAt(captureSubject, validEvent, seq, stamp, ack)
	m.NumDelivered = deliveries
	l.source.handle(m)
	return ack
}

func TestLagCreditModeShedsAFirstDeliveryOverTheCeilingAndAcksIt(t *testing.T) {
	l := newLCHarness(t, 1, 2, 100, nil)
	now := l.clk.now()
	var acks []*recordingAck
	for i := 0; i < 3; i++ {
		acks = append(acks, l.send(uint64(i+1), 1, now))
	}
	for i, a := range acks {
		require.Truef(t, a.settled(t), "message %d was never settled: a shed message must still ack (the ack trap)", i)
	}
	published, _, _, _ := l.counts()
	require.Equal(t, 2, published, "burst 2: the third message is over the ceiling")
	require.Equal(t, []core.IngestCause{core.CauseCeiling}, l.sheds)
}

func TestLagCreditModeRedeliveryIsUnchargedButStillRefusedForADeletedTenant(t *testing.T) {
	l := newLCHarness(t, 1, 1, 100, nil)
	now := l.clk.now()
	require.True(t, l.send(1, 1, now).settled(t))
	readsBefore := l.lc.view(StreamCapture).readCount()
	// the bucket is empty; a redelivery is admitted anyway and moves nothing
	require.True(t, l.send(2, 2, now.Add(-time.Hour)).settled(t))
	published, _, _, _ := l.counts()
	require.Equal(t, 2, published, "a redelivery paid on delivery 1")
	require.Equal(t, readsBefore, l.lc.view(StreamCapture).readCount(), "a redelivery must not feed the lag or skew estimate")
	require.Empty(t, l.sheds)

	deleted := newLCHarness(t, 1000, 1000, 100000, func(string) bool { return true })
	require.True(t, deleted.send(1, 2, deleted.clk.now()).settled(t))
	published, _, _, _ = deleted.counts()
	require.Zero(t, published, "a deleted tenant's redelivery must be refused in lagCredit mode too")
}

func TestLagCreditModeChargesReadingsOnTheRouteTheMessageStageChose(t *testing.T) {
	// a message ceiling that admits both, a reading ceiling of burst 1 and a negligible rate: the second event's one reading is shed after decode
	l := newLCHarness(t, 0.000001, 10, 1, nil)
	now := l.clk.now()
	a1 := l.send(1, 1, now)
	a2 := l.send(2, 1, now)
	require.True(t, a1.settled(t))
	require.True(t, a2.settled(t), "a message shed at the reading stage still acks")
	published, _, _, _ := l.counts()
	require.Equal(t, 1, published)
	l.mu.Lock()
	defer l.mu.Unlock()
	require.Equal(t, 1, l.rsh)
	require.Empty(t, l.sheds, "the message stage admitted both")
}

func TestLagCreditModeNeverCallsTheLegacyRoutingDecision(t *testing.T) {
	l := newLCHarness(t, 1000, 1000, 100000, nil)
	l.source.meter = func(string, time.Time) time.Time { panic("the age cutoff must not run in lagCredit mode") }
	require.True(t, l.send(1, 1, l.clk.now()).settled(t))
}

// ---- the idle probe ----

type fakeProber struct {
	pending atomic.Uint64
	acks    atomic.Uint64
	err     atomic.Bool
	calls   atomic.Int32
	// hook runs inside each question; it is set while the probe goroutine runs, so it is atomic.
	hook atomic.Pointer[func()]
}

func (f *fakeProber) Backlog(context.Context) (uint64, uint64, error) {
	f.calls.Add(1)
	if h := f.hook.Load(); h != nil {
		(*h)()
	}
	if f.err.Load() {
		return 0, 0, errors.New("broker unreachable")
	}
	return f.pending.Load(), f.acks.Load(), nil
}

func probeFixture(t *testing.T) (*GatewayJetStreamSource, *LagCreditIngest, *fakeProber, func()) {
	t.Helper()
	lc := NewLagCreditIngest(LagCreditIngestConfig{
		Ceiling: core.StaticCeiling(1000, 2000), Reading: core.StaticCeiling(1000, 2000), Factor: func(string) float64 { return 1 },
	})
	lc.view(StreamStore).start()
	es := &GatewayJetStreamSource{Id: "p"}
	p := &fakeProber{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { es.probeIdle(ctx, lc, StreamStore, p); close(done) }()
	return es, lc, p, func() { cancel(); <-done }
}

func TestTheIdleProbeMarksAQuietCaughtUpStreamCurrent(t *testing.T) {
	_, lc, p, stop := probeFixture(t)
	defer stop()
	require.Equal(t, unknownLag, lc.view(StreamStore).view(time.Now()).Lag)
	require.Eventually(t, func() bool { return lc.view(StreamStore).view(time.Now()).Lag < time.Second }, 3*time.Second, 20*time.Millisecond,
		"a started stream with nothing queued must stop being unknown")
	require.Positive(t, p.calls.Load())
}

func TestTheIdleProbeDoesNotTrustAStreamWithAQueue(t *testing.T) {
	_, lc, p, stop := probeFixture(t)
	p.pending.Store(10)
	time.Sleep(900 * time.Millisecond)
	stop()
	require.Positive(t, p.calls.Load())
	require.Equal(t, unknownLag, lc.view(StreamStore).view(time.Now()).Lag, "a queue at the broker is not caught up")
}

// A fetched batch the loop has not read out of its buffer is delivered but unacknowledged: pending is 0 and the
// stream is not caught up.
func TestTheIdleProbeDoesNotTrustADeliveredButUnackedBatch(t *testing.T) {
	_, lc, p, stop := probeFixture(t)
	p.acks.Store(100)
	time.Sleep(900 * time.Millisecond)
	stop()
	require.Positive(t, p.calls.Load())
	require.Equal(t, unknownLag, lc.view(StreamStore).view(time.Now()).Lag)
}

// A delivery that said nothing was queued does not shorten the bound: the pod may have held it for ten seconds.
func TestAZeroPendingDeliveryHeldLocallyKeepsItsAgeInTheBound(t *testing.T) {
	v := newSourceView()
	v.start()
	v.observe(viewEpoch.Add(-time.Minute), viewEpoch.Add(-time.Minute).Add(-5*time.Millisecond), 1, 0) // skew known
	// delivered with nothing behind it, held for ten seconds, then read: it is ten seconds old
	alpha, _, _ := v.observe(viewEpoch, viewEpoch.Add(-10*time.Second), 2, 0)
	require.GreaterOrEqual(t, alpha, 9900*time.Millisecond)
	// another message was appended one second after its delivery; five seconds on it is about fourteen seconds old
	require.GreaterOrEqual(t, v.view(viewEpoch.Add(5*time.Second)).Lag, 14*time.Second)
}

func TestTheIdleProbeIgnoresAnErrorAndALoopBlockedInHandle(t *testing.T) {
	es, lc, p, stop := probeFixture(t)
	p.err.Store(true)
	time.Sleep(600 * time.Millisecond)
	require.Equal(t, unknownLag, lc.view(StreamStore).view(time.Now()).Lag, "an unreachable broker proves nothing")
	p.err.Store(false)
	es.inHandle.Store(true)
	time.Sleep(300 * time.Millisecond)
	callsBlocked := p.calls.Load()
	time.Sleep(600 * time.Millisecond)
	require.Equal(t, callsBlocked, p.calls.Load(), "no question is asked while the loop is inside handle (its buffer is invisible to the broker)")
	require.Equal(t, unknownLag, lc.view(StreamStore).view(time.Now()).Lag)
	stop()
}

func TestTheIdleProbeDiscardsAnAnswerThatOverlappedARead(t *testing.T) {
	_, lc, p, stop := probeFixture(t)
	// a message is read while the question is in flight: the broker's "nothing queued" predates it and may describe a batch the loop holds
	hook := func() { lc.view(StreamStore).observe(time.Now(), time.Now().Add(-2*time.Second), 1, 5000) }
	p.hook.Store(&hook)
	time.Sleep(900 * time.Millisecond)
	stop()
	require.GreaterOrEqual(t, lc.view(StreamStore).view(time.Now()).Lag, 2*time.Second, "the overlapped answer was used")
}

// ---- parity with the existing gate's accounting ----

// An admission metered at the platform default because the tenant's own ceiling is not known is counted, as
// the existing gate's message limiters count it (core.WithUnresolvedAdmissions): the ingest
// governance_unresolved_admissions_total series, and the alert on its unreachable cause, must not go quiet
// for the gateway and external MQTT traffic when an operator switches to lagCredit. A denied message and a
// resolved or static ceiling are not counted, and the reading stage is not counted (the existing reading
// limiters do not count either).
func TestLagCreditAdmissionsAtAnUnresolvedCeilingAreCounted(t *testing.T) {
	var counted []core.CeilingSource
	ceiling := func(tenant string) core.TenantCeiling {
		switch tenant {
		case "unreachable":
			return core.TenantCeiling{RatePerSecond: 1, Burst: 1, Source: core.CeilingUnreachable}
		case "pending":
			return core.TenantCeiling{RatePerSecond: 1, Burst: 1, Source: core.CeilingPending}
		case "unknown":
			return core.TenantCeiling{RatePerSecond: 1, Burst: 1, Source: core.CeilingUnknownTenant}
		}
		return core.TenantCeiling{RatePerSecond: 1, Burst: 1, Source: core.CeilingResolved}
	}
	lc := NewLagCreditIngest(LagCreditIngestConfig{
		Ceiling: ceiling, Reading: ceiling, Factor: func(string) float64 { return 1 },
		Clock: func() time.Time { return viewEpoch },
		Hooks: LagCreditHooks{Unresolved: func(src core.CeilingSource) { counted = append(counted, src) }},
	})
	lc.view(StreamCapture).start()
	stamped := func(seq uint64) messaging.Message {
		return messaging.Message{AppendTime: viewEpoch, StreamSeq: seq, NumDelivered: 1}
	}

	_, ok := lc.admit("gw", "unreachable", StreamCapture, stamped(1))
	require.True(t, ok)
	_, ok = lc.admit("gw", "unreachable", StreamCapture, stamped(2))
	require.False(t, ok, "burst 1")
	require.True(t, lc.admitLive("mqtt", "pending"))
	m, ok := lc.admit("gw", "resolved", StreamCapture, stamped(3))
	require.True(t, ok)
	require.True(t, m.readings("gw", "unknown", 1), "the reading stage")
	// a backlog admission at the default is counted too
	old := messaging.Message{AppendTime: viewEpoch.Add(-time.Minute), StreamSeq: 4, NumPending: 1000, NumDelivered: 1}
	m, ok = lc.admit("gw", "unknown", StreamCapture, old)
	require.True(t, ok)
	require.Equal(t, core.RouteBacklog, m.route)

	require.Equal(t, []core.CeilingSource{core.CeilingUnreachable, core.CeilingPending, core.CeilingUnknownTenant}, counted)
}

// With the lag-credited branch taken, the gateway asks the composed gate with OriginPreMetered and skips the
// reading stage's Readings gate, so the legacy transcript cannot pass: the existing gate refuses the origin
// loudly. This is what makes TestTheLegacyGateIsUnchangedByTheLagCreditedGate a statement about the
// branch rather than about a source that never takes it.
func TestTheLegacyTranscriptCannotPassWithTheLagCreditedBranchTaken(t *testing.T) {
	require.PanicsWithValue(t, "processor: OriginPreMetered reached the existing rate gate; it is valid only with the lag-credited gate (ingestMetering: lagCredit)", func() {
		legacyTranscript(t, func(es *GatewayJetStreamSource) {
			lc := NewLagCreditIngest(LagCreditIngestConfig{
				Ceiling: core.StaticCeiling(10, 5), Reading: core.StaticCeiling(10, 3), Factor: func(string) float64 { return 1 },
			})
			lc.view(StreamCapture).start()
			es.UseLagCredit(lc, StreamCapture)
		})
	})
}

// A DOCUMENTED RESIDUAL, pinned so it cannot change unnoticed (second review, finding P1-a). A pod with no
// skew sample yet has nothing to tell a small backlog's age from the broker's clock offset: a delivery
// with few messages queued behind it (NumPending at most caughtUpPending) is taken as current, so the
// first such delivery after a stall teaches its own age as the skew and the stall earns no credit. A warm
// pod, which learned the offset from traffic before the stall, admits the same backlog in full.
//
// The scenario: a 10/s, burst-20 tenant sends its burst at t=0 and then 10/s, while nothing is read for
// 1.5 s; the 35 queued messages are then drained at once. The tenant never exceeded its ceiling. The
// existing (legacy) gate meters these at now too, and sheds the same 15: the residual is not a regression,
// it is the part of the stall the lag credit cannot see on a pod that has never measured its broker.
func TestAColdPodCannotTellASmallStallFromClockSkew(t *testing.T) {
	run := func(warm bool) int {
		clk := &replayClock{t: viewEpoch}
		lc := NewLagCreditIngest(LagCreditIngestConfig{
			Ceiling: core.StaticCeiling(10, 20), Reading: core.StaticCeiling(1000, 2000), Factor: func(string) float64 { return 1 },
			Clock: clk.now,
		})
		lc.view(StreamCapture).start()
		seq := uint64(0)
		if warm {
			// another tenant's traffic, read as it arrives, before the stall: the offset is learned (0)
			for i := 0; i < 5; i++ {
				seq++
				_, _ = lc.admit("gw", "neighbour", StreamCapture, messaging.Message{AppendTime: clk.t, StreamSeq: seq, NumDelivered: 1})
				clk.t = clk.t.Add(10 * time.Millisecond)
			}
		}
		start := clk.t
		var stamps []time.Time
		for i := 0; i < 20; i++ {
			stamps = append(stamps, start)
		}
		for i := 1; i <= 15; i++ {
			stamps = append(stamps, start.Add(time.Duration(i)*100*time.Millisecond))
		}
		clk.t = start.Add(1500 * time.Millisecond) // the stall ends; drain everything now
		admitted := 0
		for i, st := range stamps {
			seq++
			msg := messaging.Message{AppendTime: st, StreamSeq: seq, NumPending: uint64(len(stamps) - 1 - i), NumDelivered: 1}
			if _, ok := lc.admit("gw", "acme", StreamCapture, msg); ok {
				admitted++
			}
		}
		return admitted
	}
	require.Equal(t, 35, run(true), "a warm pod credits the stall: the compliant tenant loses nothing")
	require.Equal(t, 20, run(false), "the documented cold-pod residual: the stall's age is taken for skew")

	// the existing gate, on the same drain: every message is younger than burst/rate (2 s), so all are
	// metered at now in the live bucket, and the same 15 are shed
	clk := &replayClock{t: viewEpoch.Add(1500 * time.Millisecond)}
	live := core.NewTenantRateLimiter(core.StaticCeiling(10, 20), core.WithClock(clk.now))
	l := func() *core.TenantRateLimiter {
		return core.NewTenantRateLimiter(core.StaticCeiling(10, 20), core.WithClock(clk.now))
	}
	gate, meterAt := NewRateGate(live, l(), l(), l(), nil), newMeterAtClock(live, clk.now)
	legacy := 0
	for i := 0; i < 35; i++ {
		stamp := viewEpoch
		if i >= 20 {
			stamp = viewEpoch.Add(time.Duration(i-19) * 100 * time.Millisecond)
		}
		if gate("gw", "acme", meterAt("acme", stamp), false, OriginAuthenticated) {
			legacy++
		}
	}
	require.Equal(t, 20, legacy, "legacy sheds the same messages: the residual is not a regression")
}
