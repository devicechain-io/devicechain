// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Envelope ids are held to the stream's duplicate window: a bucket spans bucketSeconds of the
// broker's append time, a source is republished for at most the redelivery span after that, and
// the sum must fit the window or a late redelivery walks to a round whose id has expired and
// stores its event again.
func TestEnvelopeIdsFitTheDuplicateWindow(t *testing.T) {
	republishSpan := (streams.ConsumerMaxDeliver-1)*2*streams.ConsumerAckWaitSeconds + streams.ConsumerAckWaitSeconds
	assert.LessOrEqual(t, bucketSeconds+republishSpan, streams.RedeliveryDuplicateWindowSeconds,
		"a bucket's time span plus a source's republish span must fit the inbound/resolved duplicate window")
	assert.LessOrEqual(t, bucketSeconds, 30)
}

// Two sources in the same sequence range but far apart in append time are different buckets; a
// redelivery of one (same append time) is the same bucket.
func TestABucketIsBoundedInTimeAsWellAsSequence(t *testing.T) {
	key := func(seq uint64, at time.Time) bucketKey {
		src, _ := envTestSource(seq)
		src.AppendTime = at
		k, _, ok := memberIndex(envTestItem(&ackCoord{src: src, remaining: 1}, 0, "d"), 100)
		require.True(t, ok)
		return k
	}
	t0 := time.Unix(1_700_000_010, 0)
	a := key(10, t0)
	assert.Equal(t, a, key(10, t0), "a redelivery is in the same bucket")
	assert.Equal(t, a, key(11, t0.Add(5*time.Second)))
	assert.NotEqual(t, a, key(11, t0.Add(bucketSeconds*time.Second)), "a source a bucket-span later is another bucket")
	assert.NotEqual(t, a.Source(), key(11, t0.Add(2*bucketSeconds*time.Second)).Source())
}

// A round the broker rejects while the pod is alive is retried under the SAME id, so no hole stays
// below a stored round; a redelivery of the straddling source then stores nothing new.
func TestARejectedRoundIsRetriedUnderTheSameIdAndLeavesNoHole(t *testing.T) {
	h := newEnvHarness(t)
	h.iproc.envelopeChunkMax = 1 // one event per round: a source's two events straddle rounds 0 and 1
	h.eng.SetGiveUp(5 * time.Second)
	h.w.rejectOnce = map[string]bool{gid("acme", 0, 0): true}
	h.source(7, 2)
	h.publish()

	require.Len(t, h.w.bySeq, 2, "both rounds stored: the rejected one by its retry")
	ids := map[string]bool{}
	for _, m := range h.w.bySeq {
		ids[m.DedupID] = true
	}
	assert.Equal(t, map[string]bool{gid("acme", 0, 0): true, gid("acme", 0, 1): true}, ids, "no round was skipped")
	assert.EqualValues(t, 1, h.eng.Retries.Load()+0, "one rejection retried")
	assert.EqualValues(t, 1, h.acks[7].n.Load(), "the source is acked once, after both events are stored")

	// The source comes back (delivery 2): everything is stored already.
	src, ack := envTestSource(7)
	src.NumDelivered = 2
	coord := &ackCoord{src: src, remaining: 2}
	h.items = append(h.items, envTestItem(coord, 0, "dev-7"), envTestItem(coord, 1, "dev-7"))
	h.publish()
	assert.Len(t, h.w.bySeq, 2, "no duplicate")
	assert.EqualValues(t, 1, ack.n.Load())
}

// Everything a process learns about a bucket is shared: a duplicate answer raises the high-water
// mark, so the next chunk goes to a fresh round without probing upward, and a round read once is
// not read again.
func TestTheHighWaterMarkAndCacheAreSharedAcrossChunks(t *testing.T) {
	first := newEnvHarness(t)
	first.source(1, 1)
	first.source(2, 1)
	first.publish() // round 0 stored by "another pod"

	h := &envHarness{t: t, w: first.w, acks: map[uint64]*envSrcAck{}}
	h.iproc = &InboundEventsProcessor{ResolvedEventsWriter: first.w}
	h.rounds = &fakeRounds{w: first.w}
	h.eng = testEngine(h.iproc, first.w, h.rounds)
	h.source(3, 1)
	h.publish() // round 0 is a duplicate; 3 goes to round 1
	h.source(4, 1)
	h.source(5, 1)
	h.publish() // goes straight to round 2
	h.source(6, 1)
	h.publish() // and round 3

	var ids []string
	for _, m := range first.w.msgs {
		ids = append(ids, m.DedupID)
	}
	assert.Equal(t, []string{gid("acme", 0, 0), gid("acme", 0, 0), gid("acme", 0, 1), gid("acme", 0, 2), gid("acme", 0, 3)}, ids,
		"after the one duplicate every chunk takes a fresh round: the probes are O(1) per bucket, not per chunk")
	assert.EqualValues(t, 1, h.rounds.gets.Load(), "round 0 was read back once")
	assert.EqualValues(t, 1, h.eng.Duplicates.Load())
}

// A round a redelivered chunk publishes raises the mark too, so the next fresh chunk does not
// start at the same round and probe upward.
func TestARedeliveredChunkRaisesTheMarkForTheNextFreshChunk(t *testing.T) {
	h := newEnvHarness(t)
	src, _ := envTestSource(1)
	src.NumDelivered = 2
	h.acks[1] = &envSrcAck{}
	h.items = append(h.items, envTestItem(&ackCoord{src: src, remaining: 1}, 0, "dev-1"))
	h.publish() // redelivered: round 0
	h.source(2, 1)
	h.publish() // fresh: must be round 1, not a duplicate of round 0
	assert.Equal(t, []string{gid("acme", 0, 0), gid("acme", 0, 1)}, []string{h.w.msgs[0].DedupID, h.w.msgs[1].DedupID})
	assert.EqualValues(t, 0, h.eng.Duplicates.Load())
}

// A round read back once is cached: later redelivered chunks settle against it with neither a
// read nor a publish.
func TestAStoredRoundIsReadOnceAndThenSkipped(t *testing.T) {
	first := newEnvHarness(t)
	first.source(1, 1)
	first.source(2, 1)
	first.publish() // round 0 holds 1 and 2
	stored := len(first.w.msgs)

	h := &envHarness{t: t, w: first.w, acks: map[uint64]*envSrcAck{}}
	h.iproc = &InboundEventsProcessor{ResolvedEventsWriter: first.w}
	h.rounds = &fakeRounds{w: first.w}
	h.eng = testEngine(h.iproc, first.w, h.rounds)
	for _, seq := range []uint64{1, 2} {
		src, ack := envTestSource(seq)
		src.NumDelivered = 2
		h.acks[seq] = ack
		h.items = append(h.items, envTestItem(&ackCoord{src: src, remaining: 1}, 0, fmt.Sprintf("dev-%d", seq)))
		h.publish() // one redelivered source at a time
	}
	assert.EqualValues(t, 1, h.rounds.gets.Load(), "round 0 was read once")
	assert.Equal(t, stored+1, len(first.w.msgs), "only the first redelivery published (to find the duplicate); the second settled from the cache")
	assert.EqualValues(t, 1, h.acks[1].n.Load())
	assert.EqualValues(t, 1, h.acks[2].n.Load())
}

// A walk tells the broker its sources are still being worked on.
func TestAWalkKeepsItsSourcesAlive(t *testing.T) {
	first := newEnvHarness(t)
	first.source(1, 1)
	first.publish()
	h := &envHarness{t: t, w: first.w, acks: map[uint64]*envSrcAck{}}
	h.iproc = &InboundEventsProcessor{ResolvedEventsWriter: first.w}
	h.rounds = &fakeRounds{w: first.w}
	h.eng = testEngine(h.iproc, first.w, h.rounds)
	h.eng.SetBeatEvery(0)
	beats := &progAck{}
	src := messaging.NewConsumedMessage("i.acme.inbound-events", nil, 2, nil, beats)
	src.StreamSeq = 9
	coord := &ackCoord{src: src, remaining: 1}
	h.items = append(h.items, envTestItem(coord, 0, "dev-9"))
	// Source 1 is stored in round 0; 9 is new but redelivered: it walks from round 0.
	h.source(1, 1)
	h.publish()
	assert.Greater(t, beats.progress.Load(), int32(0), "InProgress was sent during the walk")
	assert.EqualValues(t, 1, beats.acks.Load())
}

type progAck struct{ acks, progress atomic.Int32 }

func (a *progAck) Ack() error        { a.acks.Add(1); return nil }
func (a *progAck) InProgress() error { a.progress.Add(1); return nil }

// The encoder's refusals never fall back to per-event ids: a chunk it refuses is split, down to
// single members published legacy-shaped under the group id.
func TestEncoderRefusalsSplitTheChunkAndNeverUseEventIds(t *testing.T) {
	check := func(t *testing.T, h *envHarness, wantMsgs int) {
		t.Helper()
		assert.Empty(t, h.w.legacy, "nothing went out under a per-event id")
		assert.GreaterOrEqual(t, len(h.w.msgs), wantMsgs)
		for _, m := range h.w.msgs {
			assert.True(t, strings.HasPrefix(m.DedupID, "G:"), "id %q", m.DedupID)
		}
		for seq, a := range h.acks {
			assert.EqualValues(t, 1, a.n.Load(), "source %d acked once", seq)
		}
	}
	t.Run("a token that is not UTF-8 never reaches the envelope path", func(t *testing.T) {
		h := newEnvHarness(t)
		h.source(1, 1)
		h.source(2, 1)
		h.source(3, 1)
		h.items[1].event.DeviceTypeToken = "bad\xff\xfe"
		h.publish()
		assert.Empty(t, h.w.legacy, "nothing went out under a per-event id")
		assert.EqualValues(t, 1, h.acks[1].n.Load())
		assert.EqualValues(t, 0, h.acks[2].n.Load(), "the event the marshaller refuses is left unacknowledged, as with the flag off")
		assert.EqualValues(t, 1, h.acks[3].n.Load())
	})
	t.Run("a record size the raw-size sum does not see", func(t *testing.T) {
		h := newEnvHarness(t)
		for seq := uint64(1); seq <= 4; seq++ {
			h.source(seq, 1)
		}
		// Four events whose raw bytes sum under the cap, and whose member records do not.
		for i := range h.items {
			// Nothing for the envelope to take out, so its records are larger than the events.
			h.items[i].event.DeviceTypeToken, h.items[i].event.ProfileVersionToken = "", ""
			h.items[i].event.OccurredTime, h.items[i].event.ProcessedTime = time.Time{}, time.Time{}
			sizeEventTo(t, &h.items[i], 16380)
		}
		h.publish()
		check(t, h, 2)
	})
	t.Run("the same source queued twice is one member", func(t *testing.T) {
		h := newEnvHarness(t)
		h.source(1, 1)
		h.source(2, 1)
		// The redelivery of source 2 is queued beside its original: the same index.
		src, ack := envTestSource(2)
		src.NumDelivered = 2
		h.acks[200] = ack
		h.items = append(h.items, envTestItem(&ackCoord{src: src, remaining: 1}, 0, "dev-2"))
		h.publish()
		check(t, h, 1)
		assert.EqualValues(t, 3-1, h.eng.Members.Load(), "two members, not three")
	})
}

// sizeEventTo grows the item's payload until its marshalled event is size bytes (or just under).
func sizeEventTo(t *testing.T, it *resolvedItem, size int) {
	t.Helper()
	pad := size - 200
	for i := 0; i < 8; i++ {
		p := it.event.Payload.(*resolvedevent.ResolvedMeasurementsPayload)
		p.Entries[0].Entries[0].Value = strings.Repeat("x", max(pad, 1))
		b, err := resolvedevent.MarshalResolvedEvent(&it.event)
		require.NoError(t, err)
		if len(b) == size {
			return
		}
		pad += size - len(b)
	}
	b, _ := resolvedevent.MarshalResolvedEvent(&it.event)
	require.LessOrEqual(t, len(b), size, fmt.Sprintf("could not size the event to %d", size))
}
