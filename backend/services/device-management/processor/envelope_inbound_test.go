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
		inboundEnvelope(suite.T(), 4242, ack, 2, member("MISSING-A"), member("MISSING-B")), nil).Once()
	readAndHandleOne(suite.IP, context.Background())

	require.Eventually(suite.T(), func() bool { return ack.n.Load() == 1 }, 10*time.Second, 5*time.Millisecond,
		"the envelope is acked once both members are split out")
	w.mu.Lock()
	defer w.mu.Unlock()
	require.Len(suite.T(), w.msgs, 2)
	ids := []string{w.msgs[0].DedupID, w.msgs[1].DedupID}
	assert.ElementsMatch(suite.T(), []string{"x:tenant1:39u:0", "x:tenant1:39u:1"}, ids)
	for _, m := range w.msgs {
		assert.Equal(suite.T(), "2", m.Headers[messaging.HeaderRetry])
		assert.False(suite.T(), m.IsEnvelope())
	}
	select {
	case item := <-suite.IP.failed:
		suite.T().Fatalf("a retrying member must not be dead-lettered: %+v", item.event)
	default:
	}
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
func TestAMemberThatMustRetryIsSplitOutAlone(t *testing.T) {
	ack := &envCountingAck{}
	env := inboundEnvelope(t, 4242, ack, 3, []byte{0x1a, 0x01, 'a'}, []byte{0x1a, 0x01, 'b'})
	units, marked, err := messaging.SplitEnvelope(env, "tenant1", func(string) {})
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
	assert.Equal(t, "3", got.Headers[messaging.HeaderRetry], "the deliveries already spent travel with it")

	w.err = errors.New("broker refused")
	assert.False(t, iproc.splitOut(units[0].Msg, "tenant1"), "a refused copy leaves the member to retry in its envelope")
	assert.False(t, (&InboundEventsProcessor{}).splitOut(units[0].Msg, "tenant1"), "no writer, no split")
}
