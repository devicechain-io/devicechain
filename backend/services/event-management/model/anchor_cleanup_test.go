// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
)

// DeleteAnchorsForEntity (ADR-044) removes a deleted entity's anchors: a device by
// device_id (it is the anchor SOURCE), an anchor target (customer/area/...) by
// (anchor_type, anchor_id). It is idempotent and tenant-scoped.
func TestDeleteAnchorsForEntity(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 7, 6, 20, 0, 0, 0, time.UTC)

	if err := api.CreateEventAnchors(ctx, api.RDB.DB(ctx), []*EventAnchor{
		{DeviceId: 4, EventType: esmodel.Measurement, OccurredTime: occurred, AnchorType: "customer", AnchorId: 3},
		{DeviceId: 4, EventType: esmodel.Measurement, OccurredTime: occurred, AnchorType: "area", AnchorId: 9},
		{DeviceId: 7, EventType: esmodel.Measurement, OccurredTime: occurred, AnchorType: "customer", AnchorId: 3},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	count := func() int64 {
		var n int64
		api.RDB.DB(ctx).Model(&EventAnchor{}).Count(&n)
		return n
	}
	assert.Equal(t, int64(3), count())

	// Deleting a DEVICE removes every anchor it sourced (its two rows), leaving
	// device 7's anchor untouched.
	n, err := api.DeleteAnchorsForEntity(ctx, "device", 4)
	assert.NoError(t, err)
	assert.Equal(t, int64(2), n)
	assert.Equal(t, int64(1), count())

	// Deleting a CUSTOMER removes rows targeting it — device 7's remaining anchor.
	n, err = api.DeleteAnchorsForEntity(ctx, "customer", 3)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), n)
	assert.Equal(t, int64(0), count())

	// Idempotent: deleting an already-absent entity removes nothing, no error.
	n, err = api.DeleteAnchorsForEntity(ctx, "device", 4)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

// A delete acting in one tenant must never touch another tenant's anchors.
func TestDeleteAnchorsForEntity_TenantScoped(t *testing.T) {
	api := newPersistenceTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	if err := api.CreateEventAnchors(acme, api.RDB.DB(acme), []*EventAnchor{
		{DeviceId: 4, EventType: esmodel.Measurement, OccurredTime: time.Now().UTC(), AnchorType: "customer", AnchorId: 3},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	other := core.WithTenant(context.Background(), "other")
	n, err := api.DeleteAnchorsForEntity(other, "device", 4)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), n, "must not delete another tenant's anchors")

	var remaining int64
	api.RDB.DB(acme).Model(&EventAnchor{}).Count(&remaining)
	assert.Equal(t, int64(1), remaining, "acme's anchor must survive another tenant's delete")
}
