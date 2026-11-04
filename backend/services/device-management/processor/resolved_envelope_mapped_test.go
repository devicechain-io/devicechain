// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mappedMembers splits an inbound envelope of n members at seq into the messages the resolvers
// see, one per member.
func mappedMembers(t *testing.T, seq uint64, delivered int, n int) []messaging.Message {
	t.Helper()
	events := make([][]byte, n)
	for i := range events {
		events[i] = undecodableInstant(t, string(rune('a'+i)))
	}
	env := inboundEnvelope(t, seq, &envCountingAck{}, delivered, events...)
	units, _, err := messaging.SplitEnvelope(env, "tenant1", func(string) {}, nil)
	require.NoError(t, err)
	out := make([]messaging.Message, len(units))
	for i, u := range units {
		out[i] = u.Msg
	}
	return out
}

// One inbound envelope's resolved events are ONE resolved envelope, the members in the inbound
// envelope's member order however the resolver pool finished them. A member that failed or was
// split out has no event in it and its index is simply absent. The envelope's id is
// R:<tenant>:<inbound envelope seq>:0, and every member's source is settled once.
func TestAnInboundEnvelopeMapsToOneResolvedEnvelopeInMemberOrder(t *testing.T) {
	h := newEnvHarness(t)
	members := mappedMembers(t, 900, 1, 5)
	for _, i := range []int{4, 0, 2} { // 1 and 3 failed or were split out; the pool finished 4, 0, 2
		h.items = append(h.items, envTestItem(&ackCoord{src: members[i], remaining: 1}, 0, "dev"))
	}
	h.publish()

	require.Len(t, h.w.msgs, 1, "one resolved envelope for the one inbound envelope")
	msg := h.w.msgs[0]
	assert.Equal(t, messaging.EnvelopeDedupID("R", "acme", 900, 0), msg.DedupID)
	r, err := eventenvelope.Parse(msg.Value)
	require.NoError(t, err)
	assert.Equal(t, eventenvelope.KindResolved, r.Header().Kind)
	assert.EqualValues(t, 900, r.Header().SourceSeq)
	assert.Equal(t, []uint32{0 * fanSlots, 2 * fanSlots, 4 * fanSlots}, h.members(1))
	assert.EqualValues(t, 3, h.eng.Members.Load())
}

// A redelivery of the inbound envelope after its resolved envelope was stored (a lost ack)
// publishes nothing new: the duplicate PubAck names the original, which holds every member.
func TestARedeliveredInboundEnvelopeStoresNothingTwice(t *testing.T) {
	first := newEnvHarness(t)
	for _, i := range []int{0, 1, 2} {
		first.items = append(first.items, envTestItem(&ackCoord{src: mappedMembers(t, 901, 1, 3)[i], remaining: 1}, 0, "dev"))
	}
	first.publish()
	require.Len(t, first.w.bySeq, 1)

	second := newEnvHarness(t)
	second.w = first.w
	second.rounds = &fakeRounds{w: first.w}
	second.iproc = &InboundEventsProcessor{ResolvedEventsWriter: first.w}
	second.eng = testEngine(second.iproc, first.w, second.rounds)
	for _, i := range []int{0, 1, 2} {
		second.items = append(second.items, envTestItem(&ackCoord{src: mappedMembers(t, 901, 2, 3)[i], remaining: 1}, 0, "dev"))
	}
	second.publish()

	assert.Len(t, first.w.bySeq, 1, "nothing new stored by the redelivery")
	assert.EqualValues(t, 1, second.eng.Duplicates.Load())
}

// A copy split out of an envelope member arrives with a delivery count of its own (1), long after
// the envelope's rounds were stored and the process's high-water mark moved on. Its event may
// already be in a stored round, so it is treated as redelivered: it walks from round 0, finds its
// index in the round that holds it, and stores NOTHING a second time.
func TestASplitCopyArrivingLateStoresNothingTwice(t *testing.T) {
	h := newEnvHarness(t)
	members := mappedMembers(t, 900, 1, 3)
	// The envelope's own resolve published member 0, then (a later collection) member 1: rounds 0 and 1.
	h.items = append(h.items, envTestItem(&ackCoord{src: members[0], remaining: 1}, 0, "dev"))
	h.publish()
	h.items = append(h.items, envTestItem(&ackCoord{src: members[1], remaining: 1}, 0, "dev"))
	h.publish()
	require.Len(t, h.w.bySeq, 2)

	ack := &envSrcAck{}
	copyMsg := messaging.NewConsumedMessage("i.acme.inbound-events", nil, 1,
		map[string]string{messaging.HeaderRetry: messaging.RetryHeader(900, 0, 0)}, ack)
	copyMsg.StreamSeq = 9
	messaging.ApplyRetryHeader(&copyMsg)
	require.True(t, copyMsg.IsSplitCopy())
	h.items = append(h.items, envTestItem(&ackCoord{src: copyMsg, remaining: 1}, 0, "dev"))
	h.publish()

	assert.Len(t, h.w.bySeq, 2, "the copy's event was already in round 0: nothing new is stored")
	assert.EqualValues(t, 1, ack.n.Load(), "the copy is settled from the stored round")
}

// A DM bucket can need more than 256 rounds (its member index also names a fan-out slot): the
// walk bound is bucketSize x fanSlots, so a redelivery of a bucket stored one event per round
// walks all of it and settles every source.
func TestAWalkOverMoreThan256RoundsSettlesTheWholeBucket(t *testing.T) {
	first := newEnvHarness(t)
	first.iproc.envelopeChunkMax = 1
	for seq := uint64(1); seq <= 5; seq++ {
		first.source(seq, 64) // 320 events, one per round
	}
	first.publish()
	require.Len(t, first.w.bySeq, 320)

	second := &envHarness{t: t, w: first.w, acks: map[uint64]*envSrcAck{}}
	second.iproc = &InboundEventsProcessor{ResolvedEventsWriter: first.w}
	second.rounds = &fakeRounds{w: first.w}
	second.eng = testEngine(second.iproc, first.w, second.rounds)
	for seq := uint64(1); seq <= 5; seq++ {
		ack := &envSrcAck{}
		second.acks[seq] = ack
		src := messaging.NewConsumedMessage("i.acme.inbound-events", nil, 2, nil, ack)
		src.StreamSeq = seq
		coord := &ackCoord{src: src, remaining: 64}
		for f := 0; f < 64; f++ {
			second.items = append(second.items, envTestItem(coord, f, "dev"))
		}
	}
	second.publish()

	assert.Len(t, first.w.bySeq, 320, "the redelivery stored nothing new")
	for seq, a := range second.acks {
		assert.EqualValues(t, 1, a.n.Load(), "source %d settled", seq)
	}
}
