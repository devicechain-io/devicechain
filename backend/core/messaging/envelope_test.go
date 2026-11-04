// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"sync/atomic"
	"testing"
	"time"

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

func noop(string) {}

func TestALegacyMessageIsNotSplit(t *testing.T) {
	m := NewConsumedMessage("i.acme.inbound-events", event("d", 1), 1, nil, &countAck{})
	units, marked, err := SplitEnvelope(m, "acme", noop, nil)
	assert.NoError(t, err)
	assert.False(t, marked)
	assert.Nil(t, units)
	assert.False(t, m.IsEnvelope())
	_, isMember := m.MemberIndex()
	assert.False(t, isMember)
	assert.Equal(t, "", m.MemberDedupID(DedupKindFailed, "acme"))
	_, _, ok := m.SourceCoordinate()
	assert.False(t, ok)
}

func TestOnlyThisFormatsMarkerMakesAnEnvelope(t *testing.T) {
	env, _ := envelopeMessage(t, &countAck{}, event("a", 1), event("b", 2))
	assert.True(t, env.IsEnvelope())
	env.Headers = map[string]string{eventenvelope.HeaderName: "2"}
	assert.False(t, env.IsEnvelope(), "another version's marker is not this format")
}

func TestMembersAreLegacyShapedMessagesAndTheEnvelopeAcksWhenTheLastDoes(t *testing.T) {
	ack := &countAck{}
	env, events := envelopeMessage(t, ack, event("a", 1), event("b", 2), event("c", 3))
	units, marked, err := SplitEnvelope(env, "acme", noop, nil)
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
		seq, midx, ok := u.Msg.SourceCoordinate()
		assert.True(t, ok)
		assert.Equal(t, uint64(4242), seq)
		assert.Equal(t, idx, midx)
	}
	assert.Equal(t, "fe:acme:39u:2", units[1].Msg.MemberDedupID(DedupKindFailed, "acme"))
	assert.Equal(t, "x:acme:39u:2", units[1].Msg.MemberDedupID(DedupKindSplit, "acme"))

	require.NoError(t, units[0].Msg.Ack())
	require.NoError(t, units[0].Msg.Ack()) // a member acked twice counts once
	require.NoError(t, units[2].Msg.Ack())
	assert.EqualValues(t, 0, ack.acks.Load(), "a member is still outstanding")
	require.NoError(t, units[1].Msg.Ack())
	assert.EqualValues(t, 1, ack.acks.Load(), "the envelope is acked once, by the last member")
}

// The retry disposition is "do not ack": a member that never acks leaves its envelope to be
// redelivered whole, which is the whole point.
func TestAMemberLeftUnackedLeavesTheEnvelopeUnacked(t *testing.T) {
	ack := &countAck{}
	env, _ := envelopeMessage(t, ack, event("a", 1), event("b", 2), event("c", 3))
	units, _, err := SplitEnvelope(env, "acme", noop, nil)
	require.NoError(t, err)
	require.NoError(t, units[0].Msg.Ack())
	require.NoError(t, units[2].Msg.Ack())
	assert.EqualValues(t, 0, ack.acks.Load())
}

// Every member stops a clock of its own with its own result: the first the envelope's receipt
// clock, the others one started for each. A poison member is counted invalid and a dropped one
// dropped, whatever the others did.
func TestEveryMemberStopsAClockOfItsOwn(t *testing.T) {
	env, _ := envelopeMessage(t, &countAck{}, event("a", 1), event("b", 2), event("c", 3))
	var received, started []string
	units, _, err := SplitEnvelope(env, "acme",
		func(r string) { received = append(received, r) },
		func() func(string) { return func(r string) { started = append(started, r) } })
	require.NoError(t, err)
	units[2].Done(core.ResultDropped)
	units[0].Done(core.ResultInvalid)
	units[1].Done(core.ResultOK)
	assert.Equal(t, []string{core.ResultInvalid}, received, "the receipt clock is the first member's")
	assert.ElementsMatch(t, []string{core.ResultDropped, core.ResultOK}, started, "the others each had their own")
}

// The broker is beaten once per envelope, not once per held member.
func TestAHeldEnvelopeIsBeatenOncePerInterval(t *testing.T) {
	ack := &countAck{}
	env, _ := envelopeMessage(t, ack, event("a", 1), event("b", 2), event("c", 3))
	units, _, err := SplitEnvelope(env, "acme", noop, nil)
	require.NoError(t, err)
	for _, u := range units {
		require.NoError(t, u.Msg.InProgress())
	}
	assert.EqualValues(t, 1, ack.progress.Load(), "three held members, one beat")
	time.Sleep(beatInterval + 50*time.Millisecond)
	require.NoError(t, units[1].Msg.InProgress())
	assert.EqualValues(t, 2, ack.progress.Load(), "and the next interval's beat still goes out")
}

func TestAnUnreadableEnvelopeIsAnErrorAndSettlesNothing(t *testing.T) {
	ack := &countAck{}
	env, _ := envelopeMessage(t, ack, event("a", 1), event("b", 2))
	_, marked, err := SplitEnvelope(env, "globex", func(string) { t.Fatal("done") }, nil)
	assert.True(t, marked)
	assert.ErrorIs(t, err, eventenvelope.ErrTenant)

	env.Value = append([]byte(nil), env.Value...)
	env.Value[len(env.Value)/2] ^= 1
	_, marked, err = SplitEnvelope(env, "acme", func(string) { t.Fatal("done") }, nil)
	assert.True(t, marked)
	assert.ErrorIs(t, err, eventenvelope.ErrCorrupt)
	assert.EqualValues(t, 0, ack.acks.Load(), "disposing of poison is the caller's")
}

func TestAnEnvelopeWithNoMembersIsAckedAtOnce(t *testing.T) {
	ack := &countAck{}
	body, err := eventenvelope.Encode(eventenvelope.Header{Kind: eventenvelope.KindUnresolved, Tenant: "acme", SettledElsewhere: []uint32{0, 1}}, nil)
	require.NoError(t, err)
	m := NewConsumedMessage("i.acme.inbound-events", body, 1,
		map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}, ack)
	var results []string
	units, marked, err := SplitEnvelope(m, "acme", func(r string) { results = append(results, r) }, nil)
	require.NoError(t, err)
	assert.True(t, marked)
	assert.Empty(t, units)
	assert.EqualValues(t, 1, ack.acks.Load())
	assert.Equal(t, []string{core.ResultOK}, results)
}

func TestTheRetryHeaderIsParsedStrictly(t *testing.T) {
	h := RetryHeader(4242, 2, 1)
	assert.Equal(t, "39u:2:1", h)
	o, attempts, ok := parseRetryHeader(map[string]string{HeaderRetry: h})
	require.True(t, ok)
	assert.Equal(t, splitOrigin{seq: 4242, idx: 2}, o)
	assert.Equal(t, 1, attempts)
	for _, bad := range []string{"", "3", "39u:2", "39u:2:x", "39u:2:-1", "39u:2:6", "0:2:1", "39u:99999999:1", "39u:2:1:9", "zz!:2:1"} {
		_, _, ok := parseRetryHeader(map[string]string{HeaderRetry: bad})
		assert.False(t, ok, "%q is not believed", bad)
	}
	_, _, ok = parseRetryHeader(nil)
	assert.False(t, ok)
}

// A copy split out of an envelope is read back with the deliveries it has used and the
// coordinate it came from, and is named by that coordinate, not by its own sequence.
func TestASplitCopyKeepsItsOriginAndItsAttempts(t *testing.T) {
	m := NewConsumedMessage("i.acme.inbound-events", event("a", 1), 1,
		map[string]string{HeaderRetry: RetryHeader(4242, 2, 2)}, &countAck{})
	m.StreamSeq = 9000
	ApplyRetryHeader(&m)
	assert.Equal(t, 3, m.NumDelivered, "its first delivery on top of the two already used")
	seq, idx, ok := m.SourceCoordinate()
	require.True(t, ok)
	assert.Equal(t, uint64(4242), seq)
	assert.Equal(t, uint32(2), idx)
	_, isMember := m.MemberIndex()
	assert.False(t, isMember, "a copy is an ordinary message: it is never split out again")

	plain := NewConsumedMessage("i.acme.inbound-events", event("a", 1), 1, nil, nil)
	ApplyRetryHeader(&plain)
	assert.Equal(t, 1, plain.NumDelivered)
	_, _, ok = plain.SourceCoordinate()
	assert.False(t, ok)
}
