// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	dmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"gorm.io/gorm"
)

type EventResolverTestSuite struct {
	suite.Suite
	API *dmtest.MockApi
}

func (suite *EventResolverTestSuite) SetupTest() {
	suite.API = new(dmtest.MockApi)
}

// Build a resolver bound to the suite mock under the given auth mode.
func (suite *EventResolverTestSuite) resolver(authMode string) *EventResolver {
	return NewEventResolver(1, suite.API, authMode, nil, nil, nil, nil, nil)
}

// A device whose stable token is the given value.
func deviceWithToken(token string) *dmodel.Device {
	return &dmodel.Device{
		Model:          gorm.Model{ID: 1},
		TokenReference: rdb.TokenReference{Token: token},
	}
}

// An event presenting an access-token credential and (optionally) a self-asserted
// device token.
func eventWithCredential(deviceToken string) *esmodel.UnresolvedEvent {
	ctype := string(dmodel.CredentialAccessToken)
	cid := "tok-abc"
	return &esmodel.UnresolvedEvent{
		Device:         deviceToken,
		EventType:      esmodel.Location,
		CredentialType: &ctype,
		CredentialId:   &cid,
	}
}

// optional mode + no credential falls back to the trusted device-token lookup.
func (suite *EventResolverTestSuite) TestOptionalNoCredentialUsesToken() {
	suite.API.Mock.On("DevicesByToken").Return([]*dmodel.Device{deviceWithToken("TEST-123")}, nil)

	event := &esmodel.UnresolvedEvent{Device: "TEST-123", EventType: esmodel.Location}
	device, reason, err := suite.resolver(config.AuthModeOptional).resolveDevice(context.Background(), event)

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), uint(0), reason)
	assert.Equal(suite.T(), "TEST-123", device.Token)
	suite.API.AssertNotCalled(suite.T(), "AuthenticateDevice")
}

// A presented credential is authenticated and the resolved device is returned;
// the trusted token path is not consulted.
func (suite *EventResolverTestSuite) TestCredentialAuthenticates() {
	suite.API.Mock.On("AuthenticateDevice").Return(deviceWithToken("TEST-123"), nil)

	device, reason, err := suite.resolver(config.AuthModeOptional).resolveDevice(context.Background(), eventWithCredential("TEST-123"))

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), uint(0), reason)
	assert.Equal(suite.T(), "TEST-123", device.Token)
	suite.API.AssertNotCalled(suite.T(), "DevicesByToken")
}

// A credential is also honoured when the event carries no self-asserted token.
func (suite *EventResolverTestSuite) TestCredentialWithoutAssertedToken() {
	suite.API.Mock.On("AuthenticateDevice").Return(deviceWithToken("TEST-123"), nil)

	device, _, err := suite.resolver(config.AuthModeOptional).resolveDevice(context.Background(), eventWithCredential(""))

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "TEST-123", device.Token)
}

// A self-asserted token that disagrees with the authenticated device is rejected
// to prevent impersonation.
func (suite *EventResolverTestSuite) TestCredentialTokenMismatchRejected() {
	suite.API.Mock.On("AuthenticateDevice").Return(deviceWithToken("TEST-123"), nil)

	_, reason, err := suite.resolver(config.AuthModeOptional).resolveDevice(context.Background(), eventWithCredential("OTHER-DEVICE"))

	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), uint(dmproto.FailureReason_Unauthenticated), reason)
}

// A failed authentication surfaces as an Unauthenticated failure.
func (suite *EventResolverTestSuite) TestCredentialAuthFails() {
	suite.API.Mock.On("AuthenticateDevice").Return((*dmodel.Device)(nil), dmodel.ErrCredentialExpired)

	_, reason, err := suite.resolver(config.AuthModeOptional).resolveDevice(context.Background(), eventWithCredential("TEST-123"))

	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), uint(dmproto.FailureReason_Unauthenticated), reason)
}

// required mode rejects an event that presents no credential.
func (suite *EventResolverTestSuite) TestRequiredNoCredentialRejected() {
	event := &esmodel.UnresolvedEvent{Device: "TEST-123", EventType: esmodel.Location}
	_, reason, err := suite.resolver(config.AuthModeRequired).resolveDevice(context.Background(), event)

	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), uint(dmproto.FailureReason_Unauthenticated), reason)
	suite.API.AssertNotCalled(suite.T(), "DevicesByToken")
	suite.API.AssertNotCalled(suite.T(), "AuthenticateDevice")
}

// disabled mode ignores a presented credential and trusts the device token.
func (suite *EventResolverTestSuite) TestDisabledIgnoresCredential() {
	suite.API.Mock.On("DevicesByToken").Return([]*dmodel.Device{deviceWithToken("TEST-123")}, nil)

	device, _, err := suite.resolver(config.AuthModeDisabled).resolveDevice(context.Background(), eventWithCredential("TEST-123"))

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "TEST-123", device.Token)
	suite.API.AssertNotCalled(suite.T(), "AuthenticateDevice")
}

// An unresolvable device token (no credential, optional mode) reports DeviceNotFound.
func (suite *EventResolverTestSuite) TestUnknownDeviceToken() {
	suite.API.Mock.On("DevicesByToken").Return([]*dmodel.Device{}, errors.New("not found"))

	event := &esmodel.UnresolvedEvent{Device: "MISSING", EventType: esmodel.Location}
	_, reason, err := suite.resolver(config.AuthModeOptional).resolveDevice(context.Background(), event)

	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), uint(dmproto.FailureReason_DeviceNotFound), reason)
}

// A measurement event for the given key/value.
func measurementEvent(key, value string) *esmodel.UnresolvedEvent {
	return &esmodel.UnresolvedEvent{
		Device:    "TEST-123",
		EventType: esmodel.Measurement,
		Payload: &esmodel.UnresolvedMeasurementsPayload{
			Entries: []esmodel.UnresolvedMeasurementsEntry{
				{Measurements: map[string]string{key: value}},
			},
		},
	}
}

// A measurement violating a declared metric definition routes to the dead-letter
// path (FailureReason_Invalid) before any relationship fan-out is attempted.
func (suite *EventResolverTestSuite) TestMeasurementValidationRejects() {
	def := &dmodel.MetricDefinition{MetricKey: "temp", DataType: "DOUBLE",
		MaxValue: sql.NullFloat64{Float64: 100, Valid: true}}
	suite.API.Mock.On("MetricDefinitionsByDeviceType").Return([]*dmodel.MetricDefinition{def}, nil)

	_, reason, err := suite.resolver(config.AuthModeOptional).HandleStandardEvent(
		context.Background(), deviceWithToken("TEST-123"), measurementEvent("temp", "150"))

	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), uint(dmproto.FailureReason_Invalid), reason)
	suite.API.AssertNotCalled(suite.T(), "EntityRelationships")
}

// A conforming measurement passes validation and proceeds to fan-out (here, no
// tracked relationships, so zero resolved events and no failure).
func (suite *EventResolverTestSuite) TestMeasurementValidationPasses() {
	def := &dmodel.MetricDefinition{MetricKey: "temp", DataType: "DOUBLE"}
	suite.API.Mock.On("MetricDefinitionsByDeviceType").Return([]*dmodel.MetricDefinition{def}, nil)
	suite.API.Mock.On("EntityRelationships").Return(
		&dmodel.EntityRelationshipSearchResults{Results: []dmodel.EntityRelationship{}}, nil)

	results, reason, err := suite.resolver(config.AuthModeOptional).HandleStandardEvent(
		context.Background(), deviceWithToken("TEST-123"), measurementEvent("temp", "42"))

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), uint(0), reason)
	assert.Empty(suite.T(), results)
}

// A device type that declares no metric definitions skips validation entirely
// (an undeclared/untyped fleet is unaffected).
func (suite *EventResolverTestSuite) TestMeasurementNoDefinitionsSkipsValidation() {
	suite.API.Mock.On("MetricDefinitionsByDeviceType").Return([]*dmodel.MetricDefinition{}, nil)
	suite.API.Mock.On("EntityRelationships").Return(
		&dmodel.EntityRelationshipSearchResults{Results: []dmodel.EntityRelationship{}}, nil)

	_, reason, err := suite.resolver(config.AuthModeOptional).HandleStandardEvent(
		context.Background(), deviceWithToken("TEST-123"), measurementEvent("anything", "not-a-number"))

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), uint(0), reason)
}

func TestEventResolverTestSuite(t *testing.T) {
	suite.Run(t, new(EventResolverTestSuite))
}

// Verify presentedCredential treats blank/absent fields as "no credential".
func TestPresentedCredential(t *testing.T) {
	ctype := string(dmodel.CredentialAccessToken)
	cid := "tok-abc"
	empty := ""

	assert.Nil(t, presentedCredential(&esmodel.UnresolvedEvent{}))
	assert.Nil(t, presentedCredential(&esmodel.UnresolvedEvent{CredentialType: &ctype}))
	assert.Nil(t, presentedCredential(&esmodel.UnresolvedEvent{CredentialType: &ctype, CredentialId: &empty}))

	got := presentedCredential(&esmodel.UnresolvedEvent{CredentialType: &ctype, CredentialId: &cid})
	assert.NotNil(t, got)
	assert.Equal(t, ctype, got.CredentialType)
	assert.Equal(t, cid, got.CredentialId)
}
