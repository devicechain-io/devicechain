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
