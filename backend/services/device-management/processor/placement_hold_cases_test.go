// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// A re-send refused again (placement flapped between the check and the publish) is held again,
// not released, and the source is still acked exactly once when the output is finally stored.
func TestAReSendRefusedAgainIsHeldAgain(t *testing.T) {
	var placed atomic.Bool
	var refusals atomic.Int32
	w := newPlacementWriter(func(m messaging.Message) error {
		// Refused on the first attempt and on the first re-send.
		if refusals.Load() < 2 {
			refusals.Add(1)
			return unplaced()
		}
		return nil
	})
	iproc := startHoldProcessor(t, w, func(string) error {
		if !placed.Load() {
			return unplaced()
		}
		return nil
	})
	ack := &beatAck{}
	iproc.OnResolvedEvent(holdSource(7, messaging.MaxDeliver, ack), holdTenant, []EventResolutionResults{gateEvent(0)})
	require.Eventually(t, func() bool { return len(w.ids()) == 1 }, 2*time.Second, time.Millisecond)
	placed.Store(true)
	require.Eventually(t, func() bool { return ack.acks.Load() == 1 }, 2*time.Second, time.Millisecond)
	require.Equal(t, 3, countID(w.ids(), "r:acme:7:0"), "first attempt, refused re-send, stored re-send")
	stopWithin(t, iproc, 5*time.Second)
	require.EqualValues(t, 1, ack.acks.Load())
}

// Only a FINAL delivery is held. An earlier one is left unacked exactly as before: the broker
// redelivers it after AckWait, so the hold neither beats it nor publishes anything again.
func TestANonFinalPlacementRefusalIsLeftForRedelivery(t *testing.T) {
	w := newPlacementWriter(func(messaging.Message) error { return unplaced() })
	iproc := startHoldProcessor(t, w, func(string) error { return nil })
	ack := &beatAck{}
	iproc.OnResolvedEvent(holdSource(3, messaging.MaxDeliver-1, ack), holdTenant, []EventResolutionResults{gateEvent(0)})
	require.Eventually(t, func() bool { return len(w.ids()) == 1 }, 2*time.Second, time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	require.Zero(t, ack.beats.Load(), "a non-final delivery is not held")
	require.Zero(t, ack.acks.Load())
	require.Len(t, w.ids(), 1, "nothing is published again in-process")
	stopWithin(t, iproc, 5*time.Second)
}

// A final delivery with any failure that is not a placement refusal is not held: re-sending would
// not cure it, and it is left to the max-delivery recorder as before.
func TestAFinalDeliveryWithAnotherFailureIsNotHeld(t *testing.T) {
	w := newPlacementWriter(func(m messaging.Message) error {
		if m.DedupID == "r:acme:4:0" {
			return unplaced()
		}
		return errors.New("stream refused the append")
	})
	iproc := startHoldProcessor(t, w, func(string) error { return nil })
	ack := &beatAck{}
	iproc.OnResolvedEvent(holdSource(4, messaging.MaxDeliver, ack), holdTenant,
		[]EventResolutionResults{gateEvent(0), gateEvent(1)})
	require.Eventually(t, func() bool { return len(w.ids()) == 2 }, 2*time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	beats := ack.beats.Load()
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, beats, ack.beats.Load(), "released: no longer beaten")
	require.Zero(t, ack.acks.Load())
	require.Len(t, w.ids(), 2, "nothing is published again")
	stopWithin(t, iproc, 5*time.Second)
}

// A stop while sources are held, or being re-sent, returns promptly, sends on no closed channel,
// and leaves the held sources unacked; the hold's gauge returns to zero.
func TestStopWhileHeldLeavesTheSourceUnacked(t *testing.T) {
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "held"})
	w := newPlacementWriter(func(messaging.Message) error { return unplaced() })
	failed := newPlacementWriter(func(messaging.Message) error { return nil })
	iproc := NewInboundEventsProcessor(&core.Microservice{InstanceId: "inst", FunctionalArea: "device-management"},
		[]messaging.MessageReader{&sliceReader{}}, w, failed, core.NewNoOpLifecycleCallbacks(), nil,
		config.AuthModeOptional, time.Duration(config.DefaultMaxEventFutureSkewSeconds)*time.Second,
		ResolveMetrics{placementHeld: gauge}, WithPlacementCheck(func(string) error { return nil }))
	iproc.placementHoldBeat, iproc.placementHoldPoll = 5*time.Millisecond, time.Millisecond
	require.NoError(t, iproc.Initialize(context.Background()))
	require.NoError(t, iproc.Start(context.Background()))
	acks := make([]*beatAck, 50)
	for i := range acks {
		acks[i] = &beatAck{}
		iproc.OnResolvedEvent(holdSource(uint64(100+i), messaging.MaxDeliver, acks[i]), holdTenant,
			[]EventResolutionResults{gateEvent(i), gateEvent(i + 1)})
	}
	require.Eventually(t, func() bool { return testutil.ToFloat64(gauge) > 0 }, 2*time.Second, time.Millisecond)
	stopWithin(t, iproc, 5*time.Second)
	for i, a := range acks {
		require.Zero(t, a.acks.Load(), "source %d", i)
	}
	require.Zero(t, testutil.ToFloat64(gauge), "the gauge forgets abandoned holds")
}

// The hold is bounded: past placementHoldLimit a source is not held (and stays unacked for the
// max-delivery recorder) rather than grow without limit.
func TestThePlacementHoldIsBounded(t *testing.T) {
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "held"})
	h := startPlacementHolds(func(string) error { return unplaced() }, gauge, nil, time.Hour, time.Hour,
		func(context.Context, resolvedItem) bool { return false })
	defer func() { h.cancel(); <-h.done }()
	coord := func(seq uint64) *ackCoord {
		return &ackCoord{src: holdSource(seq, messaging.MaxDeliver, &beatAck{}), tenant: holdTenant,
			refused: []heldOutput{{index: 0, event: gateEvent(0).Resolved}}}
	}
	for seq := uint64(1); seq <= placementHoldLimit; seq++ {
		require.True(t, h.hold(coord(seq)))
	}
	require.False(t, h.hold(coord(placementHoldLimit+1)), "past the limit")
	require.EqualValues(t, placementHoldLimit, testutil.ToFloat64(gauge))
}

// unplacedAckedWriter is the envelope writer over the fake broker, refusing every publish for
// placement while unplaced is set.
type unplacedAckedWriter struct {
	*fakeAckedWriter
	unplaced *atomic.Bool
}

func (u unplacedAckedWriter) PublishAcked(ctx context.Context, msg messaging.Message, done func(messaging.PubAckInfo, error)) {
	if u.unplaced.Load() {
		done(messaging.PubAckInfo{}, unplaced())
		return
	}
	u.fakeAckedWriter.PublishAcked(ctx, msg, done)
}

type unplacedRounds struct {
	*fakeRounds
	unplaced *atomic.Bool
}

func (u unplacedRounds) Publish(ctx context.Context, msg messaging.Message) (messaging.PubAckInfo, error) {
	if u.unplaced.Load() {
		return messaging.PubAckInfo{}, unplaced()
	}
	return u.fakeRounds.Publish(ctx, msg)
}

// With resolved envelopes on, a final delivery whose round is still refused for placement when the
// walk gives up is held too, and its events go out again as a redelivery of the same bucket: stored
// once, and the source acked once.
func TestAnEnvelopeRoundRefusedForPlacementIsHeldAndWalkedAgain(t *testing.T) {
	var refusing atomic.Bool
	refusing.Store(true)
	base := newFakeAckedWriter()
	w := unplacedAckedWriter{fakeAckedWriter: base, unplaced: &refusing}
	rounds := unplacedRounds{fakeRounds: &fakeRounds{w: base}, unplaced: &refusing}
	failed := newPlacementWriter(func(messaging.Message) error { return nil })
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "held"})
	iproc := NewInboundEventsProcessor(&core.Microservice{InstanceId: "inst", FunctionalArea: "device-management"},
		[]messaging.MessageReader{&sliceReader{}}, w, failed, core.NewNoOpLifecycleCallbacks(), nil,
		config.AuthModeOptional, time.Duration(config.DefaultMaxEventFutureSkewSeconds)*time.Second,
		ResolveMetrics{placementHeld: gauge}, WithResolvedEnvelopes(true, rounds), WithPlacementCheck(func(string) error {
			if refusing.Load() {
				return unplaced()
			}
			return nil
		}))
	iproc.placementHoldBeat, iproc.placementHoldPoll = 5*time.Millisecond, 5*time.Millisecond
	require.NoError(t, iproc.Initialize(context.Background()))
	require.NoError(t, iproc.Start(context.Background()))
	iproc.env.SetGiveUp(20 * time.Millisecond)

	ack := &beatAck{}
	iproc.OnResolvedEvent(holdSource(40, messaging.MaxDeliver, ack), holdTenant,
		[]EventResolutionResults{gateEvent(0), gateEvent(1)})
	require.Eventually(t, func() bool { return testutil.ToFloat64(gauge) == 1 }, 2*time.Second, time.Millisecond,
		"held once the walk gave up")
	require.Zero(t, ack.acks.Load())

	refusing.Store(false)
	require.Eventually(t, func() bool { return ack.acks.Load() == 1 }, 2*time.Second, time.Millisecond)
	base.mu.Lock()
	stored := len(base.stored)
	base.mu.Unlock()
	require.Equal(t, 1, stored, "the two events are stored once, in one round")
	base.mu.Lock()
	var round messaging.Message
	for _, m := range base.bySeq {
		round = m
	}
	base.mu.Unlock()
	r, err := eventenvelope.Parse(round.Value)
	require.NoError(t, err)
	require.Equal(t, 2, r.Len(), "both refused events, each under its own member index")
	require.NotEqual(t, r.Index(0), r.Index(1))
	stopWithin(t, iproc, 5*time.Second)
	require.EqualValues(t, 1, ack.acks.Load())
}

// A final delivery is beaten from the moment its fan-out is handed on, not from the moment its
// last output settles: a publish can wait behind the writer's window, or for an envelope walker,
// past AckWait, and the broker gives up on a final delivery whose AckWait lapses meanwhile.
func TestAFinalDeliveryIsBeatenWhileItsOutputsWait(t *testing.T) {
	release := make(chan struct{})
	w := newPlacementWriter(func(messaging.Message) error {
		<-release
		return nil
	})
	iproc := startHoldProcessor(t, w, func(string) error { return nil })
	ack := &beatAck{}
	iproc.OnResolvedEvent(holdSource(9, messaging.MaxDeliver, ack), holdTenant, []EventResolutionResults{gateEvent(0)})
	require.Eventually(t, func() bool { return ack.beats.Load() >= 3 }, 2*time.Second, time.Millisecond,
		"beaten while its publish waits")
	close(release)
	require.Eventually(t, func() bool { return ack.acks.Load() == 1 }, 2*time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	beats := ack.beats.Load()
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, beats, ack.beats.Load(), "no beat after the ack")
	stopWithin(t, iproc, 5*time.Second)
}

// Every source shape keeps its own identity through a hold: an integration uplink publishes again
// under its own "ru" ids, and a member of an inbound envelope under its member coordinate. A held
// member keeps its PARENT alive (its InProgress renews the envelope) and the envelope is acked only
// once that member's refused output is stored.
func TestHeldSourcesKeepTheirIdentity(t *testing.T) {
	t.Run("uplink", func(t *testing.T) {
		var placed atomic.Bool
		w := newPlacementWriter(func(m messaging.Message) error {
			if !placed.Load() {
				return unplaced()
			}
			return nil
		})
		iproc := startHoldProcessor(t, w, func(string) error {
			if !placed.Load() {
				return unplaced()
			}
			return nil
		})
		ack := &beatAck{}
		src := messaging.NewConsumedMessage(messaging.ScopedSubject("inst", holdTenant, streams.IntegrationUplinks),
			nil, messaging.MaxDeliver, nil, ack)
		src.StreamSeq = 11
		iproc.OnResolvedEvent(src, holdTenant, []EventResolutionResults{gateEvent(0)})
		require.Eventually(t, func() bool { return len(w.ids()) == 1 }, 2*time.Second, time.Millisecond)
		placed.Store(true)
		require.Eventually(t, func() bool { return ack.acks.Load() == 1 }, 2*time.Second, time.Millisecond)
		require.Equal(t, []string{"ru:acme:b:0", "ru:acme:b:0"}, w.ids())
		stopWithin(t, iproc, 5*time.Second)
	})
	t.Run("envelope member", func(t *testing.T) {
		var placed atomic.Bool
		w := newPlacementWriter(func(m messaging.Message) error {
			if !placed.Load() && m.DedupID == "r:acme:c:1.0" {
				return unplaced()
			}
			return nil
		})
		iproc := startHoldProcessor(t, w, func(string) error {
			if !placed.Load() {
				return unplaced()
			}
			return nil
		})
		parent := &beatAck{}
		env := inboundEnvelope(t, 12, parent, messaging.MaxDeliver,
			undecodableInstant(t, "a"), undecodableInstant(t, "b"))
		units, _, err := messaging.SplitEnvelope(env, "tenant1", func(string) {}, nil)
		require.NoError(t, err)
		require.Len(t, units, 2)
		for i, u := range units {
			iproc.OnResolvedEvent(u.Msg, holdTenant, []EventResolutionResults{gateEvent(i)})
		}
		require.Eventually(t, func() bool { return len(w.ids()) == 2 }, 2*time.Second, time.Millisecond)
		// A member's InProgress reaches the broker at most once a second per envelope.
		require.Eventually(t, func() bool { return parent.beats.Load() >= 2 }, 3*time.Second, 10*time.Millisecond,
			"the held member keeps its envelope alive")
		require.Zero(t, parent.acks.Load(), "the envelope waits for its held member")
		placed.Store(true)
		require.Eventually(t, func() bool { return parent.acks.Load() == 1 }, 2*time.Second, time.Millisecond)
		ids := w.ids()
		require.Equal(t, 1, countID(ids, "r:acme:c:0.0"), "the stored member is not published again")
		require.Equal(t, 2, countID(ids, "r:acme:c:1.0"), "the refused member, again under its own id")
		stopWithin(t, iproc, 5*time.Second)
	})
}

// A settle that arrives after the stop began holds nothing (no new work for a run that is
// ending), while a durable one still acks its source.
func TestALateSettleAfterStopHoldsNothing(t *testing.T) {
	h := startPlacementHolds(func(string) error { return nil }, nil, nil, time.Hour, time.Hour,
		func(context.Context, resolvedItem) bool { return true })
	h.cancel()
	<-h.done
	iproc := &InboundEventsProcessor{}
	refusedAck, storedAck := &beatAck{}, &beatAck{}
	refused := &ackCoord{src: holdSource(1, messaging.MaxDeliver, refusedAck), remaining: 1, tenant: holdTenant,
		events: []EventResolutionResults{gateEvent(0)}, holds: h}
	stored := &ackCoord{src: holdSource(2, messaging.MaxDeliver, storedAck), remaining: 1, tenant: holdTenant,
		events: []EventResolutionResults{gateEvent(0)}, holds: h}
	iproc.settleResolved(refused, 0, unplaced())
	iproc.settleResolved(stored, 0, nil)
	require.Empty(t, h.entries)
	require.Zero(t, refusedAck.acks.Load())
	require.Zero(t, refusedAck.beats.Load())
	require.EqualValues(t, 1, storedAck.acks.Load())
}
