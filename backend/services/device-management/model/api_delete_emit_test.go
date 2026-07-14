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

// captureEntityPublisher records the entity-deletion events emitted, so a test can
// assert the delete path published them (ADR-044).
type captureEntityPublisher struct{ events []*EntityDeletedEvent }

func (c *captureEntityPublisher) PublishEntityDeleted(_ context.Context, e *EntityDeletedEvent) {
	c.events = append(c.events, e)
}

func newDeleteEmitTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	// deleteEdgeEntity resolves the token, then cascades relationship edges,
	// attributes, alarms, and (for a device) credentials before the row — so every
	// table it touches must exist.
	if err := db.AutoMigrate(&Device{}, &DeviceType{}, &DeviceCredential{},
		&EntityRelationship{}, &EntityAttribute{}, &Alarm{}, &EntityGroupMembership{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

// DeleteDevice (via deleteEdgeEntity) emits an entity-deletion event on success —
// the whole point of ADR-044 decision 2 — and emits nothing when the token resolves
// to no entity or when no publisher is wired.
func TestDeleteDevice_EmitsEntityDeleted(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	capture := &captureEntityPublisher{}
	api.EntityDeletedPublisher = capture

	dev := &Device{}
	dev.Token = "dev-1"
	if err := api.RDB.DB(ctx).Create(dev).Error; err != nil {
		t.Fatalf("seed device: %v", err)
	}

	deleted, err := api.DeleteDevice(ctx, "dev-1")
	assert.NoError(t, err)
	assert.True(t, deleted)
	if assert.Len(t, capture.events, 1, "a successful delete emits exactly one event") {
		assert.Equal(t, entity.TypeDevice, capture.events[0].EntityType)
		assert.Equal(t, dev.ID, capture.events[0].EntityId)
		assert.Equal(t, "dev-1", capture.events[0].EntityToken)
		assert.False(t, capture.events[0].DeletedTime.IsZero(), "deleted time is stamped")
	}

	// A delete of an unknown token resolves to nothing → no emit.
	deleted, err = api.DeleteDevice(ctx, "ghost")
	assert.NoError(t, err)
	assert.False(t, deleted)
	assert.Len(t, capture.events, 1, "no emit for a token that matches nothing")
}

// captureEvictor records eviction calls.
type evictCall struct {
	etype   entity.Type
	id      uint
	token   string
	sources []uint
}
type membershipEvict struct {
	entityType string
	entityIds  []uint
}
type captureEvictor struct {
	calls            []evictCall
	relSources       [][]uint
	membershipEvicts []membershipEvict
}

func (c *captureEvictor) EvictEntityDelete(_ context.Context, etype entity.Type, id uint, token string, sources []uint) {
	c.calls = append(c.calls, evictCall{etype, id, token, sources})
}

func (c *captureEvictor) EvictRelationshipSources(_ context.Context, sourceDeviceIds []uint) {
	c.relSources = append(c.relSources, sourceDeviceIds)
}

func (c *captureEvictor) EvictMemberships(_ context.Context, entityType string, entityIds []uint) {
	c.membershipEvicts = append(c.membershipEvicts, membershipEvict{entityType, entityIds})
}

// Deleting an entity evicts the hot-path caches (ADR-044 F2): the deleted device's
// own keys, plus the relationship cache of every device that tracked it as a target.
func TestDeleteDevice_EvictsCaches(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	evictor := &captureEvictor{}
	api.CacheEvictor = evictor

	// Device A (the target) and device B, which tracks A.
	a := &Device{}
	a.Token = "dev-a"
	b := &Device{}
	b.Token = "dev-b"
	if err := api.RDB.DB(ctx).Create(a).Error; err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := api.RDB.DB(ctx).Create(b).Error; err != nil {
		t.Fatalf("seed b: %v", err)
	}
	rel := &EntityRelationship{SourceType: "device", SourceId: b.ID, TargetType: "device", TargetId: a.ID}
	rel.Token = "rel-1"
	if err := api.RDB.DB(ctx).Create(rel).Error; err != nil {
		t.Fatalf("seed relationship: %v", err)
	}

	deleted, err := api.DeleteDevice(ctx, "dev-a")
	assert.NoError(t, err)
	assert.True(t, deleted)
	if assert.Len(t, evictor.calls, 1) {
		call := evictor.calls[0]
		assert.Equal(t, entity.TypeDevice, call.etype)
		assert.Equal(t, a.ID, call.id)
		assert.Equal(t, "dev-a", call.token)
		assert.Equal(t, []uint{b.ID}, call.sources, "device B tracked A as a target, so its relationship cache is evicted")
	}
}

// Deleting a NON-device anchor target (a customer tracked by a device) evicts the
// tracking device's relationship cache but touches no device-by-token entry.
func TestDeleteCustomer_EvictsTrackers(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	if err := api.RDB.DB(context.Background()).AutoMigrate(&Customer{}); err != nil {
		t.Fatalf("migrate customer: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	evictor := &captureEvictor{}
	api.CacheEvictor = evictor

	cust := &Customer{}
	cust.Token = "acme-corp"
	dev := &Device{}
	dev.Token = "dev-x"
	if err := api.RDB.DB(ctx).Create(cust).Error; err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if err := api.RDB.DB(ctx).Create(dev).Error; err != nil {
		t.Fatalf("seed device: %v", err)
	}
	rel := &EntityRelationship{SourceType: "device", SourceId: dev.ID, TargetType: "customer", TargetId: cust.ID}
	rel.Token = "rel-c"
	if err := api.RDB.DB(ctx).Create(rel).Error; err != nil {
		t.Fatalf("seed relationship: %v", err)
	}

	deleted, err := api.DeleteCustomer(ctx, "acme-corp")
	assert.NoError(t, err)
	assert.True(t, deleted)
	if assert.Len(t, evictor.calls, 1) {
		assert.Equal(t, entity.TypeCustomer, evictor.calls[0].etype)
		assert.Equal(t, []uint{dev.ID}, evictor.calls[0].sources, "the tracking device's cache is evicted")
	}
}

// Removing a relationship (unassign) evicts the source device's relationship cache
// — the target survives, so nothing else repairs the stale set (ADR-044 F2 / F-1).
func TestRemoveEntityRelationship_EvictsSource(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	evictor := &captureEvictor{}
	api.CacheEvictor = evictor

	rel := &EntityRelationship{SourceType: "device", SourceId: 77, TargetType: "customer", TargetId: 3}
	rel.Token = "rel-u"
	if err := api.RDB.DB(ctx).Create(rel).Error; err != nil {
		t.Fatalf("seed relationship: %v", err)
	}

	removed, err := api.RemoveEntityRelationship(ctx, "rel-u")
	assert.NoError(t, err)
	assert.True(t, removed)
	if assert.Len(t, evictor.relSources, 1) {
		assert.Equal(t, []uint{77}, evictor.relSources[0])
	}
}

// With no publisher wired the delete still succeeds — emission is a nil-safe no-op.
func TestDeleteDevice_NoPublisherIsNoOp(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	dev := &Device{}
	dev.Token = "dev-2"
	if err := api.RDB.DB(ctx).Create(dev).Error; err != nil {
		t.Fatalf("seed device: %v", err)
	}
	deleted, err := api.DeleteDevice(ctx, "dev-2")
	assert.NoError(t, err)
	assert.True(t, deleted)
}
