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
		&EntityRelationship{}, &EntityAttribute{}, &Alarm{}); err != nil {
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
