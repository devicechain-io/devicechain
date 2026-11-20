// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func placementHarness(t *testing.T) (*captureHarness, *atomic.Bool, prometheus.Gauge) {
	t.Helper()
	placed := &atomic.Bool{}
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "placement_held_messages"})
	h := newCaptureHarnessWith(t, func(s *GatewayJetStreamSource) {
		s.UsePlacementCheck(func(string) error {
			if placed.Load() {
				return nil
			}
			return messaging.ErrUnplacedTenant
		})
		s.UsePlacementHeldGauge(gauge)
		s.finalHoldBeat, s.finalHoldPoll = 5*time.Millisecond, 5*time.Millisecond
	})
	h.publishErr = messaging.ErrUnplacedTenant
	return h, placed, gauge
}

func waitHeld(t *testing.T, gauge prometheus.Gauge, n float64) {
	t.Helper()
	require.Eventually(t, func() bool { return testutil.ToFloat64(gauge) == n }, 2*time.Second, time.Millisecond)
}

func noMoreBeats(t *testing.T, ack *progressAck) {
	t.Helper()
	beats := ack.beats.Load()
	time.Sleep(30 * time.Millisecond)
	require.Equal(t, beats, ack.beats.Load(), "terminal settlement must stop hold heartbeats")
}

func TestPlacementRerunRefusalRetainsOneOwner(t *testing.T) {
	h, placed, gauge := placementHarness(t)
	ack := &progressAck{}
	h.source.handle(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, 30, ack))
	h.quiesce(t, 1)
	waitHeld(t, gauge, 1)
	placed.Store(true) // The check succeeds, but forwarding loses placement again.
	h.quiesce(t, 2)
	placed.Store(false)
	require.Zero(t, ack.counts())
	waitHeld(t, gauge, 1)
	before := ack.beats.Load()
	require.Eventually(t, func() bool { return ack.beats.Load() >= before+3 }, time.Second, time.Millisecond)
	h.mu.Lock()
	h.publishErr = nil
	h.mu.Unlock()
	placed.Store(true)
	require.True(t, ack.settled(t))
	waitHeld(t, gauge, 0)
	noMoreBeats(t, ack)
	require.Equal(t, 1, ack.counts())
	_, failed, _, _ := h.counts()
	require.Zero(t, failed)
	for _, redelivery := range h.flags() {
		require.True(t, redelivery)
	}
}

func TestPlacementRerunPoisonStopsBeating(t *testing.T) {
	for _, routeFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "acked", true: "route-unavailable"}[routeFails], func(t *testing.T) {
			h, placed, gauge := placementHarness(t)
			ack := &progressAck{}
			h.source.handle(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, 31, ack))
			h.quiesce(t, 1)
			waitHeld(t, gauge, 1)
			poison := errors.New("publish rejected for a non-placement reason")
			h.mu.Lock()
			h.publishErr = poison
			if routeFails {
				h.failedErr = errors.New("failed-decode unavailable")
			}
			h.mu.Unlock()
			placed.Store(true)
			h.quiesce(t, 2)
			waitHeld(t, gauge, 0)
			noMoreBeats(t, ack)
			_, failed, _, _ := h.counts()
			require.Equal(t, 1, failed)
			h.mu.Lock()
			reason := h.failedReasons[0]
			h.mu.Unlock()
			require.ErrorIs(t, reason, poison)
			if routeFails {
				require.Zero(t, ack.counts())
			} else {
				require.Equal(t, 1, ack.counts())
			}
		})
	}
}

func TestPlacementRerunEarlyDropStopsBeating(t *testing.T) {
	h, placed, gauge := placementHarness(t)
	var gone atomic.Bool
	h.gate = func(string, string, time.Time, bool, Origin) bool { return !gone.Load() }
	ack := &progressAck{}
	h.source.handle(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, 32, ack))
	h.quiesce(t, 1)
	waitHeld(t, gauge, 1)
	gone.Store(true)
	placed.Store(true)
	require.True(t, ack.settled(t))
	waitHeld(t, gauge, 0)
	noMoreBeats(t, ack)
	published, failed, _, _ := h.counts()
	require.Equal(t, 1, published, "the lifecycle refusal must precede another decode/publish")
	require.Zero(t, failed)
	require.Equal(t, 1, ack.counts())
}

func TestPlacementHoldBeatsThroughAsynchronousRerun(t *testing.T) {
	h, placed, gauge := placementHarness(t)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	build := h.source.build
	var attempts atomic.Int64
	h.source.build = func(source, tenant string, event *model.UnresolvedEvent, payload interface{}, id string) (context.Context, messaging.Message, bool) {
		if attempts.Add(1) > 1 {
			close(started)
			<-release
		}
		return build(source, tenant, event, payload, id)
	}
	ack := &progressAck{}
	h.source.handle(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, 33, ack))
	h.quiesce(t, 1)
	h.mu.Lock()
	h.publishErr = nil
	h.mu.Unlock()
	placed.Store(true)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery never reached decode/publish")
	}
	beats := ack.beats.Load()
	require.Eventually(t, func() bool { return ack.beats.Load() >= beats+3 }, time.Second, time.Millisecond,
		"a rerun is asynchronous: keep beating until its terminal settlement")
	require.EqualValues(t, 2, attempts.Load(), "only one recovery owns the held message")
	waitHeld(t, gauge, 1)
	require.Zero(t, ack.counts())
	releaseOnce.Do(func() { close(release) })
	require.True(t, ack.settled(t))
	waitHeld(t, gauge, 0)
	noMoreBeats(t, ack)
}

func TestPlacementHoldShutdownJoinsBeforeClosingMessages(t *testing.T) {
	h, placed, gauge := placementHarness(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var admissions atomic.Int64
	h.gate = func(string, string, time.Time, bool, Origin) bool {
		if admissions.Add(1) == 2 {
			close(entered)
			<-release
		}
		return true
	}
	ack := &progressAck{}
	h.source.handle(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, 34, ack))
	h.quiesce(t, 1)
	waitHeld(t, gauge, 1)
	placed.Store(true)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery never entered handle")
	}
	h.source.handle(capturedMsg(captureSubject, validEvent, 1, 340, &recordingAck{}))
	h.quiesce(t, 2)
	require.EqualValues(t, 1, h.source.inHandle.Load(), "reader completion must not hide an active recovery")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	stopped := h.source.stopPipeline(ctx)
	cancel()
	require.False(t, stopped, "stop must join the recovery worker before closing messages")
	require.False(t, h.source.messagesClosed, "a live recovery can still send")
	once.Do(func() { close(release) })
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.True(t, h.source.stopPipeline(ctx))
	select {
	case <-h.source.finalHolds.done:
	default:
		t.Fatal("hold loops were not joined")
	}
	waitHeld(t, gauge, 0)
	require.Zero(t, ack.counts(), "shutdown abandons the final delivery unacked")
	_, failed, _, _ := h.counts()
	require.Zero(t, failed)
	noMoreBeats(t, ack)
}

func TestPlacementHoldCapAndNonFinalFallback(t *testing.T) {
	h, _, gauge := placementHarness(t)
	nonfinal := &progressAck{}
	h.source.settler(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver-1, 1, nonfinal), "acme")(messaging.ErrUnplacedTenant)
	require.Zero(t, nonfinal.beats.Load())
	waitHeld(t, gauge, 0)
	ack := &progressAck{}
	for seq := uint64(1); seq <= finalPlacementHoldLimit; seq++ {
		h.source.settler(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, seq, ack), "acme")(messaging.ErrUnplacedTenant)
	}
	overflow := &progressAck{}
	h.source.settler(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, finalPlacementHoldLimit+1, overflow), "acme")(messaging.ErrUnplacedTenant)
	require.Equal(t, float64(finalPlacementHoldLimit), testutil.ToFloat64(gauge), "per-source memory is bounded")
	require.Zero(t, overflow.beats.Load(), "overflow keeps the old unacked behavior")
	require.Zero(t, overflow.counts())
	require.Zero(t, ack.counts())
}

func TestMessageStorePlacementRerunPreservesDedupID(t *testing.T) {
	h, placed, gauge := placementHarness(t)
	h.source.UseMessageStore()
	build := h.source.build
	var mu sync.Mutex
	var ids []string
	h.source.build = func(source, tenant string, event *model.UnresolvedEvent, payload interface{}, id string) (context.Context, messaging.Message, bool) {
		mu.Lock()
		ids = append(ids, id)
		mu.Unlock()
		return build(source, tenant, event, payload, id)
	}
	ack := &progressAck{}
	msg := capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, 35, ack)
	msg.DedupID = "mqtt-store:acme:epoch-7:35"
	h.source.handle(msg)
	h.quiesce(t, 1)
	waitHeld(t, gauge, 1)
	h.mu.Lock()
	h.publishErr = nil
	h.mu.Unlock()
	placed.Store(true)
	require.True(t, ack.settled(t))
	waitHeld(t, gauge, 0)
	mu.Lock()
	got := append([]string(nil), ids...)
	mu.Unlock()
	require.Equal(t, []string{msg.DedupID, msg.DedupID}, got)
	require.Equal(t, []bool{true, true}, h.flags())
	require.Equal(t, 1, ack.counts())
}

func TestPlacementHoldPollsEachTenant(t *testing.T) {
	var acmeReady, globexReady atomic.Bool
	h := newCaptureHarnessWith(t, func(s *GatewayJetStreamSource) {
		s.UsePlacementCheck(func(tenant string) error {
			if tenant == "acme" && acmeReady.Load() || tenant == "globex" && globexReady.Load() {
				return nil
			}
			return messaging.ErrUnplacedTenant
		})
		s.finalHoldBeat, s.finalHoldPoll = 5*time.Millisecond, 5*time.Millisecond
	})
	h.publishErr = messaging.ErrUnplacedTenant
	acme, globex := &progressAck{}, &progressAck{}
	h.source.handle(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, 36, acme))
	h.source.handle(capturedMsg("inst-1.globex.devices.sensor-001.events", validEvent, messaging.MaxDeliver, 37, globex))
	h.quiesce(t, 2)
	h.mu.Lock()
	h.publishErr = nil
	h.mu.Unlock()
	globexReady.Store(true)
	require.True(t, globex.settled(t), "one tenant's unavailable placement must not block another")
	require.Zero(t, acme.counts())
	before := acme.beats.Load()
	require.Eventually(t, func() bool { return acme.beats.Load() >= before+3 }, time.Second, time.Millisecond)
	acmeReady.Store(true)
	require.True(t, acme.settled(t))
	noMoreBeats(t, acme)
	noMoreBeats(t, globex)
}

type placementEnvelopeWriter struct {
	envWriter
	ready *atomic.Bool
}

func (w placementEnvelopeWriter) PublishAcked(ctx context.Context, msg messaging.Message, done func(messaging.PubAckInfo, error)) {
	if !w.ready.Load() {
		done(messaging.PubAckInfo{}, messaging.ErrUnplacedTenant)
		return
	}
	w.envWriter.PublishAcked(ctx, msg, done)
}

type placementEnvelopeRounds struct {
	envRounds
	ready   *atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *placementEnvelopeRounds) Publish(ctx context.Context, msg messaging.Message) (messaging.PubAckInfo, error) {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.release:
	case <-ctx.Done():
		return messaging.PubAckInfo{}, ctx.Err()
	}
	if !r.ready.Load() {
		return messaging.PubAckInfo{}, messaging.ErrUnplacedTenant
	}
	return r.envRounds.Publish(ctx, msg)
}

func TestEnvelopePlacementHoldStartsAfterWalkAndForgetsBeats(t *testing.T) {
	b := newEnvBroker()
	var ready atomic.Bool
	rounds := &placementEnvelopeRounds{envRounds: envRounds{b}, ready: &ready, entered: make(chan struct{}), release: make(chan struct{})}
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "envelope_placement_held_messages"})
	es := NewGatewayJetStreamSource(nil, "gw-env", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {},
		func(_ string, tenant string, event *model.UnresolvedEvent, _ interface{}, id string) (context.Context, messaging.Message, bool) {
			return core.WithTenant(context.Background(), tenant), messaging.Message{Value: pbDevice(event.Device), DedupID: id}, true
		}, func(string, string, []byte, error) error {
			t.Error("placement must not go to failed-decode")
			return nil
		}, nil, admitAllReadings, nil)
	es.SetWriter(placementEnvelopeWriter{envWriter{b}, &ready})
	es.UsePlacementCheck(func(string) error {
		if ready.Load() {
			return nil
		}
		return messaging.ErrUnplacedTenant
	})
	es.UsePlacementHeldGauge(gauge)
	es.finalHoldBeat, es.finalHoldPoll = 5*time.Millisecond, 5*time.Millisecond
	require.NoError(t, es.UseEnvelopes(CaptureEnvelopes{Rounds: rounds, Former: messaging.FormerConfig{Linger: time.Millisecond}}))
	es.startPipeline(2)
	es.ce.engine.SetGiveUp(100 * time.Millisecond)
	es.ce.engine.SetBeatEvery(5 * time.Millisecond)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		require.True(t, es.stopPipeline(ctx))
	})
	ack := &progressAck{}
	es.handle(envCaptured("acme", 38, messaging.MaxDeliver, midBucket(), ack))
	select {
	case <-rounds.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("envelope walk never started")
	}
	require.Zero(t, testutil.ToFloat64(gauge), "the engine owns retries until it finally settles")
	close(rounds.release)
	waitHeld(t, gauge, 1)
	require.Zero(t, ack.counts())
	assertNoEnvelopeBeatEntries(t, es)
	before := ack.beats.Load()
	require.Eventually(t, func() bool { return ack.beats.Load() >= before+3 }, time.Second, time.Millisecond)
	ready.Store(true)
	require.True(t, ack.settled(t))
	waitHeld(t, gauge, 0)
	noMoreBeats(t, ack)
	assertNoEnvelopeBeatEntries(t, es)
	require.Equal(t, 1, ack.counts())
	require.Equal(t, []string{"acme-38"}, b.devices(t))
}

func assertNoEnvelopeBeatEntries(t *testing.T, es *GatewayJetStreamSource) {
	t.Helper()
	n := 0
	es.beats.Range(func(_, _ interface{}) bool { n++; return true })
	require.Zero(t, n, "settlement must release the envelope beat-table entry")
}

// The writer reports its pending asynchronous recovery as refused when draining.
// This must end as an abandoned hold, never as poison.
type placementDrainWriter struct {
	attempts atomic.Int64
	pending  chan func(error)
	started  chan struct{}
}

func (w *placementDrainWriter) Publish(_ context.Context, _ messaging.Message, done func(error)) {
	if w.attempts.Add(1) == 1 {
		done(messaging.ErrUnplacedTenant)
		return
	}
	w.pending <- done
	close(w.started)
}
func (w *placementDrainWriter) Fail(err error, done func(error)) { done(err) }
func (w *placementDrainWriter) Draining() {
	select {
	case done := <-w.pending:
		done(errors.New("writer draining"))
	default:
	}
}
func (*placementDrainWriter) Close() {}

func TestPlacementHoldShutdownAbandonsPendingRerun(t *testing.T) {
	var ready atomic.Bool
	writer := &placementDrainWriter{pending: make(chan func(error), 1), started: make(chan struct{})}
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "draining_placement_holds"})
	h := newCaptureHarnessWith(t, func(s *GatewayJetStreamSource) {
		s.SetWriter(writer)
		s.UsePlacementCheck(func(string) error {
			if ready.Load() {
				return nil
			}
			return messaging.ErrUnplacedTenant
		})
		s.UsePlacementHeldGauge(gauge)
		s.finalHoldBeat, s.finalHoldPoll = 5*time.Millisecond, 5*time.Millisecond
	})
	ack := &progressAck{}
	h.source.handle(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, 39, ack))
	waitHeld(t, gauge, 1)
	ready.Store(true)
	select {
	case <-writer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("asynchronous rerun never submitted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.True(t, h.source.stopPipeline(ctx))
	waitHeld(t, gauge, 0)
	require.Zero(t, ack.counts(), "a held rerun abandoned at shutdown stays unacked")
	_, failed, _, _ := h.counts()
	require.Zero(t, failed, "a drain refusal must not route held telemetry as poison")
	noMoreBeats(t, ack)
}

func holdFallbackHarness(t *testing.T, maxAge time.Duration) (*captureHarness, *atomic.Bool, prometheus.Gauge, *prometheus.CounterVec) {
	t.Helper()
	placed := &atomic.Bool{}
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "placement_held_messages"})
	fallback := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "placement_hold_fallback"}, []string{"source", "reason"})
	h := newCaptureHarnessWith(t, func(s *GatewayJetStreamSource) {
		s.UsePlacementCheck(func(string) error {
			if placed.Load() {
				return nil
			}
			return messaging.ErrUnplacedTenant
		})
		s.UsePlacementHeldGauge(gauge)
		s.UsePlacementHoldFallbackCounter(fallback)
		s.finalHoldBeat, s.finalHoldPoll = 5*time.Millisecond, 5*time.Millisecond
		s.finalHoldMaxAge = maxAge
	})
	h.publishErr = messaging.ErrUnplacedTenant
	return h, placed, gauge, fallback
}

func TestPlacementHoldExpiresAtMaxAgeAndFallsBackUnacked(t *testing.T) {
	h, _, gauge, fallback := holdFallbackHarness(t, 60*time.Millisecond)
	ack := &progressAck{}
	h.source.settler(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, 1, ack), "acme")(messaging.ErrUnplacedTenant)
	waitHeld(t, gauge, 1)
	waitHeld(t, gauge, 0)
	// The counter moves before the gauge drops, so it is already visible here.
	require.Equal(t, float64(1), testutil.ToFloat64(fallback.WithLabelValues("gw-test", "expired")), "an expired hold is counted, never silent")
	require.Zero(t, ack.counts(), "the expired message is left unacked for the max-delivery recorder, not acked or dropped")
	noMoreBeats(t, ack)
	// The slot is free again: the tenant that never came back no longer holds capacity.
	other := &progressAck{}
	h.source.settler(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, 2, other), "other")(messaging.ErrUnplacedTenant)
	waitHeld(t, gauge, 1)
}

func TestPlacementHoldUnexpiredIsKept(t *testing.T) {
	h, placed, gauge, fallback := holdFallbackHarness(t, time.Hour)
	ack := &progressAck{}
	h.source.settler(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, 1, ack), "acme")(messaging.ErrUnplacedTenant)
	waitHeld(t, gauge, 1)
	time.Sleep(60 * time.Millisecond)
	require.Equal(t, float64(1), testutil.ToFloat64(gauge))
	require.Zero(t, testutil.ToFloat64(fallback.WithLabelValues("gw-test", "expired")))
	_ = placed
}

func TestPlacementHoldFullIsCountedAndItsLogRateLimited(t *testing.T) {
	buf := logSink.Capture(t)
	h, _, gauge, fallback := holdFallbackHarness(t, time.Hour)
	ack := &progressAck{}
	for seq := uint64(1); seq <= finalPlacementHoldLimit; seq++ {
		h.source.settler(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, seq, ack), "acme")(messaging.ErrUnplacedTenant)
	}
	waitHeld(t, gauge, finalPlacementHoldLimit)
	for i := 0; i < 50; i++ {
		h.source.settler(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, uint64(finalPlacementHoldLimit+1+i), &progressAck{}), "other")(messaging.ErrUnplacedTenant)
	}
	require.Equal(t, float64(50), testutil.ToFloat64(fallback.WithLabelValues("gw-test", "full")))
	require.Equal(t, 1, strings.Count(buf.String(), "Final placement hold is full"), "one log per interval, not one per refusal")
}

func TestPlacementRecoveryCountsInboundOnce(t *testing.T) {
	h, placed, gauge := placementHarness(t)
	ack := &progressAck{}
	h.source.handle(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, 40, ack))
	h.quiesce(t, 1)
	waitHeld(t, gauge, 1)
	h.mu.Lock()
	h.publishErr = nil
	h.mu.Unlock()
	placed.Store(true)
	require.True(t, ack.settled(t))
	waitHeld(t, gauge, 0)
	_, _, received, _ := h.counts()
	require.Equal(t, 1, received, "a recovered message is one inbound message, not two")
}

func TestPlacementHoldExpiryWaitsForAnInFlightRerun(t *testing.T) {
	h, _, gauge, fallback := holdFallbackHarness(t, time.Hour)
	ack := &progressAck{}
	h.source.settler(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, 1, ack), "acme")(messaging.ErrUnplacedTenant)
	waitHeld(t, gauge, 1)
	holds := h.source.finalHolds
	holds.mu.Lock()
	var entry *finalPlacementHold
	for e := range holds.entries {
		entry = e
	}
	entry.running = true
	holds.mu.Unlock()
	holds.expire(time.Now().Add(2 * time.Hour))
	require.Equal(t, float64(1), testutil.ToFloat64(gauge), "a queued or re-running entry is not expired out from under its re-run")
	require.Zero(t, testutil.ToFloat64(fallback.WithLabelValues("gw-test", "expired")))
	holds.mu.Lock()
	entry.running = false
	holds.mu.Unlock()
	holds.expire(time.Now().Add(2 * time.Hour))
	require.Zero(t, testutil.ToFloat64(gauge))
	require.Equal(t, float64(1), testutil.ToFloat64(fallback.WithLabelValues("gw-test", "expired")))
}
