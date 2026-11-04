// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

type countAck struct {
	acks, progress atomic.Int32
}

func (a *countAck) Ack() error        { a.acks.Add(1); return nil }
func (a *countAck) InProgress() error { a.progress.Add(1); return nil }

// event is a PUnresolvedEvent-shaped message: field 3 (the device) and field 7 (the type).
func event(device string, typ uint64) []byte {
	var b []byte
	b = protowire.AppendTag(b, 3, protowire.BytesType)
	b = protowire.AppendString(b, device)
	b = protowire.AppendTag(b, 7, protowire.VarintType)
	return protowire.AppendVarint(b, typ)
}

func envelopeMessage(t *testing.T, ack Acknowledger, events ...[]byte) (Message, [][]byte) {
	t.Helper()
	in := make([]eventenvelope.Event, len(events))
	for i, e := range events {
		in[i] = eventenvelope.Event{Index: uint32(i * 2), Bytes: e}
	}
	body, err := eventenvelope.Encode(eventenvelope.Header{Kind: eventenvelope.KindUnresolved, Tenant: "acme"}, in)
	require.NoError(t, err)
	m := NewConsumedMessage("i.acme.inbound-events", body, 2,
		map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue, HeaderCorrelationID: "cid-1"}, ack)
	m.StreamSeq = 4242
	return m, events
}

func TestALegacyMessageIsNotSplit(t *testing.T) {
	m := NewConsumedMessage("i.acme.inbound-events", event("d", 1), 1, nil, &countAck{})
	units, marked, err := SplitEnvelope(m, "acme", func(string) {})
	assert.NoError(t, err)
	assert.False(t, marked)
	assert.Nil(t, units)
	assert.False(t, m.IsEnvelope())
	_, isMember := m.MemberIndex()
	assert.False(t, isMember)
	assert.Equal(t, "", m.MemberDedupID(DedupKindFailed, "acme"))
}

func TestMembersAreLegacyShapedMessagesAndTheEnvelopeAcksWhenTheLastDoes(t *testing.T) {
	ack := &countAck{}
	env, events := envelopeMessage(t, ack, event("a", 1), event("b", 2), event("c", 3))
	var results []string
	units, marked, err := SplitEnvelope(env, "acme", func(r string) { results = append(results, r) })
	require.NoError(t, err)
	require.True(t, marked)
	require.Len(t, units, 3)

	for i, u := range units {
		assert.Equal(t, events[i], u.Msg.Value, "the member's own legacy bytes")
		assert.Equal(t, env.Subject, u.Msg.Subject)
		assert.Equal(t, 2, u.Msg.NumDelivered, "a member's deliveries are its envelope's")
		assert.Equal(t, uint64(4242), u.Msg.StreamSeq)
		assert.Equal(t, "cid-1", u.Msg.CorrelationID())
		assert.False(t, u.Msg.IsEnvelope(), "a member is not split again")
		idx, ok := u.Msg.MemberIndex()
		assert.True(t, ok)
		assert.Equal(t, uint32(i*2), idx, "the index is the member's place in its source")
	}
	assert.Equal(t, "f:acme:39u:2", units[1].Msg.MemberDedupID(DedupKindFailed, "acme"))

	require.NoError(t, units[0].Msg.Ack())
	require.NoError(t, units[0].Msg.Ack()) // a member acked twice counts once
	require.NoError(t, units[2].Msg.Ack())
	assert.EqualValues(t, 0, ack.acks.Load(), "a member is still outstanding")
	require.NoError(t, units[1].Msg.Ack())
	assert.EqualValues(t, 1, ack.acks.Load(), "the envelope is acked once, by the last member")

	require.NoError(t, units[1].Msg.InProgress())
	assert.EqualValues(t, 1, ack.progress.Load(), "a member held keeps the envelope's ack-wait alive")
}

// The retry disposition is "do not ack": a member that never acks leaves its envelope to be
// redelivered whole, which is the whole point.
func TestAMemberLeftUnackedLeavesTheEnvelopeUnacked(t *testing.T) {
	ack := &countAck{}
	env, _ := envelopeMessage(t, ack, event("a", 1), event("b", 2), event("c", 3))
	units, _, err := SplitEnvelope(env, "acme", func(string) {})
	require.NoError(t, err)
	require.NoError(t, units[0].Msg.Ack())
	require.NoError(t, units[2].Msg.Ack())
	assert.EqualValues(t, 0, ack.acks.Load())
}

func TestTheEnvelopesClockStopsOnceWithTheMostSevereMemberResult(t *testing.T) {
	env, _ := envelopeMessage(t, &countAck{}, event("a", 1), event("b", 2), event("c", 3))
	var results []string
	units, _, err := SplitEnvelope(env, "acme", func(r string) { results = append(results, r) })
	require.NoError(t, err)
	units[0].Done(core.ResultOK)
	units[2].Done(core.ResultInvalid)
	assert.Empty(t, results, "not until the last member")
	units[1].Done(core.ResultRetry)
	assert.Equal(t, []string{core.ResultRetry}, results)
}

func TestAnUnreadableEnvelopeIsAnErrorAndSettlesNothing(t *testing.T) {
	ack := &countAck{}
	env, _ := envelopeMessage(t, ack, event("a", 1), event("b", 2))
	_, marked, err := SplitEnvelope(env, "globex", func(string) { t.Fatal("done") })
	assert.True(t, marked)
	assert.ErrorIs(t, err, eventenvelope.ErrTenant)

	env.Value = append([]byte(nil), env.Value...)
	env.Value[len(env.Value)/2] ^= 1
	_, marked, err = SplitEnvelope(env, "acme", func(string) { t.Fatal("done") })
	assert.True(t, marked)
	assert.ErrorIs(t, err, eventenvelope.ErrCorrupt)
	assert.EqualValues(t, 0, ack.acks.Load(), "disposing of poison is the caller's")
}

func TestAnEnvelopeWithNoMembersIsAckedAtOnce(t *testing.T) {
	ack := &countAck{}
	body, err := eventenvelope.Encode(eventenvelope.Header{Kind: eventenvelope.KindUnresolved, Tenant: "acme", SettledElsewhere: []uint32{0, 1}}, nil)
	require.NoError(t, err)
	m := NewConsumedMessage("i.acme.inbound-events", body, 1, map[string]string{eventenvelope.HeaderName: "1"}, ack)
	var results []string
	units, marked, err := SplitEnvelope(m, "acme", func(r string) { results = append(results, r) })
	require.NoError(t, err)
	assert.True(t, marked)
	assert.Empty(t, units)
	assert.EqualValues(t, 1, ack.acks.Load())
	assert.Equal(t, []string{core.ResultOK}, results)
}

func TestRetryCarryIsBounded(t *testing.T) {
	assert.Equal(t, 0, retryCarry(nil))
	assert.Equal(t, 3, retryCarry(map[string]string{HeaderRetry: "3"}))
	assert.Equal(t, 0, retryCarry(map[string]string{HeaderRetry: "-1"}), "not a count")
	assert.Equal(t, 0, retryCarry(map[string]string{HeaderRetry: "x"}))
	assert.Equal(t, 0, retryCarry(map[string]string{HeaderRetry: "999"}), "more than the budget is not believed")
	assert.Equal(t, MaxDeliver, retryCarry(map[string]string{HeaderRetry: "5"}))
}
