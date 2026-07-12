// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// captureAttr records the device-attribute events emitted so a test can assert which
// attribute writes become dynamic-threshold facts (ADR-051 slice 4c-3).
type captureAttr struct {
	events []*DeviceAttributeEvent
}

func (c *captureAttr) PublishDeviceAttribute(_ context.Context, e *DeviceAttributeEvent) {
	c.events = append(c.events, e)
}

// newAttrEmitTestApi builds an in-memory Api with a device seeded so attribute writes
// resolve their owner, and returns the api plus the capture publisher.
func newAttrEmitTestApi(t *testing.T) (*Api, *captureAttr, context.Context) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&Device{}, &DeviceType{}, &DeviceProfile{}, &DeviceProfileVersion{},
		&MetricDefinition{}, &CommandDefinition{}, &DetectionRule{},
		&EntityAttribute{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	api := NewApi(&rdb.RdbManager{Database: db})
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "sensor"}); err != nil {
		t.Fatalf("seed type: %v", err)
	}
	if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "d1", DeviceTypeToken: "sensor"}); err != nil {
		t.Fatalf("seed device: %v", err)
	}

	cap := &captureAttr{}
	api.DeviceAttributePublisher = cap
	return api, cap, ctx
}

// setAttr is a small helper for the common set request.
func setAttr(t *testing.T, api *Api, ctx context.Context, scope, key, valueType, value string) {
	t.Helper()
	if _, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{
		EntityType: entity.TypeDevice.String(),
		Entity:     "d1",
		Scope:      scope,
		AttrKey:    key,
		ValueType:  valueType,
		Value:      strp(value),
	}); err != nil {
		t.Fatalf("set attribute: %v", err)
	}
}

// A numeric SHARED attribute on a device emits an upsert fact carrying the parsed value.
func TestSetAttribute_SharedDoubleEmitsUpsert(t *testing.T) {
	api, cap, ctx := newAttrEmitTestApi(t)
	setAttr(t, api, ctx, string(AttributeScopeShared), "maxTemp", string(AttributeValueDouble), "72.5")

	if assert.Len(t, cap.events, 1, "a numeric platform-set device attribute emits one fact") {
		ev := cap.events[0]
		assert.Equal(t, "d1", ev.DeviceToken)
		assert.Equal(t, "maxTemp", ev.AttrKey)
		assert.Equal(t, string(AttributeScopeShared), ev.Scope, "the fact carries its scope")
		assert.Equal(t, 72.5, ev.Value)
		assert.False(t, ev.Removed)
		assert.False(t, ev.UpdatedAt.IsZero(), "the fact carries the write time")
	}
}

// The same key set in BOTH eligible scopes emits two scope-distinct facts (the identity
// includes scope), and deleting one scope emits a removal naming only that scope — so the
// consumer keys per (device, scope, key) and one scope never annihilates the other.
func TestSetAttribute_SameKeyDistinctPerScope(t *testing.T) {
	api, cap, ctx := newAttrEmitTestApi(t)
	setAttr(t, api, ctx, string(AttributeScopeShared), "maxTemp", string(AttributeValueDouble), "100")
	setAttr(t, api, ctx, string(AttributeScopeServer), "maxTemp", string(AttributeValueDouble), "50")

	if assert.Len(t, cap.events, 2, "the same key in two scopes is two facts") {
		assert.Equal(t, string(AttributeScopeShared), cap.events[0].Scope)
		assert.Equal(t, 100.0, cap.events[0].Value)
		assert.Equal(t, string(AttributeScopeServer), cap.events[1].Scope)
		assert.Equal(t, 50.0, cap.events[1].Value)
	}
	cap.events = nil

	deleted, err := api.DeleteEntityAttribute(ctx, entity.TypeDevice.String(), "d1", string(AttributeScopeServer), "maxTemp")
	assert.NoError(t, err)
	assert.True(t, deleted)
	if assert.Len(t, cap.events, 1, "deleting one scope emits one scoped removal") {
		assert.True(t, cap.events[0].Removed)
		assert.Equal(t, string(AttributeScopeServer), cap.events[0].Scope, "the removal names only the deleted scope, not the sibling")
	}
}

// A numeric type with a nil value emits a removal (fail-closed: no stale number).
func TestSetAttribute_NumericNilValueEmitsRemoval(t *testing.T) {
	api, cap, ctx := newAttrEmitTestApi(t)
	if _, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{
		EntityType: entity.TypeDevice.String(), Entity: "d1",
		Scope: string(AttributeScopeShared), AttrKey: "maxTemp",
		ValueType: string(AttributeValueDouble), Value: nil,
	}); err != nil {
		t.Fatalf("set attribute: %v", err)
	}
	if assert.Len(t, cap.events, 1) {
		assert.True(t, cap.events[0].Removed, "a numeric type with no value emits a removal")
	}
}

// A LONG attribute is numeric too: its integer text parses to a float64 value.
func TestSetAttribute_ServerLongEmitsUpsert(t *testing.T) {
	api, cap, ctx := newAttrEmitTestApi(t)
	setAttr(t, api, ctx, string(AttributeScopeServer), "maxRpm", string(AttributeValueLong), "3000")

	if assert.Len(t, cap.events, 1) {
		assert.Equal(t, 3000.0, cap.events[0].Value)
		assert.False(t, cap.events[0].Removed)
	}
}

// A CLIENT-scope attribute is device-reported: it must NOT become a threshold source, so
// nothing is emitted (a device cannot set the limit it is evaluated against).
func TestSetAttribute_ClientScopeDoesNotEmit(t *testing.T) {
	api, cap, ctx := newAttrEmitTestApi(t)
	setAttr(t, api, ctx, string(AttributeScopeClient), "maxTemp", string(AttributeValueDouble), "72.5")
	assert.Empty(t, cap.events, "a CLIENT-scope attribute is not a dynamic-threshold source")
}

// A non-numeric attribute type emits a REMOVAL, so a previously-projected numeric value
// for the same key is dropped rather than left stale.
func TestSetAttribute_NonNumericTypeEmitsRemoval(t *testing.T) {
	api, cap, ctx := newAttrEmitTestApi(t)
	setAttr(t, api, ctx, string(AttributeScopeShared), "label", string(AttributeValueString), "hot")

	if assert.Len(t, cap.events, 1, "a platform-set device attribute always emits, numeric or not") {
		assert.True(t, cap.events[0].Removed, "a non-numeric value emits a removal")
		assert.Equal(t, "label", cap.events[0].AttrKey)
	}
}

// A numeric type whose value text does not parse also emits a removal (fail-closed: no
// stale number survives).
func TestSetAttribute_UnparseableNumberEmitsRemoval(t *testing.T) {
	api, cap, ctx := newAttrEmitTestApi(t)
	setAttr(t, api, ctx, string(AttributeScopeShared), "maxTemp", string(AttributeValueDouble), "not-a-number")

	if assert.Len(t, cap.events, 1) {
		assert.True(t, cap.events[0].Removed)
	}
}

// Overwriting a numeric attribute with a numeric value re-upserts (last write wins); the
// second write carries a not-earlier timestamp than the first (real-time monotonic).
func TestSetAttribute_OverwriteReUpserts(t *testing.T) {
	api, cap, ctx := newAttrEmitTestApi(t)
	setAttr(t, api, ctx, string(AttributeScopeShared), "maxTemp", string(AttributeValueDouble), "72.5")
	setAttr(t, api, ctx, string(AttributeScopeShared), "maxTemp", string(AttributeValueDouble), "80")

	if assert.Len(t, cap.events, 2, "each write emits") {
		assert.Equal(t, 72.5, cap.events[0].Value)
		assert.Equal(t, 80.0, cap.events[1].Value)
		assert.False(t, cap.events[1].UpdatedAt.Before(cap.events[0].UpdatedAt),
			"the later write's timestamp is not earlier (monotonic ordering)")
	}
}

// Deleting a tracked device attribute emits a removal.
func TestDeleteAttribute_EmitsRemoval(t *testing.T) {
	api, cap, ctx := newAttrEmitTestApi(t)
	setAttr(t, api, ctx, string(AttributeScopeShared), "maxTemp", string(AttributeValueDouble), "72.5")
	cap.events = nil

	deleted, err := api.DeleteEntityAttribute(ctx, entity.TypeDevice.String(), "d1", string(AttributeScopeShared), "maxTemp")
	assert.NoError(t, err)
	assert.True(t, deleted)
	if assert.Len(t, cap.events, 1, "deleting a tracked attribute emits a removal") {
		assert.True(t, cap.events[0].Removed)
		assert.Equal(t, "maxTemp", cap.events[0].AttrKey)
	}
}

// Deleting an attribute that does not exist changes no state and emits nothing.
func TestDeleteAttribute_NoRowNoEmit(t *testing.T) {
	api, cap, ctx := newAttrEmitTestApi(t)
	deleted, err := api.DeleteEntityAttribute(ctx, entity.TypeDevice.String(), "d1", string(AttributeScopeShared), "ghost")
	assert.NoError(t, err)
	assert.False(t, deleted)
	assert.Empty(t, cap.events, "deleting a missing attribute emits nothing")
}

// Deleting a CLIENT-scope attribute (never a threshold source) emits nothing even though
// a row is removed.
func TestDeleteAttribute_ClientScopeDoesNotEmit(t *testing.T) {
	api, cap, ctx := newAttrEmitTestApi(t)
	setAttr(t, api, ctx, string(AttributeScopeClient), "reported", string(AttributeValueDouble), "1")
	cap.events = nil

	deleted, err := api.DeleteEntityAttribute(ctx, entity.TypeDevice.String(), "d1", string(AttributeScopeClient), "reported")
	assert.NoError(t, err)
	assert.True(t, deleted, "the row is still deleted")
	assert.Empty(t, cap.events, "but a CLIENT-scope delete emits no threshold fact")
}

// With no publisher wired, an attribute set still succeeds — emission is a nil-safe no-op.
func TestSetAttribute_NoPublisherIsNoOp(t *testing.T) {
	api, _, ctx := newAttrEmitTestApi(t)
	api.DeviceAttributePublisher = nil
	_, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{
		EntityType: entity.TypeDevice.String(), Entity: "d1",
		Scope: string(AttributeScopeShared), AttrKey: "maxTemp",
		ValueType: string(AttributeValueDouble), Value: strp("72.5"),
	})
	assert.NoError(t, err)
}

// dynamicThresholdEligible unit-checks the entity-type / scope gate directly, including a
// non-device owner (which the integration tests can't easily seed).
func TestDynamicThresholdEligible(t *testing.T) {
	dev := entity.TypeDevice.String()
	assert.True(t, dynamicThresholdEligible(dev, string(AttributeScopeShared)))
	assert.True(t, dynamicThresholdEligible(dev, string(AttributeScopeServer)))
	assert.False(t, dynamicThresholdEligible(dev, string(AttributeScopeClient)))
	assert.False(t, dynamicThresholdEligible("customer", string(AttributeScopeShared)), "only device attributes source a threshold")
	assert.False(t, dynamicThresholdEligible(dev, "BOGUS"))
}
