// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
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
)

// The credential the fixture device presents. The values are markers, not
// credential-shaped strings: an assertion here fails by printing what it found, and
// a test about not retaining credential material should not be what puts
// credential-shaped text into a CI log.
const (
	fixtureCredentialId     = "fixture-credential-id"
	fixtureCredentialSecret = "fixture-credential-secret"
)

// credentialLocationEvent is a position report carrying a presented credential, the
// shape an inbound event has whenever device authentication is in play.
func credentialLocationEvent() *esmodel.UnresolvedEvent {
	event := buildLocationsEvent()
	ctype := string(dmodel.CredentialMqttBasic)
	cid, secret := fixtureCredentialId, fixtureCredentialSecret
	event.CredentialType = &ctype
	event.CredentialId = &cid
	event.CredentialSecret = &secret
	return event
}

// credentialLocationMessage marshals that event onto an inbound message. deliveries
// is the delivery count stamped on it: at the cap, an unresolvable event is
// dead-lettered rather than left for another redelivery.
func credentialLocationMessage(suite *InboundEventsProcessorTestSuite, deliveries int) messaging.Message {
	event := credentialLocationEvent()
	encoded, err := esproto.MarshalUnresolvedEvent(event)
	require.NoError(suite.T(), err)
	return messaging.Message{
		Subject:      testTenantSubject,
		Key:          []byte(event.Device),
		Value:        encoded,
		NumDelivered: deliveries,
	}
}

// awaitFailed takes the next dead-lettered event off the processor's failed channel.
// The resolvers run as a worker pool, so the item arrives asynchronously; the bound
// turns "it never came" into a named failure instead of a hung test.
func awaitFailed(suite *InboundEventsProcessorTestSuite) failedItem {
	suite.T().Helper()
	select {
	case item := <-suite.IP.failed:
		return item
	case <-time.After(10 * time.Second):
		require.FailNow(suite.T(), "no event reached the dead-letter path")
		return failedItem{}
	}
}

// A failed authentication is dead-lettered without the credential that failed.
//
// The dead-letter record is durable: it outlives the request by the stream's
// retention and is the thing an operator exports when debugging ingest. The
// credential has already served its only purpose by the time resolution fails, so
// the archived record does not need it.
func (suite *InboundEventsProcessorTestSuite) TestDeadLetteredEventCarriesNoPresentedCredential() {
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(credentialLocationMessage(suite, messaging.MaxDeliver), nil)
	suite.API.Mock.On("AuthenticateDevice").Return((*dmodel.Device)(nil), dmodel.ErrCredentialSecretMismatch)

	suite.IP.ProcessMessage(context.Background())
	item := awaitFailed(suite)

	archived, err := esproto.UnmarshalUnresolvedEvent(item.event.Payload)
	require.NoError(suite.T(), err)
	assert.Nil(suite.T(), archived.CredentialType, "the archived event must not retain the presented credential type")
	assert.Nil(suite.T(), archived.CredentialId, "the archived event must not retain the presented credential id")
	assert.Nil(suite.T(), archived.CredentialSecret, "the archived event must not retain the presented credential secret")
	// The whole record, not just the decoded fields: nothing anywhere in the stored
	// bytes carries the material, however a future encoding chooses to lay it out.
	assert.NotContains(suite.T(), string(item.event.Payload), fixtureCredentialId)
	assert.NotContains(suite.T(), string(item.event.Payload), fixtureCredentialSecret)
}

// The record is still a usable dead letter, and it still reads as a failure.
//
// This is the counterweight to the assertions above, and it is the property the
// repo's fail-closed rule turns on: an empty record, or one whose reason no longer
// says the event was refused, would satisfy "no credential material" while making an
// authentication failure indistinguishable from a success further down. Everything
// that identifies the event is server-derived and stays.
func (suite *InboundEventsProcessorTestSuite) TestDeadLetteredEventStillReportsTheFailure() {
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(credentialLocationMessage(suite, messaging.MaxDeliver), nil)
	suite.API.Mock.On("AuthenticateDevice").Return((*dmodel.Device)(nil), dmodel.ErrCredentialSecretMismatch)

	suite.IP.ProcessMessage(context.Background())
	item := awaitFailed(suite)

	assert.Equal(suite.T(), uint(dmproto.FailureReason_Unauthenticated), item.event.Reason,
		"the record must still say the event was refused for failing authentication")
	assert.NotEmpty(suite.T(), item.event.Error, "the record must still carry the reason it failed")
	assert.Equal(suite.T(), "tenant1", item.tenant)

	archived, err := esproto.UnmarshalUnresolvedEvent(item.event.Payload)
	require.NoError(suite.T(), err)
	assert.Equal(suite.T(), "TEST-123", archived.Device, "the archived event must still name the device")
	assert.Equal(suite.T(), "mysource", archived.Source, "the archived event must still name the source")
	assert.Equal(suite.T(), esmodel.Location, archived.EventType, "the archived event must still carry its type")
	assert.NotNil(suite.T(), archived.Payload, "the archived event must still carry the payload being debugged")
}

// With debug logging on, the resolver's dump of the inbound event carries no
// credential material — while still dumping the event.
//
// The dump is the whole struct rendered as JSON, and a debug line is routinely
// shipped off-cluster and retained far longer than the log level was meant to stay
// raised.
func (suite *InboundEventsProcessorTestSuite) TestDebugEventDumpCarriesNoPresentedCredential() {
	// captureWarnings redirects the global logger and forces the global level, so
	// this cannot pass merely because some earlier test left logging muted.
	logs := captureWarnings(suite.T())
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(credentialLocationMessage(suite, messaging.MaxDeliver), nil)
	suite.API.Mock.On("AuthenticateDevice").Return((*dmodel.Device)(nil), dmodel.ErrCredentialSecretMismatch)

	suite.IP.ProcessMessage(context.Background())
	awaitFailed(suite)

	got := logs.String()
	assert.NotContains(suite.T(), got, fixtureCredentialId, "the credential id must not reach the log")
	assert.NotContains(suite.T(), got, fixtureCredentialSecret, "the credential secret must not reach the log")
	// The counterweight: the dump must still happen and still be useful, or the two
	// assertions above would also pass with the dump deleted outright.
	assert.True(suite.T(), strings.Contains(got, "Received Location event"),
		"the event dump must still be logged at debug level")
	assert.True(suite.T(), strings.Contains(got, `\"Device\": \"TEST-123\"`),
		"the event dump must still render the event body")
}

// A credential that authenticates still resolves the event, end to end.
//
// This is the counterweight to the whole change: clearing the credential is only
// correct while a legitimate authentication still works. A gate that proved only the
// removal would pass just as happily with authentication broken — and would keep
// passing if the clear ever moved upstream of the resolver, where it would silence
// every credential-authenticated device at once.
func (suite *InboundEventsProcessorTestSuite) TestCredentialStillAuthenticatesAndResolves() {
	// Debug logging ON, which is not incidental. The redaction upstream of
	// authentication is what this pins: an implementation that cleared the event in
	// place instead of on a copy would strip the credential in the debug dump and
	// leave nothing for authentication to read — so the whole fleet's credential
	// path would break the moment an operator raised the log level, and stay
	// correct in every test that did not. This test runs on the broken side.
	logs := captureWarnings(suite.T())
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(credentialLocationMessage(suite, 1), nil)
	suite.API.Mock.On("AuthenticateDevice").Return(buildDevice(), nil)
	suite.Resolved.Mock.On("WriteMessages", mock.Anything, mock.Anything).Return(nil)
	suite.API.Mock.On("EntityRelationships", mock.Anything, mock.Anything).Return(
		&dmodel.EntityRelationshipSearchResults{Results: []dmodel.EntityRelationship{*buildDeviceRelationship()}}, nil)
	// The token lookup is stubbed deliberately, and it is what gives the last
	// assertion its teeth. This suite runs in the mode where an event with no
	// credential falls back to trusting the self-asserted device token — which
	// resolves this event to the SAME device and publishes the SAME resolved event.
	// So an implementation that dropped the credential before authentication rather
	// than after would still satisfy every assertion about the outcome; leaving this
	// unstubbed would instead abort the suite on an unexpected call, which reports
	// the same fact as a crash rather than as a named failure.
	suite.API.Mock.On("DevicesByToken", mock.Anything, mock.Anything).Return([]*dmodel.Device{buildDevice()}, nil)

	ctx := context.Background()
	suite.IP.ProcessMessage(ctx)
	suite.IP.ProcessResolvedEvent(ctx)

	suite.Resolved.AssertCalled(suite.T(), "WriteMessages", mock.Anything, mock.Anything)
	suite.API.AssertCalled(suite.T(), "AuthenticateDevice")
	// The credential was authoritative: resolution did not quietly fall through to
	// the trusted-token path, which would produce an identical resolved event and
	// hide a credential that never reached authentication at all.
	suite.API.AssertNotCalled(suite.T(), "DevicesByToken")
	// A successful resolution logs the same dump as a failed one, so the redaction
	// holds on this path too.
	assert.NotContains(suite.T(), logs.String(), fixtureCredentialId)
	assert.NotContains(suite.T(), logs.String(), fixtureCredentialSecret)
}
