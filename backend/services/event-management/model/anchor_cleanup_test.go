// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// DeleteAnchorsForEntity (ADR-044) removes a deleted entity's anchors: a device by
// device_token (it is the anchor SOURCE), an anchor target (customer/area/...) by
// (anchor_type, anchor_token). It is idempotent and tenant-scoped.
func TestDeleteAnchorsForEntity(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 7, 6, 20, 0, 0, 0, time.UTC)

	if err := api.CreateEventAnchors(ctx, api.RDB.DB(ctx), []*EventAnchor{
		{EventId: anchorEventId("device-4", occurred), DeviceToken: "device-4", EventType: eventtype.Measurement, OccurredTime: occurred, AnchorType: "customer", AnchorToken: "cust-3"},
		{EventId: anchorEventId("device-4", occurred), DeviceToken: "device-4", EventType: eventtype.Measurement, OccurredTime: occurred, AnchorType: "area", AnchorToken: "area-9"},
		{EventId: anchorEventId("device-7", occurred), DeviceToken: "device-7", EventType: eventtype.Measurement, OccurredTime: occurred, AnchorType: "customer", AnchorToken: "cust-3"},
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
	n, err := api.DeleteAnchorsForEntity(ctx, "device", "device-4", time.Time{})
	assert.NoError(t, err)
	assert.Equal(t, int64(2), n)
	assert.Equal(t, int64(1), count())

	// Deleting a CUSTOMER removes rows targeting it — device 7's remaining anchor.
	n, err = api.DeleteAnchorsForEntity(ctx, "customer", "cust-3", time.Time{})
	assert.NoError(t, err)
	assert.Equal(t, int64(1), n)
	assert.Equal(t, int64(0), count())

	// Idempotent: deleting an already-absent entity removes nothing, no error.
	n, err = api.DeleteAnchorsForEntity(ctx, "device", "device-4", time.Time{})
	assert.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

// A deleted device is cleaned both as an anchor SOURCE (device_token) and as an anchor
// TARGET — a tracked device→device relationship (gateway→sensor) records rows with
// (anchor_type="device", anchor_token=other) on the tracker's events.
func TestDeleteAnchorsForEntity_DeviceAsTarget(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 7, 6, 20, 0, 0, 0, time.UTC)

	if err := api.CreateEventAnchors(ctx, api.RDB.DB(ctx), []*EventAnchor{
		// Device 7 (a gateway) tracks device 4 (a sensor): device 7's events anchor
		// to device 4 as a target.
		{EventId: anchorEventId("device-7", occurred), DeviceToken: "device-7", EventType: eventtype.Measurement, OccurredTime: occurred, AnchorType: "device", AnchorToken: "device-4"},
		// Device 4's own event anchored to its customer.
		{EventId: anchorEventId("device-4", occurred), DeviceToken: "device-4", EventType: eventtype.Measurement, OccurredTime: occurred, AnchorType: "customer", AnchorToken: "cust-3"},
		// An unrelated device's anchor that must survive.
		{EventId: anchorEventId("device-9", occurred), DeviceToken: "device-9", EventType: eventtype.Measurement, OccurredTime: occurred, AnchorType: "customer", AnchorToken: "cust-3"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Deleting device 4 must remove BOTH its source row (device_id=4) and the row
	// where it is device 7's target (anchor device=4) — two rows.
	n, err := api.DeleteAnchorsForEntity(ctx, "device", "device-4", time.Time{})
	assert.NoError(t, err)
	assert.Equal(t, int64(2), n)

	var remaining int64
	api.RDB.DB(ctx).Model(&EventAnchor{}).Count(&remaining)
	assert.Equal(t, int64(1), remaining, "only the unrelated device's anchor survives")
}

// A delete acting in one tenant must never touch another tenant's anchors.
func TestDeleteAnchorsForEntity_TenantScoped(t *testing.T) {
	api := newPersistenceTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	occurred := time.Now().UTC()
	if err := api.CreateEventAnchors(acme, api.RDB.DB(acme), []*EventAnchor{
		{EventId: anchorEventId("device-4", occurred), DeviceToken: "device-4", EventType: eventtype.Measurement, OccurredTime: occurred, AnchorType: "customer", AnchorToken: "cust-3"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	other := core.WithTenant(context.Background(), "other")
	n, err := api.DeleteAnchorsForEntity(other, "device", "device-4", time.Time{})
	assert.NoError(t, err)
	assert.Equal(t, int64(0), n, "must not delete another tenant's anchors")

	var remaining int64
	api.RDB.DB(acme).Model(&EventAnchor{}).Count(&remaining)
	assert.Equal(t, int64(1), remaining, "acme's anchor must survive another tenant's delete")
}

// Missing parents use the occurred-time fallback. The sweep passes a zero time
// (unbounded) and is covered by the cases above.
func TestDeleteAnchorsForEntity_BoundedByBefore(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	deletedAt := time.Date(2026, 7, 6, 20, 0, 0, 0, time.UTC)
	oldEvent := deletedAt.Add(-time.Hour) // the deleted device's telemetry
	newEvent := deletedAt.Add(time.Hour)  // a reused-token device's telemetry

	if err := api.CreateEventAnchors(ctx, api.RDB.DB(ctx), []*EventAnchor{
		{EventId: anchorEventId("gw-1", oldEvent), DeviceToken: "gw-1", EventType: eventtype.Measurement, OccurredTime: oldEvent, AnchorType: "customer", AnchorToken: "cust-3"},
		{EventId: anchorEventId("gw-1", newEvent), DeviceToken: "gw-1", EventType: eventtype.Measurement, OccurredTime: newEvent, AnchorType: "customer", AnchorToken: "cust-3"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Replay of the old device's deletion event: bound the cleanup to before its
	// deletion time — only the pre-deletion anchor is removed.
	n, err := api.DeleteAnchorsForEntity(ctx, "device", "gw-1", deletedAt)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), n, "only the anchor older than the deletion is removed")

	var remaining int64
	api.RDB.DB(ctx).Model(&EventAnchor{}).Count(&remaining)
	assert.Equal(t, int64(1), remaining, "the reused token's newer anchor must survive")
}

// anchorEventId derives the event identity an anchor fixture implies, so seeded anchors for
// different devices stay distinct under uq_event_anchors_idem exactly as they do in
// production (where event_id encodes the device). Before the anchor set had an identity
// these fixtures could omit it; now omitting it makes two devices' anchors collide.
func anchorEventId(device string, occurred time.Time) []byte {
	ev := Event{DeviceToken: device, EventType: eventtype.Measurement, OccurredTime: occurred}
	return DeriveEventId("acme", &ev, nil)
}

func TestDeleteAnchorsForEntity_ProcessedTime(t *testing.T) {
	assertAnchorCleanupByReceiptTime(t, newPersistenceTestApi(t))
}

// Shared with the real hypertable test so both engines must preserve exactly the
// same anchors when an old deletion arrives after a token has been reused.
func assertAnchorCleanupByReceiptTime(t *testing.T, api *Api) {
	t.Helper()
	for _, target := range []struct {
		name, entityType, entityToken, device, anchorType, anchorToken string
	}{
		{"device_source", "device", "pump-7", "pump-7", "customer", "cust-7"},
		{"customer_target", "customer", "cust-7", "pump-7", "customer", "cust-7"},
		{"device_target", "device", "pump-7", "tracker", "device", "pump-7"},
	} {
		t.Run(target.name, func(t *testing.T) {
			ctx := core.WithTenant(context.Background(), "receipt-"+target.name)
			deletedAt := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
			var keep [][]byte
			for _, fixture := range []struct {
				name                string
				occurred, processed time.Duration
				missing, survives   bool
			}{
				{"old", -time.Hour, -time.Hour, false, false},
				{"buffered", -2 * time.Hour, 5 * time.Minute, false, true},
				{"at_cutoff", -3 * time.Hour, 0, false, true},
				{"clock_ahead", time.Hour, -time.Hour, false, false},
				{"orphan_old", -4 * time.Hour, 0, true, false},
				{"orphan_new", 2 * time.Hour, 0, true, true},
			} {
				event := Event{
					DeviceToken: target.device, EventType: eventtype.Measurement,
					OccurredTime:  deletedAt.Add(fixture.occurred),
					ProcessedTime: deletedAt.Add(fixture.processed), Source: "mqtt",
				}
				event.EventId = DeriveEventId("receipt-"+target.name, &event, []byte(fixture.name))
				if !fixture.missing {
					require.NoError(t, api.RDB.DB(ctx).Create(&event).Error)
				}
				require.NoError(t, api.CreateEventAnchors(ctx, api.RDB.DB(ctx), []*EventAnchor{{
					EventId: event.EventId, DeviceToken: event.DeviceToken, EventType: event.EventType,
					OccurredTime: event.OccurredTime, AnchorType: target.anchorType, AnchorToken: target.anchorToken,
				}}))
				if fixture.survives {
					keep = append(keep, event.EventId)
				}
			}

			removed, err := api.DeleteAnchorsForEntity(ctx, target.entityType, target.entityToken, deletedAt)
			require.NoError(t, err)
			assert.Equal(t, int64(3), removed)
			var remaining [][]byte
			require.NoError(t, api.RDB.DB(ctx).Model(&EventAnchor{}).Pluck("event_id", &remaining).Error)
			assert.ElementsMatch(t, keep, remaining, "buffered, boundary and newer orphan anchors must survive")

			removed, err = api.DeleteAnchorsForEntity(ctx, target.entityType, target.entityToken, deletedAt)
			require.NoError(t, err)
			assert.Zero(t, removed, "redelivery is idempotent")
			removed, err = api.DeleteAnchorsForEntity(ctx, target.entityType, target.entityToken, time.Time{})
			require.NoError(t, err)
			assert.Equal(t, int64(3), removed, "the sweep remains unbounded")
			var parents int64
			require.NoError(t, api.RDB.DB(ctx).Model(&Event{}).Count(&parents).Error)
			assert.Equal(t, int64(4), parents, "cleanup only removes anchors")
		})
	}
}

// An event RECEIVED before an entity's deletion was resolved against the old entity, but the
// pipeline may persist it AFTER the deletion cleanup has run (lag, a backlog, a redelivery, an
// outage here). Its anchors must not come back: a re-created entity with the same token would
// otherwise read the old one's events through them. Anchors of events received at or after the
// deletion (the re-created entity's) are kept. The same predicate as the cleanup itself.
func TestAnchorsPersistedAfterDeletionCleanupAreNotReinserted(t *testing.T) {
	assertAnchorsPersistedAfterCleanupAreDropped(t, newPersistenceTestApi(t))
}

func assertAnchorsPersistedAfterCleanupAreDropped(t *testing.T, api *Api) {
	t.Helper()
	for _, target := range []struct {
		name, entityType, entityToken, device, anchorType, anchorToken string
	}{
		{"device_source", "device", "pump-8", "pump-8", "customer", "cust-8"},
		{"customer_target", "customer", "cust-8", "pump-8", "customer", "cust-8"},
		{"device_target", "device", "pump-8", "tracker", "device", "pump-8"},
	} {
		t.Run(target.name, func(t *testing.T) {
			tenant := "late-" + target.name
			ctx := core.WithTenant(context.Background(), tenant)
			deletedAt := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

			// The deletion fact is handled first, while nothing of the old entity is stored yet.
			removed, err := api.DeleteAnchorsForEntity(ctx, target.entityType, target.entityToken, deletedAt)
			require.NoError(t, err)
			require.Zero(t, removed)

			var keep [][]byte
			for _, fixture := range []struct {
				name                string
				occurred, processed time.Duration
				survives            bool
			}{
				{"received_before", -time.Hour, -time.Minute, false},
				{"buffered_old_clock", -2 * time.Hour, -time.Second, false},
				{"at_cutoff", -3 * time.Hour, 0, true},
				{"recreated", time.Minute, time.Minute, true},
			} {
				event := Event{
					DeviceToken: target.device, EventType: eventtype.Measurement,
					OccurredTime:  deletedAt.Add(fixture.occurred),
					ProcessedTime: deletedAt.Add(fixture.processed), Source: "mqtt",
				}
				event.EventId = DeriveEventId(tenant, &event, []byte(fixture.name))
				require.NoError(t, api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
					if err := tx.Create(&event).Error; err != nil {
						return err
					}
					return api.CreateEventAnchors(ctx, tx, []*EventAnchor{{
						EventId: event.EventId, DeviceToken: event.DeviceToken, EventType: event.EventType,
						OccurredTime: event.OccurredTime, AnchorType: target.anchorType, AnchorToken: target.anchorToken,
					}})
				}))
				if fixture.survives {
					keep = append(keep, event.EventId)
				}
			}
			var remaining [][]byte
			require.NoError(t, api.RDB.DB(ctx).Model(&EventAnchor{}).Pluck("event_id", &remaining).Error)
			assert.ElementsMatch(t, keep, remaining,
				"an anchor of an event received before the deletion must not be re-inserted after its cleanup; "+
					"one received at or after it (the re-created entity's) must be kept")

			// Another tenant's same-token entity is untouched by this tenant's deletion.
			other := core.WithTenant(context.Background(), tenant+"-other")
			event := Event{DeviceToken: target.device, EventType: eventtype.Measurement,
				OccurredTime: deletedAt.Add(-time.Hour), ProcessedTime: deletedAt.Add(-time.Hour), Source: "mqtt"}
			event.EventId = DeriveEventId(tenant+"-other", &event, []byte("other"))
			require.NoError(t, api.RDB.DB(other).Transaction(func(tx *gorm.DB) error {
				if err := tx.Create(&event).Error; err != nil {
					return err
				}
				return api.CreateEventAnchors(other, tx, []*EventAnchor{{
					EventId: event.EventId, DeviceToken: event.DeviceToken, EventType: event.EventType,
					OccurredTime: event.OccurredTime, AnchorType: target.anchorType, AnchorToken: target.anchorToken,
				}})
			}))
			var n int64
			require.NoError(t, api.RDB.DB(other).Model(&EventAnchor{}).Count(&n).Error)
			assert.Equal(t, int64(1), n, "a deletion is scoped to its own tenant")
		})
	}
}
