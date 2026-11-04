// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	gproto "google.golang.org/protobuf/proto"
)

// An inbound event envelope is resolved member by member. Each member is a message of its own
// to the resolver pool, its outcome is its own, and the envelope is acked when every member
// is.

type envCountingAck struct{ n atomic.Int32 }

func (a *envCountingAck) Ack() error { a.n.Add(1); return nil }

// inboundEnvelope packs the events (the bytes of PUnresolvedEvents) into one envelope message
// at stream sequence seq.
func inboundEnvelope(t *testing.T, seq uint64, ack messaging.Acknowledger, delivered int, events ...[]byte) messaging.Message {
	t.Helper()
	members := make([]eventenvelope.Event, len(events))
	for i, b := range events {
		members[i] = eventenvelope.Event{Index: uint32(i), Bytes: b}
	}
	body, err := eventenvelope.Encode(eventenvelope.Header{Kind: eventenvelope.KindUnresolved, Tenant: "tenant1"}, members)
	require.NoError(t, err)
	m := messaging.NewConsumedMessage(testTenantSubject, body, delivered,
		map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}, ack)
	m.StreamSeq = seq
	return m
}

// undecodableInstant is a well-formed PUnresolvedEvent that does not decode into an event: its
// instant is not a time.
func undecodableInstant(t *testing.T, device string) []byte {
	t.Helper()
	b, err := gproto.Marshal(&esproto.PUnresolvedEvent{Device: device, EventType: int64(eventtype.Location), OccurredTime: "not a time " + device})
	require.NoError(t, err)
	_, err = esproto.UnmarshalUnresolvedEvent(b)
	require.Error(t, err, "the fixture must not decode")
	return b
}

// Members that cannot be decoded each get a failed record of their own, under the id
// f:<tenant>:<envelope seq>:<member index>, and the envelope is acked once, after both records
// are stored.
func (suite *InboundEventsProcessorTestSuite) TestEachUndecodableMemberIsRecordedUnderItsOwnId() {
	ack := &envCountingAck{}
	env := inboundEnvelope(suite.T(), 4242, ack, 1, undecodableInstant(suite.T(), "a"), undecodableInstant(suite.T(), "b"))
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(env, nil).Once()

	readAndHandleOne(suite.IP, context.Background())
	first, second := awaitFailed(suite), awaitFailed(suite)

	ids := map[string]bool{}
	for _, item := range []failedItem{first, second} {
		_, ok := item.src.MemberIndex()
		require.True(suite.T(), ok, "the record's source is a member of the envelope")
		ids[sourceDedupID(dedupKindFailed, item.tenant, item.src, 0)] = true
		assert.Equal(suite.T(), "tenant1", item.tenant)

	}
	assert.Equal(suite.T(), map[string]bool{"f:tenant1:39u:0": true, "f:tenant1:39u:1": true}, ids)

	assert.EqualValues(suite.T(), 0, ack.n.Load(), "the envelope is not acked before its records are stored")
	require.NoError(suite.T(), first.src.Ack())
	assert.EqualValues(suite.T(), 0, ack.n.Load(), "nor while one member is still outstanding")
	require.NoError(suite.T(), second.src.Ack())
	assert.EqualValues(suite.T(), 1, ack.n.Load(), "it is acked once, when the last member is settled")
	require.NoError(suite.T(), second.src.Ack())
	assert.EqualValues(suite.T(), 1, ack.n.Load(), "and a member acked twice does not ack it twice")
}

// An envelope that cannot be read at all is one failed record, located by its stream sequence
// as an undecodable message is, and acked once the record is stored.
func (suite *InboundEventsProcessorTestSuite) TestAnUnreadableEnvelopeIsRecordedOnceByLocator() {
	ack := &envCountingAck{}
	env := inboundEnvelope(suite.T(), 7, ack, 1, undecodableInstant(suite.T(), "a"), undecodableInstant(suite.T(), "b"))
	env.Value = append([]byte(nil), env.Value...)
	env.Value[len(env.Value)/2] ^= 0xff
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(env, nil).Once()

	readAndHandleOne(suite.IP, context.Background())
	item := awaitFailed(suite)
	_, member := item.src.MemberIndex()
	assert.False(suite.T(), member, "the record is about the envelope, not a member")
	assert.Contains(suite.T(), item.event.Message, "stream sequence 7")
	require.NoError(suite.T(), item.src.Ack())
	assert.EqualValues(suite.T(), 1, ack.n.Load())
}

// The pool end to end: members for devices that are not registered yet are split out, each
// published alone with the deliveries spent, and the envelope is acked once the last copy is
// stored - without a failed record and without waiting out the retry ladder as a whole. With no
// split writer the same members leave the envelope unacknowledged, to be redelivered whole.
func (suite *InboundEventsProcessorTestSuite) TestRetryingMembersAreSplitOutOfTheirEnvelope() {
	suite.API.Mock.On("DevicesByToken").Return([]*dmodel.Device{}, nil)
	member := func(device string) []byte {
		ev := buildLocationsEvent()
		ev.Device = device
		b, err := esproto.MarshalUnresolvedEvent(ev)
		require.NoError(suite.T(), err)
		return b
	}

	w := &captureWriter{}
	suite.IP.splitWriter = w
	ack := &envCountingAck{}
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(
		inboundEnvelope(suite.T(), 4242, ack, 1, member("MISSING-A"), member("MISSING-B")), nil).Once()
	readAndHandleOne(suite.IP, context.Background())

	require.Eventually(suite.T(), func() bool { return ack.n.Load() == 1 }, 10*time.Second, 5*time.Millisecond,
		"the envelope is acked once both members are split out")
	w.mu.Lock()
	defer w.mu.Unlock()
	require.Len(suite.T(), w.msgs, 2)
	ids := []string{w.msgs[0].DedupID, w.msgs[1].DedupID}
	assert.ElementsMatch(suite.T(), []string{"x:tenant1:39u:0", "x:tenant1:39u:1"}, ids)
	for _, m := range w.msgs {
		assert.Regexp(suite.T(), `^39u:[01]:0$`, m.Headers[messaging.HeaderRetry])
		assert.False(suite.T(), m.IsEnvelope())
	}
	select {
	case item := <-suite.IP.failed:
		suite.T().Fatalf("a retrying member must not be dead-lettered: %+v", item.event)
	default:
	}
}

// A brown-out of the API is every member's, not any one member's: it is not split into k copies
// and k synchronous publishes. The envelope is held (members unacked) and comes back whole, as a
// message in the same brown-out does. And an envelope on a LATER delivery never splits, so a
// member whose device is still unknown does not get a second copy.
//
// Two top-level tests, not subtests: SetupTest runs per test, and a held envelope keeps its
// resolvers calling the API mock, so re-arming one shared mock between cases is a data race.
func (suite *InboundEventsProcessorTestSuite) TestABrownOutDoesNotSplit() {
	suite.API.Mock.On("DevicesByToken").Return(([]*dmodel.Device)(nil), errors.New("db unreachable"))
	suite.assertEnvelopeHeldWhole(1)
}

func (suite *InboundEventsProcessorTestSuite) TestALaterDeliveryDoesNotSplit() {
	suite.API.Mock.On("DevicesByToken").Return([]*dmodel.Device{}, nil)
	suite.assertEnvelopeHeldWhole(2)
}

func (suite *InboundEventsProcessorTestSuite) assertEnvelopeHeldWhole(delivered int) {
	member := func(device string) []byte {
		ev := buildLocationsEvent()
		ev.Device = device
		b, err := esproto.MarshalUnresolvedEvent(ev)
		require.NoError(suite.T(), err)
		return b
	}
	w := &captureWriter{}
	suite.IP.splitWriter = w
	ack := &envCountingAck{}
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(
		inboundEnvelope(suite.T(), 4242, ack, delivered, member("MISSING-A"), member("MISSING-B")), nil).Once()
	readAndHandleOne(suite.IP, context.Background())
	time.Sleep(300 * time.Millisecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	assert.Empty(suite.T(), w.msgs, "nothing is split out")
	assert.EqualValues(suite.T(), 0, ack.n.Load(), "the envelope stays unacked")
}

func (suite *InboundEventsProcessorTestSuite) TestWithoutASplitWriterARetryingMemberHoldsItsEnvelope() {
	suite.API.Mock.On("DevicesByToken").Return([]*dmodel.Device{}, nil)
	ev := buildLocationsEvent()
	ev.Device = "MISSING-A"
	b, err := esproto.MarshalUnresolvedEvent(ev)
	require.NoError(suite.T(), err)
	ack := &envCountingAck{}
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(
		inboundEnvelope(suite.T(), 4242, ack, 1, b, b[:len(b):len(b)]), nil).Once()
	readAndHandleOne(suite.IP, context.Background())
	time.Sleep(200 * time.Millisecond)
	assert.EqualValues(suite.T(), 0, ack.n.Load(), "a member left to retry leaves the envelope unacked")
}

type captureWriter struct {
	mu   sync.Mutex
	msgs []messaging.Message
	err  error
}

func (w *captureWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	w.msgs = append(w.msgs, msgs...)
	return nil
}
func (w *captureWriter) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}
func (w *captureWriter) HandleResponse(error) {}

// A member that must retry is split out: published alone, in the legacy shape, with the
// member's own bytes, the envelope's correlation id, the id x:<tenant>:<seq>:<index> and the
// deliveries already spent. A refused publish reports false so the caller leaves the envelope
// to redeliver instead.
// A member that must retry is split out: published alone, in the legacy shape, with the
// member's own bytes, the envelope's correlation id, the id x:<tenant>:<seq>:<index> and
// Dc-Retry, its origin coordinate and the deliveries it used BEFORE the copy's first (none: the
// copy's first delivery is the same attempt). A refused publish reports false so the caller
// leaves the envelope to redeliver instead. And only on the envelope's FIRST delivery: a later
// one never splits, so a member can never have two copies.
func TestAMemberThatMustRetryIsSplitOutAlone(t *testing.T) {
	ack := &envCountingAck{}
	env := inboundEnvelope(t, 4242, ack, 1, []byte{0x1a, 0x01, 'a'}, []byte{0x1a, 0x01, 'b'})
	units, marked, err := messaging.SplitEnvelope(env, "tenant1", func(string) {}, nil)
	require.NoError(t, err)
	require.True(t, marked)
	require.Len(t, units, 2)

	w := &captureWriter{}
	iproc := &InboundEventsProcessor{splitWriter: w}
	require.True(t, iproc.splitOut(units[1].Msg, "tenant1"))
	require.Len(t, w.msgs, 1)
	got := w.msgs[0]
	assert.Equal(t, units[1].Msg.Value, got.Value, "the member's own bytes, a legacy event")
	assert.False(t, got.IsEnvelope(), "published in the legacy shape")
	assert.Equal(t, "x:tenant1:39u:1", got.DedupID)
	assert.Equal(t, "39u:1:0", got.Headers[messaging.HeaderRetry], "origin coordinate, no attempts used yet")

	w.err = errors.New("broker refused")
	assert.False(t, iproc.splitOut(units[0].Msg, "tenant1"), "a refused copy leaves the member to retry in its envelope")
	assert.False(t, (&InboundEventsProcessor{}).splitOut(units[0].Msg, "tenant1"), "no writer, no split")

	// A redelivery of the envelope does not split again.
	again := inboundEnvelope(t, 4242, &envCountingAck{}, 2, []byte{0x1a, 0x01, 'a'}, []byte{0x1a, 0x01, 'b'})
	redelivered, _, err := messaging.SplitEnvelope(again, "tenant1", func(string) {}, nil)
	require.NoError(t, err)
	ok := &captureWriter{}
	assert.False(t, (&InboundEventsProcessor{splitWriter: ok}).splitOut(redelivered[0].Msg, "tenant1"))
	assert.Empty(t, ok.msgs, "no second copy of a member, however long its envelope is held")
}

// The event a split copy produces is named by the coordinate it came from, so the copy and its
// envelope - which may both resolve the member - publish under ONE id and the broker's window
// folds the second into the first: the member reaches DETECT once.
func TestACopyAndItsEnvelopeNameTheSameEventAlike(t *testing.T) {
	env := inboundEnvelope(t, 4242, &envCountingAck{}, 1, []byte{0x1a, 0x01, 'a'}, []byte{0x1a, 0x01, 'b'})
	units, _, err := messaging.SplitEnvelope(env, "tenant1", func(string) {}, nil)
	require.NoError(t, err)
	member := units[1].Msg

	w := &captureWriter{}
	require.True(t, (&InboundEventsProcessor{splitWriter: w}).splitOut(member, "tenant1"))
	copyMsg := messaging.NewConsumedMessage(member.Subject, w.msgs[0].Value, 1, w.msgs[0].Headers, nil)
	copyMsg.StreamSeq = 987654 // the copy's own, unrelated sequence
	messaging.ApplyRetryHeader(&copyMsg)

	for _, kind := range []string{dedupKindResolved, dedupKindFailed} {
		for position := 0; position < 3; position++ {
			assert.Equal(t, sourceDedupID(kind, "tenant1", member, position), sourceDedupID(kind, "tenant1", copyMsg, position),
				"kind %s position %d", kind, position)
		}
	}
	assert.Equal(t, "r:tenant1:39u:1.2", sourceDedupID(dedupKindResolved, "tenant1", copyMsg, 2))
	assert.Equal(t, "f:tenant1:39u:1", sourceDedupID(dedupKindFailed, "tenant1", copyMsg, 0))
	// An ordinary message is still named by its own sequence.
	plain := messaging.Message{StreamSeq: 987654}
	assert.Equal(t, "r:tenant1:l62u:0", sourceDedupID(dedupKindResolved, "tenant1", plain, 0))
}

// The failed-events stream holds device-management's records and event-management's, numbered
// from two unrelated sequence spaces. No id of one may equal an id of the other, or the window
// would drop a real record as a duplicate of a stranger's.
func TestFailedRecordIdsOfTheTwoAreasNeverCollide(t *testing.T) {
	dm := map[string]bool{}
	em := map[string]bool{}
	for _, seq := range []uint64{1, 2, 36, 4242, 987654} {
		for idx := uint32(0); idx < 70; idx++ {
			src := messaging.Message{StreamSeq: seq}
			dm[sourceDedupID(dedupKindFailed, "tenant1", src, int(idx))] = true
			units, _, err := messaging.SplitEnvelope(inboundEnvelope(t, seq, nil, 1, []byte{0x1a, 0x01, 'a'}, []byte{0x1a, 0x01, 'b'}), "tenant1", func(string) {}, nil)
			require.NoError(t, err)
			dm[sourceDedupID(dedupKindFailed, "tenant1", units[1].Msg, 0)] = true
			// event-management builds its member records with MemberDedupID on a resolved member
			// whose index is idx.
			em[unitWithIndex(t, seq, idx).MemberDedupID(messaging.DedupKindFailed, "tenant1")] = true
		}
	}
	for id := range em {
		assert.False(t, dm[id], "%q is both a device-management and an event-management failed-record id", id)
	}
}

// unitWithIndex is a resolved-stream member at (seq, idx), as event-management reads one.
func unitWithIndex(t *testing.T, seq uint64, idx uint32) messaging.Message {
	t.Helper()
	events := make([]eventenvelope.Event, 0, 2)
	events = append(events, eventenvelope.Event{Index: idx, Bytes: []byte{0x1a, 0x01, 'a'}}, eventenvelope.Event{Index: idx + 1, Bytes: []byte{0x1a, 0x01, 'b'}})
	body, err := eventenvelope.Encode(eventenvelope.Header{Kind: eventenvelope.KindResolved, Tenant: "tenant1"}, events)
	require.NoError(t, err)
	m := messaging.NewConsumedMessage(testTenantSubject, body, 1,
		map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}, nil)
	m.StreamSeq = seq
	units, _, err := messaging.SplitEnvelope(m, "tenant1", func(string) {}, nil)
	require.NoError(t, err)
	return units[0].Msg
}
