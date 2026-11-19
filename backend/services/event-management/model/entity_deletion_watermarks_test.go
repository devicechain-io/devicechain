// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestDeletionResweepSchedule(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.FixedZone("offset", 3600))
	require.Nil(t, resweepSchedule(false, now), "clear when no older transaction can persist late anchors")
	after := resweepSchedule(true, now)
	require.NotNil(t, after, "an in-flight writer must keep the resweep armed")
	assert.Equal(t, now.UTC().Add(time.Minute), *after)
}

func TestDeletionWatermarkMonotonicAcrossOffsets(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "offset")
	cutoff := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	older := time.Date(2026, 11, 10, 12, 30, 0, 0, time.FixedZone("east", 3600))
	for _, when := range []time.Time{cutoff, older} {
		_, err := api.DeleteAnchorsForEntity(ctx, "area", "floor", when)
		require.NoError(t, err)
	}
	var w EntityDeletionWatermark
	require.NoError(t, api.RDB.DB(ctx).Take(&w).Error)
	assert.True(t, w.DeletedTime.Equal(cutoff), "12:30+01:00 predates 12:00Z")
	_, offset := w.DeletedTime.Zone()
	assert.Zero(t, offset)
	require.NotNil(t, w.ResweepAfter)
	_, offset = w.ResweepAfter.Zone()
	assert.Zero(t, offset)
}

func TestDeletionFilterSQLBoundsAndCandidatePairs(t *testing.T) {
	for _, dialect := range []gorm.Dialector{sqlite.Open(":memory:"), postgres.Open("host=localhost user=test dbname=test sslmode=disable")} {
		t.Run(dialect.Name(), func(t *testing.T) {
			db, err := gorm.Open(dialect, &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
			require.NoError(t, err)
			at := time.Date(2026, 11, 10, 12, 0, 0, 123456000, time.UTC)
			anchors := []*EventAnchor{{OccurredTime: at.Add(time.Hour), EventId: []byte{0xab}}, {OccurredTime: at, EventId: []byte{0xcd}}}
			q, err := anchorCleanupCandidates(anchorCleanupQuery(db, "area", "floor", at), anchors)
			require.NoError(t, err)
			stmt := q.Delete(&EventAnchor{}).Statement
			require.NoError(t, q.Error)
			sql := stmt.SQL.String()
			assert.Contains(t, sql, "event_anchors.occurred_time BETWEEN")
			assert.Contains(t, sql, "(event_anchors.occurred_time,")
			assert.Contains(t, sql, "events.processed_time <", "keep the shared cleanup policy")
			if dialect.Name() == "postgres" {
				assert.Contains(t, sql, "(p->>0)::timestamptz, decode(p->>1, 'hex')")
				assert.Contains(t, sql, "jsonb_array_elements(")
			} else {
				assert.Contains(t, sql, "json_extract(value, '$[0]'), json_extract(value, '$[1]')")
				assert.Contains(t, sql, "json_each(")
			}
			require.GreaterOrEqual(t, len(stmt.Vars), 3)
			n := len(stmt.Vars)
			assert.Equal(t, at, stmt.Vars[n-3])
			assert.Equal(t, at.Add(time.Hour), stmt.Vars[n-2])
			var pairs [][2]string
			require.NoError(t, json.Unmarshal([]byte(stmt.Vars[n-1].(string)), &pairs))
			require.Len(t, pairs, 2)
			assert.Equal(t, "ab", pairs[0][1])
			assert.Equal(t, "cd", pairs[1][1])
			if dialect.Name() == "postgres" {
				assert.Equal(t, at.Add(time.Hour).Format(time.RFC3339Nano), pairs[0][0])
			}
		})
	}
}

func TestDeletionFilterOnlyDeletesCandidateTimeIDPairs(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "pairs")
	cutoff := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	_, err := api.DeleteAnchorsForEntity(ctx, "area", "floor", cutoff)
	require.NoError(t, err)
	anchor := func(at time.Time) *EventAnchor {
		return &EventAnchor{EventId: []byte("same-id"), DeviceToken: "pump", EventType: eventtype.Measurement,
			OccurredTime: at, AnchorType: "area", AnchorToken: "floor"}
	}
	// One row outside the batch's bounds, another inside its bounds but not in its pairs.
	for _, at := range []time.Time{cutoff.Add(-4 * time.Hour), cutoff.Add(-2 * time.Hour)} {
		require.NoError(t, api.RDB.DB(ctx).Create(anchor(at)).Error)
	}
	require.NoError(t, api.CreateEventAnchors(ctx, api.RDB.DB(ctx), []*EventAnchor{
		anchor(cutoff.Add(-3 * time.Hour)), anchor(cutoff.Add(-time.Hour)),
	}))
	var left []EventAnchor
	require.NoError(t, api.RDB.DB(ctx).Order("occurred_time").Find(&left).Error)
	require.Len(t, left, 2)
	assert.True(t, left[0].OccurredTime.Equal(cutoff.Add(-4*time.Hour)))
	assert.True(t, left[1].OccurredTime.Equal(cutoff.Add(-2*time.Hour)))
}

func TestDeletionWatermarkReplayUsesStoredParent(t *testing.T) {
	for _, storedOld := range []bool{true, false} {
		t.Run(map[bool]string{true: "old_parent_new_receipt", false: "new_parent_old_receipt"}[storedOld], func(t *testing.T) {
			api := newPersistenceTestApi(t)
			ctx := core.WithTenant(context.Background(), "replay")
			cutoff := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
			stored, incoming := cutoff.Add(-time.Minute), cutoff.Add(time.Minute)
			if !storedOld {
				stored, incoming = incoming, stored
			}
			e := Event{DeviceToken: "pump", EventType: eventtype.Measurement, OccurredTime: cutoff.Add(-time.Hour), ProcessedTime: stored}
			e.EventId = DeriveEventId("replay", &e, []byte("reading"))
			require.NoError(t, upsertParentEvents(ctx, api.RDB.DB(ctx), []*Event{&e}))
			_, err := api.DeleteAnchorsForEntity(ctx, "area", "floor", cutoff)
			require.NoError(t, err)
			e.ProcessedTime = incoming
			require.NoError(t, api.PersistInTx(ctx, func(tx *gorm.DB) error {
				if err := upsertParentEvents(ctx, tx, []*Event{&e}); err != nil {
					return err
				}
				return api.CreateEventAnchors(ctx, tx, []*EventAnchor{{EventId: e.EventId, DeviceToken: e.DeviceToken,
					EventType: e.EventType, OccurredTime: e.OccurredTime, AnchorType: "area", AnchorToken: "floor"}})
			}))
			var n int64
			require.NoError(t, api.RDB.DB(ctx).Model(&EventAnchor{}).Count(&n).Error)
			assert.Equal(t, map[bool]int64{true: 0, false: 1}[storedOld], n)
			var parent Event
			require.NoError(t, api.RDB.DB(ctx).Take(&parent).Error)
			assert.True(t, parent.ProcessedTime.Equal(stored))
		})
	}
}

func TestDeletionWatermarkMonotonicAndMissingParent(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "monotonic")
	cutoff := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	for _, when := range []time.Time{cutoff, cutoff.Add(-time.Hour)} {
		_, err := api.DeleteAnchorsForEntity(ctx, "customer", "customer", when)
		require.NoError(t, err)
	}
	var w EntityDeletionWatermark
	require.NoError(t, api.RDB.DB(ctx).Take(&w).Error)
	assert.True(t, w.DeletedTime.Equal(cutoff), "older facts must not lower the watermark")
	require.NotNil(t, w.ResweepAfter)
	assert.WithinDuration(t, time.Now().Add(EntityDeletionResweepDelay), *w.ResweepAfter, time.Second)
	for i, at := range []time.Time{cutoff.Add(-time.Minute), cutoff, cutoff.Add(time.Minute)} {
		require.NoError(t, api.CreateEventAnchors(ctx, api.RDB.DB(ctx), []*EventAnchor{{
			EventId: []byte{byte(i)}, DeviceToken: "pump", EventType: eventtype.Measurement,
			OccurredTime: at, AnchorType: "customer", AnchorToken: "customer",
		}}))
	}
	var remaining []EventAnchor
	require.NoError(t, api.RDB.DB(ctx).Find(&remaining).Error)
	require.Len(t, remaining, 2, "missing-parent fallback is strict and uses the maximum deletion")
	for _, a := range remaining {
		assert.False(t, a.OccurredTime.Before(cutoff))
	}
	_, err := api.DeleteAnchorsForEntity(ctx, "customer", "customer", time.Time{})
	require.NoError(t, err)
	var after EntityDeletionWatermark
	require.NoError(t, api.RDB.DB(ctx).Take(&after).Error)
	assert.Equal(t, w, after, "unbounded orphan sweep must not write or re-arm a watermark")
	_, err = api.DeleteAnchorsForEntity(ctx, "device", "no-watermark", time.Time{})
	require.NoError(t, err)
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityDeletionWatermark{}).Count(&n).Error)
	assert.Equal(t, int64(1), n)
}

func TestDeletionResweepCleansLateAnchorsAndCAS(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "resweep")
	other := core.WithTenant(context.Background(), "other")
	cutoff := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	_, err := api.DeleteAnchorsForEntity(ctx, "area", "floor", cutoff)
	require.NoError(t, err)
	// An old writer, or a transaction that could not see the committed watermark.
	for _, tc := range []context.Context{ctx, other} {
		require.NoError(t, api.RDB.DB(tc).Create(&EventAnchor{EventId: []byte("late"), DeviceToken: "pump",
			EventType: eventtype.Measurement, OccurredTime: cutoff.Add(-time.Minute), AnchorType: "area", AnchorToken: "floor"}).Error)
	}
	dueAt := time.Now().Add(EntityDeletionResweepDelay + time.Minute)
	due, err := api.DueEntityDeletionWatermarks(context.Background(), time.Now().Add(-time.Second))
	require.NoError(t, err)
	require.Empty(t, due, "not due before the delay")
	due, err = api.DueEntityDeletionWatermarks(context.Background(), dueAt)
	require.NoError(t, err)
	require.Len(t, due, 1)
	require.NoError(t, api.ResweepEntityDeletionWatermark(context.Background(), due[0]))
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&EventAnchor{}).Count(&n).Error)
	assert.Zero(t, n)
	require.NoError(t, api.RDB.DB(other).Model(&EventAnchor{}).Count(&n).Error)
	assert.Equal(t, int64(1), n, "resweep must retain other tenants' anchors")
	var cleared EntityDeletionWatermark
	require.NoError(t, api.RDB.DB(ctx).Take(&cleared).Error)
	assert.Nil(t, cleared.ResweepAfter, "completed work must stop being selected")
	due, err = api.DueEntityDeletionWatermarks(context.Background(), dueAt)
	require.NoError(t, err)
	assert.Empty(t, due)
	// Re-arm AFTER selection. A stale worker must not clear the newer schedule.
	_, err = api.DeleteAnchorsForEntity(ctx, "area", "floor", cutoff.Add(time.Hour))
	require.NoError(t, err)
	var rearmed EntityDeletionWatermark
	require.NoError(t, api.RDB.DB(ctx).Take(&rearmed).Error)
	require.NotNil(t, rearmed.ResweepAfter)
	// Make the changed schedule deterministic even on a coarse test clock.
	newAfter := rearmed.ResweepAfter.Add(time.Minute)
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityDeletionWatermark{}).
		Where("entity_type = ? AND entity_token = ?", "area", "floor").Update("resweep_after", newAfter).Error)
	require.NoError(t, api.ResweepEntityDeletionWatermark(context.Background(), rearmed))
	var kept EntityDeletionWatermark
	require.NoError(t, api.RDB.DB(ctx).Take(&kept).Error)
	require.NotNil(t, kept.ResweepAfter)
	assert.True(t, kept.ResweepAfter.Equal(newAfter), "stale CAS must preserve the re-arm")
}

func TestDeletionWatermarkFailsClosed(t *testing.T) {
	api := newPersistenceTestApi(t)
	_, err := api.DeleteAnchorsForEntity(context.Background(), "area", "floor", time.Now())
	require.ErrorIs(t, err, core.ErrNoTenant)
}
