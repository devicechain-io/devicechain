// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

func storeEnvSource(t *testing.T, b *envBroker, on bool) *GatewayJetStreamSource {
	t.Helper()
	es := NewGatewayJetStreamSource(nil, "wal", NewJsonDecoder(map[string]string{}), func(string, []byte) {},
		func(_ string, tenant string, event *model.UnresolvedEvent, _ interface{}, id string) (context.Context, messaging.Message, bool) {
			return core.WithTenant(context.Background(), tenant), messaging.Message{Value: pbDevice(event.Device), DedupID: id}, true
		}, func(string, string, []byte, error) error { return nil }, nil, admitAllReadings, nil)
	es.UseMessageStore()
	es.SetWriter(envWriter{b})
	if on {
		require.NoError(t, es.UseEnvelopes(CaptureEnvelopes{Rounds: envRounds{b}, AllowMessageStore: true}))
	}
	return es
}

func storeCaptured(seq uint64, delivery int, epoch, at time.Time, a messaging.Acknowledger) messaging.Message {
	m := envCaptured("acme", seq, delivery, at, a)
	m.DedupID = messaging.MqttMessageStoreDedupID("acme", epoch, seq)
	return m
}

// Construct a closed batch deterministically, independent of the former's learned rate.
func storeBatch(t *testing.T, es *GatewayJetStreamSource, epoch, at time.Time, delivery int, seqs ...uint64) (messaging.FormedBatch, []*recordingAck) {
	t.Helper()
	b := messaging.FormedBatch{Tenant: "acme"}
	var acks []*recordingAck
	for _, seq := range seqs {
		a := &recordingAck{}
		m := storeCaptured(seq, delivery, epoch, at, a)
		key, ok := es.envelopeKey("acme", m)
		require.True(t, ok)
		_, pos := splitPosition("acme", positionToken(m.DedupID, key, delivery > 1, 0))
		require.NotNil(t, pos)
		p := pendingPublish{ctx: core.WithTenant(context.Background(), "acme"),
			msg: messaging.Message{Value: pbDevice(fmt.Sprintf("acme-%d", seq))}, pos: pos, done: es.settler(m, "acme")}
		b.Members = append(b.Members, messaging.FormedMember{Bytes: p.msg.Value, Token: p})
		acks = append(acks, a)
	}
	return b, acks
}

type delayedStoreWriter struct {
	envWriter
	msg  messaging.Message
	ctx  context.Context
	done func(messaging.PubAckInfo, error)
}

func (w *delayedStoreWriter) PublishAcked(ctx context.Context, msg messaging.Message, done func(messaging.PubAckInfo, error)) {
	w.ctx, w.msg, w.done = ctx, msg, done
}

func TestStoreEnvelopeAcksEveryMemberOnlyAfterDurableStore(t *testing.T) {
	b := newEnvBroker()
	es := storeEnvSource(t, b, true)
	w := &delayedStoreWriter{envWriter: envWriter{b}}
	es.SetWriter(w)
	ce, err := es.newCaptureEngine()
	require.NoError(t, err)
	defer ce.cancel()
	batch, acks := storeBatch(t, es, midBucket().Add(-time.Hour), midBucket(), 1, 1, 2, 3)
	es.publishGroup(ce, batch)
	require.Equal(t, eventenvelope.HeaderValue, w.msg.Headers[eventenvelope.HeaderName])
	for _, a := range acks {
		require.Zero(t, a.counts())
	}
	require.Empty(t, b.devices(t))
	ack, err := b.store(w.ctx, w.msg)
	require.NoError(t, err)
	w.done(ack, nil)
	for _, a := range acks {
		require.Equal(t, 1, a.counts())
	}
	require.Equal(t, []string{"acme-1", "acme-2", "acme-3"}, b.devices(t))
}

func TestFailedStoreEnvelopeAcksNothingEvenOnFinalDelivery(t *testing.T) {
	b := newEnvBroker()
	b.rejectAll.Store(true)
	es := storeEnvSource(t, b, true)
	ce, err := es.newCaptureEngine()
	require.NoError(t, err)
	defer ce.cancel()
	ce.engine.SetGiveUp(time.Millisecond)
	// A failed envelope is an infrastructure failure, never a poison member to ack away.
	es.poison = &poisonRoutes{slots: make(chan struct{}, POISON_ROUTE_LIMIT)}
	var poison atomic.Int32
	es.failed = func(string, string, []byte, error) error { poison.Add(1); return nil }
	batch, acks := storeBatch(t, es, midBucket().Add(-time.Hour), midBucket(), messaging.MaxDeliver, 1, 2, 3)
	es.publishGroup(ce, batch)
	ce.engine.Wait()
	es.poison.running.Wait()
	for _, a := range acks {
		require.Zero(t, a.counts())
	}
	require.Zero(t, poison.Load())
	require.Empty(t, b.devices(t))
}

func TestStoreEnvelopeCrashAndPartialRedeliveryDoNotDuplicateMembers(t *testing.T) {
	b := newEnvBroker()
	epoch, at := midBucket().Add(-time.Hour), midBucket()
	first := storeEnvSource(t, b, true)
	ce, err := first.newCaptureEngine()
	require.NoError(t, err)
	batch, acks := storeBatch(t, first, epoch, at, 1, 1, 2, 3)
	// Store the first envelope but lose ALL WAL acks, as on a process crash.
	for i := range batch.Members {
		p := batch.Members[i].Token.(pendingPublish)
		p.done = func(error) {}
		batch.Members[i].Token = p
	}
	first.publishGroup(ce, batch)
	ce.engine.Wait()
	ce.cancel()
	for _, a := range acks {
		require.Zero(t, a.counts())
	}

	second := storeEnvSource(t, b, true)
	ce2, err := second.newCaptureEngine()
	require.NoError(t, err)
	defer ce2.cancel()
	// Different fetch boundary: overlap + a previously unpublished member.
	batch, again := storeBatch(t, second, epoch, at, 2, 2, 3, 4)
	second.publishGroup(ce2, batch)
	ce2.engine.Wait()
	for _, a := range again {
		require.Equal(t, 1, a.counts())
	}
	batch, again = storeBatch(t, second, epoch, at, 2, 1, 4)
	second.publishGroup(ce2, batch)
	ce2.engine.Wait()
	for _, a := range again {
		require.Equal(t, 1, a.counts())
	}
	require.Equal(t, []string{"acme-1", "acme-2", "acme-3", "acme-4"}, b.devices(t))

	// Recreating the WAL can reuse sequence AND append-time bucket, never the epoch.
	batch, again = storeBatch(t, second, epoch.Add(time.Nanosecond), at, 1, 1, 2, 3)
	second.publishGroup(ce2, batch)
	ce2.engine.Wait()
	for _, a := range again {
		require.Equal(t, 1, a.counts())
	}
	require.Len(t, b.devices(t), 7, "a new WAL incarnation must not be deduped against the old one")
}

func TestStoreEnvelopePipelineAndFlagOff(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprint(on), func(t *testing.T) {
			b := newEnvBroker()
			es := storeEnvSource(t, b, on)
			es.startPipeline(1)
			t.Cleanup(func() { require.True(t, es.stopPipeline(context.Background())) })
			epoch, at := midBucket().Add(-time.Hour), midBucket()
			var acks []*recordingAck
			for seq := uint64(1); seq <= 60; seq++ {
				a := &recordingAck{}
				acks = append(acks, a)
				es.handle(storeCaptured(seq, 1, epoch, at, a))
			}
			for _, a := range acks {
				require.True(t, a.settled(t))
			}
			require.Len(t, b.devices(t), 60)
			b.mu.Lock()
			defer b.mu.Unlock()
			if on {
				require.Less(t, len(b.order), 60, "WAL source did not batch")
			} else {
				require.Len(t, b.order, 60)
				for i, st := range b.order {
					require.Equal(t, messaging.MqttMessageStoreDedupID("acme", epoch, uint64(i+1)), st.msg.DedupID)
					require.Empty(t, st.msg.Headers[eventenvelope.HeaderName])
				}
			}
		})
	}
}

func TestStoreEnvelopeRefusesWeakIdentity(t *testing.T) {
	es := storeEnvSource(t, newEnvBroker(), true)
	epoch, at := midBucket().Add(-time.Hour), midBucket()
	for _, id := range []string{"", "acme.m0.1", "other.m123.1", "acme.m123.2", "acme.1"} {
		m := storeCaptured(1, 1, epoch, at, &recordingAck{})
		m.DedupID = id
		_, ok := es.envelopeKey("acme", m)
		require.False(t, ok, id)
	}
	// A capture identity cannot accidentally occupy a WAL round.
	m := storeCaptured(1, 1, epoch, at, &recordingAck{})
	k, ok := es.envelopeKey("acme", m)
	require.True(t, ok)
	require.EqualValues(t, epoch.UnixNano(), k.SourceEpoch)
	// Capture sequence 22 is "m" in base 36; it is not a store epoch prefix.
	key, _, _ := messaging.RoundKeyOf("acme", 22, at)
	_, pos := splitPosition("acme", positionToken(DedupID("acme", 22), key, false, 0))
	require.NotNil(t, pos)
	require.Zero(t, pos.key.SourceEpoch)
}

type placementStoreWriter struct {
	envWriter
	blocked *atomic.Bool
}

func (w placementStoreWriter) PublishAcked(ctx context.Context, msg messaging.Message, done func(messaging.PubAckInfo, error)) {
	if w.blocked.Load() {
		done(messaging.PubAckInfo{}, messaging.ErrUnplacedTenant)
		return
	}
	w.envWriter.PublishAcked(ctx, msg, done)
}

type placementStoreRounds struct {
	envRounds
	blocked *atomic.Bool
}

func (r placementStoreRounds) Publish(ctx context.Context, msg messaging.Message) (messaging.PubAckInfo, error) {
	if r.blocked.Load() {
		return messaging.PubAckInfo{}, messaging.ErrUnplacedTenant
	}
	return r.envRounds.Publish(ctx, msg)
}

func TestStoreEnvelopeFinalPlacementHoldBeatsAndRecovers(t *testing.T) {
	b := newEnvBroker()
	es := storeEnvSource(t, b, true)
	blocked := &atomic.Bool{}
	blocked.Store(true)
	es.SetWriter(placementStoreWriter{envWriter{b}, blocked})
	require.NoError(t, es.UseEnvelopes(CaptureEnvelopes{AllowMessageStore: true, Rounds: placementStoreRounds{envRounds{b}, blocked}}))
	es.UsePlacementCheck(func(string) error {
		if blocked.Load() {
			return messaging.ErrUnplacedTenant
		}
		return nil
	})
	es.finalHoldBeat, es.finalHoldPoll = time.Millisecond, time.Millisecond
	es.startPipeline(1)
	es.ce.engine.SetGiveUp(time.Millisecond)
	t.Cleanup(func() { require.True(t, es.stopPipeline(context.Background())) })
	a := &beatAck{}
	es.handle(storeCaptured(1, messaging.MaxDeliver, midBucket().Add(-time.Hour), midBucket(), a))
	held := func() int {
		es.finalHolds.mu.Lock()
		defer es.finalHolds.mu.Unlock()
		return len(es.finalHolds.entries)
	}
	require.Eventually(t, func() bool { return held() == 1 }, 2*time.Second, time.Millisecond)
	require.Zero(t, a.counts())
	require.Eventually(t, func() bool { return a.progress.Load() > 2 }, time.Second, time.Millisecond)
	blocked.Store(false)
	require.True(t, a.settled(t))
	require.Eventually(t, func() bool { return held() == 0 }, time.Second, time.Millisecond)
	require.Equal(t, []string{"acme-1"}, b.devices(t))
}

// Readers with envelope production disabled still consume both wire shapes. Pre-v1.1
// readers cannot participate; the existing inbound switch must stay off for that rollout.
func TestStoreEnvelopeAndSingleEventsShareTheInboundConsumer(t *testing.T) {
	b := newEnvBroker()
	es := storeEnvSource(t, b, true)
	ce, err := es.newCaptureEngine()
	require.NoError(t, err)
	defer ce.cancel()
	batch, _ := storeBatch(t, es, midBucket().Add(-time.Hour), midBucket(), 1, 1, 2, 3)
	es.publishGroup(ce, batch)
	ce.engine.Wait()
	_, err = b.store(core.WithTenant(context.Background(), "acme"), messaging.Message{Value: pbDevice("legacy")})
	require.NoError(t, err)
	var devices []string
	for _, st := range b.order {
		a := &recordingAck{}
		msg := messaging.NewConsumedMessage("inst-1.acme.inbound-events", st.msg.Value, 1, st.msg.Headers, a)
		units, marked, err := messaging.SplitEnvelope(msg, "acme", nil, nil)
		require.NoError(t, err)
		if !marked {
			devices = append(devices, string(msg.Value[2:]))
			require.NoError(t, msg.Ack())
		} else {
			for i, unit := range units {
				require.Zero(t, a.counts(), "inbound envelope acked before all members settled")
				devices = append(devices, string(unit.Msg.Value[2:]))
				require.NoError(t, unit.Msg.Ack())
				if i < len(units)-1 {
					require.Zero(t, a.counts())
				}
			}
		}
		require.Equal(t, 1, a.counts())
	}
	require.Equal(t, []string{"acme-1", "acme-2", "acme-3", "legacy"}, devices)
}

// A round that has already stored some members must not ack its missing complement on failure.
func TestStorePartialRecoveryFailureOnlyAcksStoredMembers(t *testing.T) {
	b := newEnvBroker()
	es := storeEnvSource(t, b, true)
	ce, err := es.newCaptureEngine()
	require.NoError(t, err)
	defer ce.cancel()
	epoch, at := midBucket().Add(-time.Hour), midBucket()
	batch, _ := storeBatch(t, es, epoch, at, 1, 1, 2)
	es.publishGroup(ce, batch)
	ce.engine.Wait()

	next := storeEnvSource(t, b, true)
	ce2, err := next.newCaptureEngine()
	require.NoError(t, err)
	defer ce2.cancel()
	ce2.engine.SetGiveUp(time.Millisecond)
	batch, acks := storeBatch(t, next, epoch, at, 2, 2, 3)
	p := batch.Members[0].Token.(pendingPublish)
	// Round zero is present; reject only the complement's round one.
	id := "W:acme:" + strconv.FormatUint(p.pos.key.SourceEpoch, 36) + ":" + strconv.FormatUint(p.pos.key.Source(), 36) + ":1"
	b.failN[id] = 1 << 20
	next.publishGroup(ce2, batch)
	ce2.engine.Wait()
	require.Equal(t, 1, acks[0].counts())
	require.Zero(t, acks[1].counts())
	require.Equal(t, []string{"acme-1", "acme-2"}, b.devices(t))
}
