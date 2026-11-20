// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func newFallbackCounter() *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fallback"}, []string{"reason"})
}

func heldCoord(seq uint64, ack *beatAck) *ackCoord {
	return &ackCoord{src: holdSource(seq, messaging.MaxDeliver, ack), tenant: holdTenant,
		refused: []heldOutput{{index: 0, event: gateEvent(0).Resolved}}}
}

// A tenant whose placement never answers must not keep its final deliveries forever: they
// would be beaten without end and fill the hold every other tenant needs. Past the maximum age
// (from the first hold) a source is dropped unacked, for the max-delivery recorder, and counted.
func TestAHeldSourceExpiresAfterTheMaximumAge(t *testing.T) {
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "held"})
	fallback := newFallbackCounter()
	h := startPlacementHolds(placementHoldConfig{check: func(string) error { return unplaced() },
		gauge: gauge, fallback: fallback, beat: time.Millisecond, poll: time.Millisecond, maxAge: 50 * time.Millisecond},
		func(context.Context, resolvedItem) bool { return true })
	defer func() { h.cancel(); <-h.done }()
	ack := &beatAck{}
	c := heldCoord(1, ack)
	c.mu.Lock()
	require.True(t, h.hold(c))
	c.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	require.EqualValues(t, 1, testutil.ToFloat64(gauge), "still held before the maximum age")
	require.Eventually(t, func() bool { return testutil.ToFloat64(gauge) == 0 }, 2*time.Second, time.Millisecond)
	require.EqualValues(t, 1, testutil.ToFloat64(fallback.WithLabelValues(placementFallbackExpired)))
	h.mu.Lock()
	require.Empty(t, h.entries, "its slot is free for other tenants")
	h.mu.Unlock()
	time.Sleep(10 * time.Millisecond)
	beats := ack.beats.Load()
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, beats, ack.beats.Load(), "no longer beaten")
	require.Zero(t, ack.acks.Load(), "left unacked for the max-delivery recorder")
}

// The age runs from the FIRST hold: a source held again after a refused re-send is not renewed.
func TestTheMaximumAgeRunsFromTheFirstHold(t *testing.T) {
	h := startPlacementHolds(placementHoldConfig{check: func(string) error { return unplaced() },
		beat: time.Hour, poll: time.Hour, maxAge: time.Minute},
		func(context.Context, resolvedItem) bool { return true })
	defer func() { h.cancel(); <-h.done }()
	c := heldCoord(1, &beatAck{})
	require.True(t, h.hold(c))
	h.mu.Lock()
	first := h.entries[c].since
	h.entries[c].idle = false // re-sent
	h.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	require.True(t, h.hold(c), "refused again")
	h.mu.Lock()
	defer h.mu.Unlock()
	require.Equal(t, first, h.entries[c].since)
}

// A source whose outputs are queued or being re-sent is not expired: its re-send is in flight,
// and the hold stays its heartbeat owner until it settles or is held (idle) again.
func TestABusyHoldIsNotExpiredUntilIdle(t *testing.T) {
	sending, release := make(chan struct{}), make(chan struct{})
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "held"})
	fallback := newFallbackCounter()
	h := startPlacementHolds(placementHoldConfig{check: func(string) error { return nil },
		gauge: gauge, fallback: fallback, beat: time.Millisecond, poll: time.Millisecond, maxAge: 20 * time.Millisecond},
		func(ctx context.Context, _ resolvedItem) bool {
			close(sending)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return true
		})
	defer func() { h.cancel(); <-h.done }()
	c := heldCoord(1, &beatAck{})
	require.True(t, h.hold(c))
	<-sending
	time.Sleep(60 * time.Millisecond) // well past the maximum age
	require.EqualValues(t, 1, testutil.ToFloat64(gauge), "a re-send in flight keeps its hold")
	require.Zero(t, testutil.ToFloat64(fallback.WithLabelValues(placementFallbackExpired)))
	close(release)
	// Refused again: idle and past the age, so it expires on the next poll.
	require.True(t, h.hold(c))
	require.Eventually(t, func() bool { return testutil.ToFloat64(gauge) == 0 }, 2*time.Second, time.Millisecond)
	require.EqualValues(t, 1, testutil.ToFloat64(fallback.WithLabelValues(placementFallbackExpired)))
}

// A full hold counts every source it turns away, by reason.
func TestAFullHoldCountsWhatItTurnsAway(t *testing.T) {
	fallback := newFallbackCounter()
	h := startPlacementHolds(placementHoldConfig{check: func(string) error { return unplaced() },
		fallback: fallback, beat: time.Hour, poll: time.Hour},
		func(context.Context, resolvedItem) bool { return true })
	defer func() { h.cancel(); <-h.done }()
	for seq := uint64(1); seq <= placementHoldLimit; seq++ {
		require.True(t, h.hold(heldCoord(seq, &beatAck{})))
	}
	for seq := uint64(0); seq < 3; seq++ {
		require.False(t, h.hold(heldCoord(placementHoldLimit+1+seq, &beatAck{})))
	}
	require.EqualValues(t, 3, testutil.ToFloat64(fallback.WithLabelValues(placementFallbackFull)))
	h.fullLog.mu.Lock()
	defer h.fullLog.mu.Unlock()
	require.Equal(t, 2, h.fullLog.suppressed, "one logged, the rest held back for the next")
}

// With no hold wired the error is rate-limited too.
func TestTheUnwiredHoldErrorIsRateLimited(t *testing.T) {
	unwiredLog.mu.Lock()
	unwiredLog.last, unwiredLog.suppressed = time.Time{}, 0
	unwiredLog.mu.Unlock()
	var h *placementHolds
	for seq := uint64(1); seq <= 3; seq++ {
		require.False(t, h.hold(heldCoord(seq, &beatAck{})))
	}
	unwiredLog.mu.Lock()
	defer unwiredLog.mu.Unlock()
	require.Equal(t, 2, unwiredLog.suppressed)
}

// A held source whose re-send fails for a reason other than placement is released and counted.
func TestAReleasedHoldIsCounted(t *testing.T) {
	var refusals atomic.Int32
	w := newPlacementWriter(func(messaging.Message) error {
		if refusals.Add(1) == 1 {
			return unplaced()
		}
		return errors.New("stream refused the append")
	})
	fallback := newFallbackCounter()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "held"})
	iproc := startHoldProcessorWith(t, w, func(string) error { return nil },
		ResolveMetrics{placementHeld: gauge, placementFallback: fallback})
	ack := &beatAck{}
	iproc.OnResolvedEvent(holdSource(5, messaging.MaxDeliver, ack), holdTenant, []EventResolutionResults{gateEvent(0)})
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(fallback.WithLabelValues(placementFallbackReleased)) == 1
	}, 2*time.Second, time.Millisecond)
	require.Zero(t, testutil.ToFloat64(gauge))
	require.Zero(t, ack.acks.Load())
	stopWithin(t, iproc, 5*time.Second)
}

// A stop counts every source it abandons while held.
func TestAShutdownCountsTheSourcesItAbandons(t *testing.T) {
	w := newPlacementWriter(func(messaging.Message) error { return unplaced() })
	fallback := newFallbackCounter()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "held"})
	iproc := startHoldProcessorWith(t, w, func(string) error { return unplaced() },
		ResolveMetrics{placementHeld: gauge, placementFallback: fallback})
	for seq := uint64(1); seq <= 3; seq++ {
		iproc.OnResolvedEvent(holdSource(seq, messaging.MaxDeliver, &beatAck{}), holdTenant, []EventResolutionResults{gateEvent(0)})
	}
	require.Eventually(t, func() bool { return testutil.ToFloat64(gauge) == 3 }, 2*time.Second, time.Millisecond)
	stopWithin(t, iproc, 5*time.Second)
	require.EqualValues(t, 3, testutil.ToFloat64(fallback.WithLabelValues(placementFallbackShutdown)))
	require.Zero(t, testutil.ToFloat64(gauge))
}

// The "hold is full" (and "no hold wired") errors are logged at most once a minute; what is held
// back is counted and reported with the next one.
func TestTheHoldsErrorLogsAreRateLimited(t *testing.T) {
	var l logLimiter
	start := time.Unix(1700000000, 0)
	ok, suppressed := l.allow(start)
	require.True(t, ok)
	require.Zero(t, suppressed)
	for i := 1; i <= 5; i++ {
		ok, _ = l.allow(start.Add(time.Duration(i) * time.Second))
		require.False(t, ok, "within the minute")
	}
	ok, suppressed = l.allow(start.Add(time.Minute))
	require.True(t, ok, "a minute later")
	require.Equal(t, 5, suppressed)
	ok, suppressed = l.allow(start.Add(2 * time.Minute))
	require.True(t, ok)
	require.Zero(t, suppressed, "the count restarts")
}
