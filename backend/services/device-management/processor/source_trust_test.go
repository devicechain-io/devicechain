// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"

	"github.com/devicechain-io/dc-device-management/config"
	dmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/assert"
)

// The presence-auth fix (B0): a transport-authenticated event (LwM2M DTLS-PSK /
// Sparkplug broker) presents no per-event credential, but a trusted ingest source
// marked it. Under `required` the resolver trusts its self-asserted device token —
// the same trust `disabled`/`optional` already grant, confined to these sources.
func (suite *EventResolverTestSuite) TestRequiredTransportAuthenticatedBypass() {
	suite.API.Mock.On("DevicesByToken").Return([]*dmodel.Device{deviceWithToken("lw-dev-1")}, nil)
	// Honored for exactly the event types the marked ingest path emits.
	for _, et := range []esmodel.EventType{esmodel.StateChange, esmodel.Measurement} {
		event := &esmodel.UnresolvedEvent{Device: "lw-dev-1", EventType: et, AuthenticatedTransport: true}
		device, _, err := suite.resolver(config.AuthModeRequired).resolveDevice(context.Background(), event)
		assert.NoError(suite.T(), err, et.String())
		assert.Equal(suite.T(), "lw-dev-1", device.Token, et.String())
	}
	// The bypass trusts the token directly — it never runs credential authentication.
	suite.API.AssertNotCalled(suite.T(), "AuthenticateDevice")
}

// SF-2: the marker is honored ONLY for the event types the transport-authenticated
// path actually emits. A marked event of any OTHER type is still rejected under
// `required` — so a future emit path for a more dangerous type (a relationship
// mutation, say) cannot silently inherit the credential bypass.
func (suite *EventResolverTestSuite) TestTransportAuthenticatedConfinedToEmittedTypes() {
	for _, et := range []esmodel.EventType{esmodel.NewRelationship, esmodel.Location, esmodel.Alert} {
		event := &esmodel.UnresolvedEvent{Device: "lw-dev-1", EventType: et, AuthenticatedTransport: true}
		_, reason, err := suite.resolver(config.AuthModeRequired).resolveDevice(context.Background(), event)
		assert.Error(suite.T(), err, et.String())
		assert.Equal(suite.T(), uint(dmproto.FailureReason_Unauthenticated), reason, et.String())
	}
	suite.API.AssertNotCalled(suite.T(), "DevicesByToken")
}

// The bypass is not the default: a `required`-mode event with no credential and no
// marker is still rejected. Guards the trust boundary — the marker must be set by a
// trusted source, never assumed.
func (suite *EventResolverTestSuite) TestRequiredUnmarkedStillRejected() {
	event := &esmodel.UnresolvedEvent{Device: "lw-dev-1", EventType: esmodel.StateChange, AuthenticatedTransport: false}
	_, reason, err := suite.resolver(config.AuthModeRequired).resolveDevice(context.Background(), event)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), uint(dmproto.FailureReason_Unauthenticated), reason)
	suite.API.AssertNotCalled(suite.T(), "DevicesByToken")
}

// The marker only SUBSTITUTES for an absent credential; it never skips verifying a
// credential that IS presented, nor the anti-impersonation token match. A marked
// event carrying a credential still authenticates through AuthenticateDevice.
func (suite *EventResolverTestSuite) TestTransportAuthenticatedStillVerifiesPresentedCredential() {
	suite.API.Mock.On("AuthenticateDevice").Return(deviceWithToken("TEST-123"), nil)
	event := eventWithCredential("TEST-123")
	event.EventType = esmodel.Measurement
	event.AuthenticatedTransport = true
	device, _, err := suite.resolver(config.AuthModeRequired).resolveDevice(context.Background(), event)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "TEST-123", device.Token)
	suite.API.AssertCalled(suite.T(), "AuthenticateDevice")
}
