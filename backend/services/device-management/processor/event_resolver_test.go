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

// The device's rule-scoping identity (device-type + active-published-profile-version
// tokens) is denormalized onto every resolved event so event-processing's DETECT
// engine can select the applicable rules without a graph read (ADR-051).
func (suite *EventResolverTestSuite) TestResolvedEventCarriesProfileScope() {
	suite.API.Mock.On("MetricDefinitionsByDeviceType").Return([]*dmodel.MetricDefinition{}, nil)
	suite.API.Mock.On("EntityRelationships").Return(
		&dmodel.EntityRelationshipSearchResults{Results: []dmodel.EntityRelationship{}}, nil)
	suite.API.ProfileScopeResult = &dmodel.ProfileScope{DeviceTypeToken: "sensor-type", ProfileVersionToken: "temp-profile@3"}

	device := deviceWithToken("TEST-123")
	device.DeviceTypeId = 77
	results, reason, err := suite.resolver(config.AuthModeOptional).HandleStandardEvent(
		context.Background(), device, measurementEvent("temp", "42"))

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), uint(0), reason)
	assert.Len(suite.T(), results, 1)
	assert.Equal(suite.T(), "sensor-type", results[0].Resolved.DeviceTypeToken)
	assert.Equal(suite.T(), "temp-profile@3", results[0].Resolved.ProfileVersionToken)
	// The resolver keys the scope on the device's TYPE id, not its row id.
	assert.Equal(suite.T(), uint(77), suite.API.ProfileScopeArg)
}

// The resolver stamps the DEDUPED UNION of the reporting device's and each anchor's
// dynamic-group memberships onto the event as ScopeMemberships (ADR-062), so the DETECT
// engine's scope check is a set test on the replayed bytes. A device-facet membership and
// a geographic (area-anchor) membership both land; a membership shared by two targets
// appears once.
func (suite *EventResolverTestSuite) TestResolvedEventCarriesScopeMemberships() {
	suite.API.Mock.On("MetricDefinitionsByDeviceType").Return([]*dmodel.MetricDefinition{}, nil)
	// One tracked anchor: the device is located-in an area (row id 900).
	suite.API.Mock.On("EntityRelationships").Return(
		&dmodel.EntityRelationshipSearchResults{Results: []dmodel.EntityRelationship{
			{TargetType: "area", TargetToken: "warehouse-3", TargetId: 900},
		}}, nil)
	suite.API.ProfileScopeResult = &dmodel.ProfileScope{}

	device := deviceWithToken("TEST-123")
	device.ID = 500
	// The device is in beta-fleet@1 + shared@1; the area is in arid-areas@2 + shared@1.
	suite.API.MembershipsFn = func(entityType string, entityId uint) []dmodel.GroupMembership {
		if entityType == "device" && entityId == 500 {
			return []dmodel.GroupMembership{{GroupToken: "beta-fleet", SelectorVersion: 1}, {GroupToken: "shared", SelectorVersion: 1}}
		}
		if entityType == "area" && entityId == 900 {
			return []dmodel.GroupMembership{{GroupToken: "arid-areas", SelectorVersion: 2}, {GroupToken: "shared", SelectorVersion: 1}}
		}
		return nil
	}

	results, reason, err := suite.resolver(config.AuthModeOptional).HandleStandardEvent(
		context.Background(), device, measurementEvent("temp", "42"))

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), uint(0), reason)
	assert.Len(suite.T(), results, 1)
	assert.ElementsMatch(suite.T(), []dmodel.GroupRef{
		{GroupToken: "beta-fleet", Version: 1},
		{GroupToken: "arid-areas", Version: 2},
		{GroupToken: "shared", Version: 1}, // shared by device+area, deduped to one
	}, results[0].Resolved.ScopeMemberships)
}

// A scope-resolution failure on a new-relationship event aborts BEFORE the
// relationship is created — so a transient lookup blip cannot leave a committed
// relationship that a redelivery would duplicate (a fresh token per attempt is not
// idempotent). It also maps to the retryable ApiCallFailed reason (ADR-051 review).
func (suite *EventResolverTestSuite) TestNewRelationshipAbortsBeforeCreateOnScopeError() {
	suite.API.ProfileScopeErr = errors.New("transient scope lookup failure")

	event := &esmodel.UnresolvedEvent{
		Device:    "TEST-123",
		EventType: esmodel.NewRelationship,
		Payload: &esmodel.UnresolvedNewRelationshipPayload{
			RelationshipType: "located-in", TargetType: "area", Target: "warehouse-3",
		},
	}
	_, reason, err := suite.resolver(config.AuthModeOptional).HandleNewRelationshipEvent(
		context.Background(), deviceWithToken("TEST-123"), event)

	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), uint(dmproto.FailureReason_ApiCallFailed), reason)
	suite.API.AssertNotCalled(suite.T(), "CreateEntityRelationship")
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

// A conforming measurement passes validation and resolves. With no tracked
// relationship it resolves to exactly one *anchorless* event (not dropped) —
// ADR-013 addendum 2026-07-01.
func (suite *EventResolverTestSuite) TestMeasurementValidationPasses() {
	def := &dmodel.MetricDefinition{MetricKey: "temp", DataType: "DOUBLE"}
	suite.API.Mock.On("MetricDefinitionsByDeviceType").Return([]*dmodel.MetricDefinition{def}, nil)
	suite.API.Mock.On("EntityRelationships").Return(
		&dmodel.EntityRelationshipSearchResults{Results: []dmodel.EntityRelationship{}}, nil)

	results, reason, err := suite.resolver(config.AuthModeOptional).HandleStandardEvent(
		context.Background(), deviceWithToken("TEST-123"), measurementEvent("temp", "42"))

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), uint(0), reason)
	assert.Len(suite.T(), results, 1)
	assert.Empty(suite.T(), results[0].Resolved.Anchors)
}

// A declared metric binds its definition id as the classifier and denormalizes the
// definition's unit + data type onto the entry, so the persisted value is
// self-describing on read without a cross-service hop (ADR-016).
func (suite *EventResolverTestSuite) TestMeasurementClassifierBound() {
	def := &dmodel.MetricDefinition{Model: gorm.Model{ID: 42}, MetricKey: "temp", DataType: "DOUBLE",
		Unit: sql.NullString{String: "Cel", Valid: true}}
	suite.API.Mock.On("MetricDefinitionsByDeviceType").Return([]*dmodel.MetricDefinition{def}, nil)

	out, err := suite.resolver(config.AuthModeOptional).ResolveMeasurementsEventPayload(
		context.Background(), deviceWithToken("TEST-123"), nil, measurementEvent("temp", "42"))

	assert.NoError(suite.T(), err)
	entry := out.(*dmodel.ResolvedMeasurementsPayload).Entries[0].Entries[0]
	if assert.NotNil(suite.T(), entry.Classifier) {
		assert.Equal(suite.T(), uint64(42), *entry.Classifier)
	}
	assert.Equal(suite.T(), "42", entry.Value)
	if assert.NotNil(suite.T(), entry.Unit) {
		assert.Equal(suite.T(), "Cel", *entry.Unit)
	}
	if assert.NotNil(suite.T(), entry.DataType) {
		assert.Equal(suite.T(), "DOUBLE", *entry.DataType)
	}
}

// A BOOLEAN metric is normalized to 1/0 so it stores in the numeric column, and
// still carries its classifier + denormalized data type (so a reader renders the
// stored 0/1 as false/true). A unit-less metric denormalizes a nil unit.
func (suite *EventResolverTestSuite) TestMeasurementBooleanNormalized() {
	def := &dmodel.MetricDefinition{Model: gorm.Model{ID: 7}, MetricKey: "engaged", DataType: "BOOLEAN"}
	suite.API.Mock.On("MetricDefinitionsByDeviceType").Return([]*dmodel.MetricDefinition{def}, nil)

	out, err := suite.resolver(config.AuthModeOptional).ResolveMeasurementsEventPayload(
		context.Background(), deviceWithToken("TEST-123"), nil, measurementEvent("engaged", "true"))

	assert.NoError(suite.T(), err)
	entry := out.(*dmodel.ResolvedMeasurementsPayload).Entries[0].Entries[0]
	assert.Equal(suite.T(), "1", entry.Value)
	assert.NotNil(suite.T(), entry.Classifier)
	if assert.NotNil(suite.T(), entry.DataType) {
		assert.Equal(suite.T(), "BOOLEAN", *entry.DataType)
	}
	assert.Nil(suite.T(), entry.Unit)
}

// An undeclared numeric measurement resolves unclassified and unchanged (lenient),
// carrying no denormalized unit/type.
func (suite *EventResolverTestSuite) TestMeasurementUndeclaredUnclassified() {
	suite.API.Mock.On("MetricDefinitionsByDeviceType").Return([]*dmodel.MetricDefinition{}, nil)

	out, err := suite.resolver(config.AuthModeOptional).ResolveMeasurementsEventPayload(
		context.Background(), deviceWithToken("TEST-123"), nil, measurementEvent("humidity", "55"))

	assert.NoError(suite.T(), err)
	entry := out.(*dmodel.ResolvedMeasurementsPayload).Entries[0].Entries[0]
	assert.Nil(suite.T(), entry.Classifier)
	assert.Nil(suite.T(), entry.Unit)
	assert.Nil(suite.T(), entry.DataType)
	assert.Equal(suite.T(), "55", entry.Value)
}

// An undeclared NON-numeric measurement cannot land in the numeric column, so it is
// dropped rather than dead-lettering the whole event — its valid numeric siblings
// still resolve (ADR-016).
func (suite *EventResolverTestSuite) TestMeasurementUndeclaredNonNumericDropped() {
	suite.API.Mock.On("MetricDefinitionsByDeviceType").Return([]*dmodel.MetricDefinition{}, nil)

	event := &esmodel.UnresolvedEvent{
		Device:    "TEST-123",
		EventType: esmodel.Measurement,
		Payload: &esmodel.UnresolvedMeasurementsPayload{
			Entries: []esmodel.UnresolvedMeasurementsEntry{
				{Measurements: map[string]string{"temp": "42", "label": "hello"}},
			},
		},
	}

	out, err := suite.resolver(config.AuthModeOptional).ResolveMeasurementsEventPayload(
		context.Background(), deviceWithToken("TEST-123"), nil, event)

	assert.NoError(suite.T(), err)
	entries := out.(*dmodel.ResolvedMeasurementsPayload).Entries[0].Entries
	if assert.Len(suite.T(), entries, 1) {
		assert.Equal(suite.T(), "temp", entries[0].Name)
		assert.Equal(suite.T(), "42", entries[0].Value)
	}
}

// A declared but non-storable metric (STRING is device state, not a time-series
// metric) is dropped rather than dead-lettering the whole event — a defensive
// backstop, since creating such a definition is already rejected (ADR-016).
func (suite *EventResolverTestSuite) TestMeasurementDeclaredNonStorableDropped() {
	def := &dmodel.MetricDefinition{Model: gorm.Model{ID: 9}, MetricKey: "label", DataType: "STRING"}
	suite.API.Mock.On("MetricDefinitionsByDeviceType").Return([]*dmodel.MetricDefinition{def}, nil)

	event := &esmodel.UnresolvedEvent{
		Device:    "TEST-123",
		EventType: esmodel.Measurement,
		Payload: &esmodel.UnresolvedMeasurementsPayload{
			Entries: []esmodel.UnresolvedMeasurementsEntry{
				{Measurements: map[string]string{"temp": "42", "label": "hello"}},
			},
		},
	}

	out, err := suite.resolver(config.AuthModeOptional).ResolveMeasurementsEventPayload(
		context.Background(), deviceWithToken("TEST-123"), nil, event)

	assert.NoError(suite.T(), err)
	entries := out.(*dmodel.ResolvedMeasurementsPayload).Entries[0].Entries
	if assert.Len(suite.T(), entries, 1) {
		assert.Equal(suite.T(), "temp", entries[0].Name)
	}
}

// A tracked relationship builds a device with ID 1 as source and the given target.
func trackedRel(id uint, targetType string, targetToken string) dmodel.EntityRelationship {
	return dmodel.EntityRelationship{
		Model:       gorm.Model{ID: id},
		SourceType:  "device",
		SourceId:    1,
		TargetType:  targetType,
		TargetToken: targetToken,
	}
}

// An unassigned device resolves to exactly one anchorless event — the event
// belongs to the device and still persists/projects (ADR-013 addendum 2026-07-01).
func (suite *EventResolverTestSuite) TestUnassignedResolvesAnchorless() {
	suite.API.Mock.On("MetricDefinitionsByDeviceType").Return([]*dmodel.MetricDefinition{}, nil)
	suite.API.Mock.On("EntityRelationships").Return(
		&dmodel.EntityRelationshipSearchResults{Results: []dmodel.EntityRelationship{}}, nil)

	results, reason, err := suite.resolver(config.AuthModeOptional).HandleStandardEvent(
		context.Background(), deviceWithToken("TEST-123"), measurementEvent("temp", "42"))

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), uint(0), reason)
	assert.Len(suite.T(), results, 1)
	assert.Empty(suite.T(), results[0].Resolved.Anchors)
}

// A single assignment anchors the one resolved event on that relationship's target.
func (suite *EventResolverTestSuite) TestSingleAssignmentAnchored() {
	suite.API.Mock.On("MetricDefinitionsByDeviceType").Return([]*dmodel.MetricDefinition{}, nil)
	suite.API.Mock.On("EntityRelationships").Return(
		&dmodel.EntityRelationshipSearchResults{Results: []dmodel.EntityRelationship{
			trackedRel(7, "customer", "cust-3"),
		}}, nil)

	results, _, err := suite.resolver(config.AuthModeOptional).HandleStandardEvent(
		context.Background(), deviceWithToken("TEST-123"), measurementEvent("temp", "42"))

	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), results, 1)
	assert.Equal(suite.T(), []dmodel.ResolvedAnchor{
		{AnchorType: "customer", AnchorToken: "cust-3", RelationshipId: 7},
	}, results[0].Resolved.Anchors)
}

// Several assignments yield one event carrying ALL of them as anchors, so the
// event is queryable by every dimension (customer, area, asset) — ADR-013 addendum.
func (suite *EventResolverTestSuite) TestMultipleAssignmentsAllAnchored() {
	suite.API.Mock.On("MetricDefinitionsByDeviceType").Return([]*dmodel.MetricDefinition{}, nil)
	suite.API.Mock.On("EntityRelationships").Return(
		&dmodel.EntityRelationshipSearchResults{Results: []dmodel.EntityRelationship{
			trackedRel(5, "area", "area-9"),
			trackedRel(2, "customer", "cust-3"),
			trackedRel(8, "asset", "asset-1"),
		}}, nil)

	results, _, err := suite.resolver(config.AuthModeOptional).HandleStandardEvent(
		context.Background(), deviceWithToken("TEST-123"), measurementEvent("temp", "42"))

	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), results, 1)
	assert.ElementsMatch(suite.T(), []dmodel.ResolvedAnchor{
		{AnchorType: "area", AnchorToken: "area-9", RelationshipId: 5},
		{AnchorType: "customer", AnchorToken: "cust-3", RelationshipId: 2},
		{AnchorType: "asset", AnchorToken: "asset-1", RelationshipId: 8},
	}, results[0].Resolved.Anchors)
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
