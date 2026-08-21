// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func testMetrics() Metrics {
	return Metrics{
		Emitted: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "emitted"}, []string{"state"}),
		Refused: prometheus.NewCounter(prometheus.CounterOpts{Name: "refused"}),
		Failed:  prometheus.NewCounter(prometheus.CounterOpts{Name: "failed"}),
		RegressedSessions: prometheus.NewCounter(
			prometheus.CounterOpts{Name: "regressed"}),
	}
}

func aDemotion(session uint64) Demotion {
	return Demotion{
		Tenant: "acme", DeviceToken: "dev-1",
		Event: adapter.DemotionEvent{
			SessionId: session, OccurredAt: time.Unix(1_700_000_000, 0), Reason: "tap disabled",
		},
	}
}

// TestADemotionGoesThroughTheSameAdmissionGate is the reason ApplyDemotion lives on the
// Publisher instead of beside it. A source going away is not a licence to write into a
// tenant being erased, and a fleet-wide release — a demoter walks EVERY asserted row a
// source owns — is exactly the shape the per-tenant ceiling exists to meter. A second
// emit path with its own gate call is how a repair becomes a way around the refusal.
func TestADemotionGoesThroughTheSameAdmissionGate(t *testing.T) {
	e := newRecordingEmitter()
	m := testMetrics()
	p := NewPublisher("mqtt1", e, func(string, string, time.Time, bool) bool { return false }, m)

	require.False(t, p.ApplyDemotion(context.Background(), aDemotion(100)),
		"a refused demotion must report that it did not reach the stream")
	require.Empty(t, e.all(), "a refused demotion still reached the emitter")
	require.Equal(t, 1.0, testutil.ToFloat64(m.Refused))

	// The counterweight: an admitted demotion does reach the stream, and is counted under
	// its OWN metric label rather than forced into a connectivity bucket.
	p = NewPublisher("mqtt1", e, func(string, string, time.Time, bool) bool { return true }, m)
	require.True(t, p.ApplyDemotion(context.Background(), aDemotion(100)))
	rec := e.all()
	require.Len(t, rec, 1)
	require.NotNil(t, rec[0].Demotion, "the emission was recorded as a connectivity claim")
	require.Equal(t, uint64(100), rec[0].Demotion.SessionId)
	require.Equal(t, "mqtt1", rec[0].Source)
	require.Equal(t, 1.0, testutil.ToFloat64(m.Emitted.WithLabelValues(labelDemoted)))
	require.Equal(t, 0.0, testutil.ToFloat64(m.Emitted.WithLabelValues(labelDisconnected)),
		"a demotion was metered as a disconnect")
}

// TestADemotionIsMeteredAsLiveTraffic pins the gate's clock argument. A demotion carries
// the releasing clock's time, but the gate's backlog bucket accrues from the last
// timestamp it saw, so feeding it any event-time at all re-accrues to burst on the next
// forward jump. Every emit path passes the zero time so each bucket stays on one clock.
func TestADemotionIsMeteredAsLiveTraffic(t *testing.T) {
	var sawSentAt time.Time
	var sawRedelivery bool
	var sawSource, sawTenant string
	p := NewPublisher("mqtt1", newRecordingEmitter(),
		func(source, tenant string, sentAt time.Time, redelivery bool) bool {
			sawSource, sawTenant, sawSentAt, sawRedelivery = source, tenant, sentAt, redelivery
			return true
		}, testMetrics())

	require.True(t, p.ApplyDemotion(context.Background(), aDemotion(100)))
	require.True(t, sawSentAt.IsZero(), "the demotion was metered at its own event time: %v", sawSentAt)
	require.False(t, sawRedelivery)
	require.Equal(t, "mqtt1", sawSource)
	require.Equal(t, "acme", sawTenant)
}

// TestADemotionDoesNotWakeCommands: the wake exists to release a returning device's
// withheld commands, and a demotion is not a return — it is the platform admitting it no
// longer knows. Waking here would put commands in front of a dispatcher on the strength
// of an administrative event, and a command is a physical actuation. What the demotion
// does instead is better: returning the row to INFERRED lifts the delivery hold outright,
// because the hold is keyed on ASSERTED-and-not-active.
func TestADemotionDoesNotWakeCommands(t *testing.T) {
	waker := &spyWaker{}
	e := newRecordingEmitter()
	p := NewPublisher("mqtt1", e, nil, testMetrics())
	p.waker = waker

	require.True(t, p.ApplyDemotion(context.Background(), aDemotion(100)))
	require.Empty(t, waker.seen(), "a demotion released withheld commands")

	// The counterweight: the waker IS wired, so a connect on the same publisher wakes.
	require.True(t, p.Apply(context.Background(), Transition{
		Tenant: "acme", DeviceToken: "dev-1",
		Event: adapter.PresenceEvent{Connected: true, SessionId: 200, OccurredAt: time.Unix(1_700_000_001, 0)},
	}))
	require.Len(t, waker.seen(), 1, "the waker was not actually attached; the assertion above proves nothing")
}

// TestADemotionEmitFailureIsCountedNotSwallowed. A demotion that never reaches the stream
// leaves the device's row asserted and frozen, which is the exact condition the demotion
// exists to end — so the caller has to learn it failed and try again on its next pass.
func TestADemotionEmitFailureIsCountedNotSwallowed(t *testing.T) {
	m := testMetrics()
	p := NewPublisher("mqtt1", failingEmitter{errors.New("stream unavailable")}, nil, m)

	require.False(t, p.ApplyDemotion(context.Background(), aDemotion(100)))
	require.Equal(t, 1.0, testutil.ToFloat64(m.Failed))
	require.Equal(t, 0.0, testutil.ToFloat64(m.Emitted.WithLabelValues(labelDemoted)),
		"a failed emission was counted as emitted")
}

// TestADemotionDoesNotDisturbTheSessionWatermark. RegressedSessions exists to surface
// clock skew BETWEEN BROKER NODES minting connect epochs. A demotion re-states a session
// the projection already holds rather than minting one, so feeding it to the watermark
// would count "regressions" that say nothing about any broker's clock — noise in the one
// counter whose whole value is that it is quiet.
func TestADemotionDoesNotDisturbTheSessionWatermark(t *testing.T) {
	m := testMetrics()
	p := NewPublisher("mqtt1", newRecordingEmitter(), nil, m)

	require.True(t, p.Apply(context.Background(), Transition{
		Tenant: "acme", DeviceToken: "dev-1",
		Event: adapter.PresenceEvent{Connected: true, SessionId: 100, OccurredAt: time.Unix(1_700_000_000, 0)},
	}))
	require.Equal(t, 0.0, testutil.ToFloat64(m.RegressedSessions))

	// A demotion naming a LOWER session than the watermark. Fed to the detector this reads
	// as a regression; it is not one.
	require.True(t, p.ApplyDemotion(context.Background(), aDemotion(50)))
	require.Equal(t, 0.0, testutil.ToFloat64(m.RegressedSessions),
		"a demotion was counted as a broker clock regression")

	// The counterweight: a genuine lower-session CONNECT still counts.
	require.True(t, p.Apply(context.Background(), Transition{
		Tenant: "acme", DeviceToken: "dev-1",
		Event: adapter.PresenceEvent{Connected: true, SessionId: 50, OccurredAt: time.Unix(1_700_000_002, 0)},
	}))
	require.Equal(t, 1.0, testutil.ToFloat64(m.RegressedSessions),
		"the regression detector is not wired at all; the assertion above proves nothing")
}
