// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	gproto "google.golang.org/protobuf/proto"
)

// undecodableStreamSeq is the position the fixture message claims to occupy on the
// inbound-events stream. A distinctive value, because the assertion that the record
// carries it has to be able to tell the sequence apart from the byte length and the
// delivery count that sit beside it in the same string.
const undecodableStreamSeq = uint64(90210)

// undecodableMessage is a message off the inbound-events stream whose body will not
// unmarshal, carrying the suite's credential markers in the bytes.
//
// The markers are not there for realism — a real undecodable body could hold anything,
// which is the point — they are there so an assertion can name the property under test:
// content present in the bytes must not appear in the durable record built from them.
// They are the same deliberately non-credential-shaped markers the sibling test uses,
// so a failure prints a fixture label rather than credential-shaped text into a CI log.
func undecodableMessage() messaging.Message {
	// A leading run of 0xff bytes: field number 15, wire type 7, which no protobuf
	// parser accepts. The test asserts the fixture really is undecodable rather than
	// trusting this, because a fixture that quietly started decoding would take every
	// assertion below with it.
	body := append([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		[]byte(fixtureCredentialId+"|"+fixtureCredentialSecret)...)
	return messaging.Message{
		Subject:      testTenantSubject,
		Value:        body,
		StreamSeq:    undecodableStreamSeq,
		NumDelivered: 1,
	}
}

// An undecodable message is dead-lettered without the bytes that would not decode.
//
// The record is durable: it is retained for the failed-events stream's window and is
// what an operator exports when debugging ingest. A body that failed to parse is
// exactly the case where what it holds is least predictable, so the record describes
// it and locates it rather than keeping a second copy of it.
func (suite *InboundEventsProcessorTestSuite) TestUndecodableMessageIsDeadLetteredWithoutItsPayload() {
	msg := undecodableMessage()
	_, decodeErr := esproto.UnmarshalUnresolvedEvent(msg.Value)
	require.Error(suite.T(), decodeErr, "the fixture must not decode, or nothing below is testing the invalid path")

	logs := captureWarnings(suite.T())
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(msg, nil)

	suite.IP.ProcessMessage(context.Background())
	item := awaitFailed(suite)

	assert.Empty(suite.T(), item.event.Payload,
		"the dead-letter record must not retain the bytes that could not be decoded")
	// Not just the payload field: nothing anywhere in the record may echo the body,
	// however a future encoding lays the record out.
	whole := item.event.Message + "\x00" + item.event.Error + "\x00" + string(item.event.Payload)
	assert.NotContains(suite.T(), whole, fixtureCredentialId)
	assert.NotContains(suite.T(), whole, fixtureCredentialSecret)
	// And not through the log line either — it is written from the same inputs.
	assert.NotContains(suite.T(), logs.String(), fixtureCredentialId)
	assert.NotContains(suite.T(), logs.String(), fixtureCredentialSecret)
}

// The record still reports the failure, and still says where the original is.
//
// This is the half that makes dropping the payload affordable. Everything recorded is
// server-derived, and the stream sequence points at the message itself, still on
// inbound-events — so an operator debugging a malformed producer is sent to the
// original rather than handed a copy of it. Without these the change would be a
// removal that also removed the reason the dead letter exists.
func (suite *InboundEventsProcessorTestSuite) TestUndecodableMessageDeadLetterLocatesTheOriginal() {
	msg := undecodableMessage()
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(msg, nil)

	suite.IP.ProcessMessage(context.Background())
	item := awaitFailed(suite)

	// It still reads as a failure. An undecodable message that stopped saying so would
	// satisfy every assertion in the test above while being strictly worse than what it
	// replaced.
	assert.Equal(suite.T(), uint(dmproto.FailureReason_Invalid), item.event.Reason,
		"the record must still say the message could not be parsed")
	assert.NotEmpty(suite.T(), item.event.Error, "the record must still carry the decode error")
	assert.Equal(suite.T(), "tenant1", item.tenant)

	assert.Contains(suite.T(), item.event.Message, strconv.FormatUint(undecodableStreamSeq, 10),
		"the record must locate the original by its stream sequence")
	assert.Contains(suite.T(), item.event.Message, strconv.Itoa(len(msg.Value)),
		"the record must state how large the undecodable body was")
	assert.Contains(suite.T(), item.event.Message, testTenantSubject,
		"the record must name the subject the original arrived on")
}

// A decode error that quotes the body back is bounded before it is recorded.
//
// This is the second door into the same record. UnmarshalUnresolvedEvent reaches
// time.Parse for bytes that decode as an event with an unparseable instant, and
// time.Parse quotes the offending string verbatim and unboundedly — so a body designed
// to be echoed would put an arbitrarily large payload-derived blob into the record
// through the error, after the payload itself was dropped.
func (suite *InboundEventsProcessorTestSuite) TestUndecodableMessageDeadLetterBoundsTheDecodeError() {
	// Long enough that an unbounded error dwarfs the cap; the marker makes the
	// assertion about content, not only about length.
	echoed := strings.Repeat("q", 4000) + fixtureCredentialSecret
	encoded, err := gproto.Marshal(&esproto.PUnresolvedEvent{
		Device:       "TEST-123",
		EventType:    int64(esmodel.Location),
		OccurredTime: echoed,
	})
	require.NoError(suite.T(), err)
	// The fixture only tests what it claims to if the decode really does fail on the
	// instant, echoing the string.
	_, decodeErr := esproto.UnmarshalUnresolvedEvent(encoded)
	require.Error(suite.T(), decodeErr)
	require.Contains(suite.T(), decodeErr.Error(), echoed,
		"the fixture must produce an error that echoes the body, or the bound is untested")

	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(messaging.Message{
		Subject: testTenantSubject, Value: encoded, StreamSeq: undecodableStreamSeq,
	}, nil)

	suite.IP.ProcessMessage(context.Background())
	item := awaitFailed(suite)

	assert.LessOrEqual(suite.T(), len(item.event.Error), invalidEventErrorCap+len("... (truncated)"),
		"the recorded decode error must be bounded")
	assert.NotContains(suite.T(), item.event.Error, fixtureCredentialSecret,
		"the recorded decode error must not carry the echoed body through")
	assert.True(suite.T(), strings.HasSuffix(item.event.Error, "... (truncated)"),
		"a shortened error must say it was shortened rather than look complete")
	assert.NotEmpty(suite.T(), item.event.Error, "the record must still carry what is left of the decode error")
}

// awaitResolved takes the next resolved event off the processor's resolved channel.
// Bounded for the same reason awaitFailed is, and it matters more here: the mutant
// this test exists to catch is one that stops a decodable event reaching this channel
// at all, which is an absence. An unbounded read would report that absence as a hung
// test rather than as a failure naming it.
func awaitResolved(suite *InboundEventsProcessorTestSuite) resolvedItem {
	suite.T().Helper()
	select {
	case item := <-suite.IP.resolved:
		return item
	case <-time.After(10 * time.Second):
		require.FailNow(suite.T(), "a decodable event never reached the resolved path")
		return resolvedItem{}
	}
}

// The counterweight: a decodable event is untouched by any of the above.
//
// A gate that proved only the removal would pass just as happily with resolution
// broken. Both halves are asserted here — the normal path still resolves, and nothing
// lands on the dead-letter channel.
func (suite *InboundEventsProcessorTestSuite) TestDecodableEventStillTakesTheNormalPath() {
	loc := buildLocationsEvent()
	encoded, err := esproto.MarshalUnresolvedEvent(loc)
	require.NoError(suite.T(), err)

	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(messaging.Message{
		Subject: testTenantSubject, Value: encoded, StreamSeq: undecodableStreamSeq,
	}, nil)
	suite.API.Mock.On("DevicesByToken", mock.Anything, mock.Anything).Return([]*dmodel.Device{buildDevice()}, nil)
	suite.API.Mock.On("EntityRelationships", mock.Anything, mock.Anything).Return(
		&dmodel.EntityRelationshipSearchResults{Results: []dmodel.EntityRelationship{*buildDeviceRelationship()}}, nil)

	suite.IP.ProcessMessage(context.Background())
	item := awaitResolved(suite)

	assert.Equal(suite.T(), "tenant1", item.tenant)
	assert.Equal(suite.T(), "TEST-123", item.event.SourceDeviceToken,
		"the resolved event must still name the device that reported it")
	// The resolver publishes to the resolved channel only after it has declined to
	// publish to the failed one, so by the time the read above returns this is decided.
	select {
	case failed := <-suite.IP.failed:
		require.FailNow(suite.T(), "a resolvable event was dead-lettered", "reason %d: %s",
			failed.event.Reason, failed.event.Message)
	default:
	}
}

// The other half of the counterweight: a decodable event that fails downstream is
// still dead-lettered WITH its payload.
//
// This is what keeps the removal scoped. The payload is dropped because an undecodable
// body cannot be one, not because dead letters stopped carrying bodies — a change that
// cleared FailedEvent.Payload everywhere would satisfy the removal test and silently
// gut the path that has something worth archiving.
func (suite *InboundEventsProcessorTestSuite) TestResolutionFailureStillDeadLettersItsPayload() {
	loc := buildLocationsEvent()
	encoded, err := esproto.MarshalUnresolvedEvent(loc)
	require.NoError(suite.T(), err)

	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(messaging.Message{
		Subject: testTenantSubject, Value: encoded, NumDelivered: messaging.MaxDeliver,
	}, nil)
	suite.API.Mock.On("DevicesByToken", mock.Anything, mock.Anything).
		Return([]*dmodel.Device{}, errors.New("device lookup failed"))

	suite.IP.ProcessMessage(context.Background())
	item := awaitFailed(suite)

	assert.NotEmpty(suite.T(), item.event.Payload,
		"an event that decoded must still be archived on its dead-letter record")
	archived, err := esproto.UnmarshalUnresolvedEvent(item.event.Payload)
	require.NoError(suite.T(), err)
	assert.Equal(suite.T(), "TEST-123", archived.Device, "the archived event must still name the device")
	assert.NotEqual(suite.T(), uint(dmproto.FailureReason_Invalid), item.event.Reason,
		"a resolution failure must not be reported as an unparseable message")
}
