// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/presence"
	"github.com/stretchr/testify/require"
)

func TestNewProjectionRowsCarryEventIncarnation(t *testing.T) {
	for _, path := range []string{"heartbeat", "locked", "per-event"} {
		t.Run(path, func(t *testing.T) {
			api := newSQLiteProjectionApi(t, "new-incarnation")
			ctx := core.WithTenant(context.Background(), "a")
			u := withIncarnation(deletionUpdate("a", "d", time.Now().UTC()), 23)
			if path == "locked" {
				u.Presence = &PresenceTransition{Claim: presence.ClaimConnected, SessionId: 1}
			}
			if path == "per-event" {
				u.Identity.Incarnation = u.Incarnation
				_, err := api.MergeDeviceState(ctx, "d", u.OccurredAt, u.Presence, u.Identity)
				require.NoError(t, err)
			} else {
				require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{u}))
			}
			var row DeviceState
			require.NoError(t, api.RDB.DB(ctx).First(&row).Error)
			require.Equal(t, eventIncarnation(23), row.Incarnation)
		})
	}
}

func TestSweepDeletesLateIncarnationRatherThanAdoptingIt(t *testing.T) {
	for _, liveID := range []int64{0, 23} {
		t.Run(fmt.Sprint(liveID), func(t *testing.T) {
			api := newSQLiteProjectionApi(t, "late-orphan")
			ctx := core.WithTenant(context.Background(), "a")
			require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{withIncarnation(deletionUpdate("a", "d", time.Now()), 17)}))
			var row DeviceState
			require.NoError(t, api.RDB.DB(ctx).First(&row).Error)
			deleted, stamped, err := api.ReconcileDeviceState(ctx, row, liveID, row.CreatedAt.Add(-time.Hour))
			require.NoError(t, err)
			require.True(t, deleted)
			require.False(t, stamped)
			requireLiveCounts(t, api, "a", "d", 0)
		})
	}
}

func TestOrphanReconcileSkipsAnIncarnationResetAfterScan(t *testing.T) {
	for _, liveID := range []int64{17, 0} {
		t.Run(fmt.Sprint(liveID), func(t *testing.T) {
			api := newSQLiteProjectionApi(t, "sweep-reset")
			ctx := core.WithTenant(context.Background(), "a")
			at := time.Now().UTC().Add(-time.Hour)
			require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{withIncarnation(deletionUpdate("a", "d", at), 17)}))
			var scanned DeviceState
			require.NoError(t, api.RDB.DB(ctx).First(&scanned).Error)
			require.Equal(t, eventIncarnation(17), scanned.Incarnation)

			require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{withIncarnation(deletionUpdate("a", "d", at.Add(time.Minute)), 23)}))
			var replacement DeviceState
			require.NoError(t, api.RDB.DB(ctx).First(&replacement).Error)
			require.Equal(t, scanned.ID, replacement.ID)
			require.Equal(t, eventIncarnation(23), replacement.Incarnation)

			deleted, stamped, err := api.ReconcileDeviceState(ctx, scanned, liveID, at)
			require.NoError(t, err)
			require.False(t, deleted)
			require.False(t, stamped)
			requireLiveCounts(t, api, "a", "d", 1)
			var after DeviceState
			require.NoError(t, api.RDB.DB(ctx).First(&after).Error)
			require.Equal(t, replacement, after)
		})
	}
}

func TestOrphanReconcileDeletesARowTheAuthorityNamesASmallerIdFor(t *testing.T) {
	api := newSQLiteProjectionApi(t, "sweep-smaller-live-id")
	ctx := core.WithTenant(context.Background(), "a")
	at := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{withIncarnation(deletionUpdate("a", "d", at), 23)}))
	var scanned DeviceState
	require.NoError(t, api.RDB.DB(ctx).First(&scanned).Error)
	require.Equal(t, eventIncarnation(23), scanned.Incarnation)
	requireLiveCounts(t, api, "a", "d", 1)

	deleted, stamped, err := api.ReconcileDeviceState(ctx, scanned, 17, at)
	require.NoError(t, err)
	require.True(t, deleted)
	require.False(t, stamped)
	requireLiveCounts(t, api, "a", "d", 0)
}

func TestProjectionIncarnationRules(t *testing.T) {
	for _, path := range []string{"heartbeat", "locked", "per-event"} {
		for _, tc := range []struct {
			name  string
			r, e  uint64
			want  uint64
			stale bool
		}{
			{"equal", 23, 23, 23, false},
			{"older", 23, 17, 23, true},
			{"newer", 17, 23, 23, false},
			{"null", 0, 23, 0, false},
			{"old-producer", 23, 0, 23, false},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				api := newSQLiteProjectionApi(t, "incarnation")
				ctx := core.WithTenant(context.Background(), "a")
				at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
				seed := withIncarnation(deletionUpdate("a", "d", at), tc.r)
				seed.Identity.ExternalId = "old"
				seed.Identity.Source = "old-source"
				seed.Presence = &PresenceTransition{Claim: presence.ClaimConnected, SessionId: 99, OccurredAt: at}
				require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{seed}))
				u := withIncarnation(deletionUpdate("a", "d", at.Add(time.Minute)), tc.e)
				u.Identity.ExternalId = "new"
				u.Measurements[0].Value.Float64 = 23
				u.Measurements[0].Name = "new-reading"
				u.Dropped[0].Name = "new-drop"
				u.Locations[0].Latitude.Float64 = 23
				if path == "locked" {
					u.Presence = &PresenceTransition{Claim: presence.ClaimDisconnected, SessionId: 1}
				}
				if path == "per-event" {
					u.Identity.Incarnation = tc.e
					_, err := api.MergeDeviceState(ctx, "d", u.OccurredAt, u.Presence, u.Identity)
					require.NoError(t, err)
				} else {
					require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{u}))
				}
				var row DeviceState
				require.NoError(t, api.RDB.DB(ctx).First(&row).Error)
				require.Equal(t, eventIncarnation(tc.want), row.Incarnation)
				wantID := "new"
				if tc.stale {
					wantID = "old"
				}
				require.Equal(t, wantID, row.ExternalId)
				if tc.stale {
					require.Equal(t, storedTime(at), row.LastActivityTime.Time)
					for _, table := range []any{&LatestMeasurement{}, &LatestLocation{}, &DroppedMeasurement{}} {
						var n int64
						require.NoError(t, api.RDB.DB(ctx).Model(table).Count(&n).Error)
						require.Equal(t, int64(1), n, "%T", table)
					}
					var old LatestMeasurement
					require.NoError(t, api.RDB.DB(ctx).First(&old).Error)
					require.Equal(t, float64(17), old.Value.Float64)
				} else if path != "per-event" {
					var reading LatestMeasurement
					require.NoError(t, api.RDB.DB(ctx).Where("name = ?", "new-reading").First(&reading).Error)
					require.Equal(t, float64(23), reading.Value.Float64)
					var fix LatestLocation
					require.NoError(t, api.RDB.DB(ctx).First(&fix).Error)
					require.Equal(t, float64(23), fix.Latitude.Float64)
				}
				if tc.r == 17 && tc.e == 23 {
					require.Empty(t, row.Source, "reset must discard the old device's transport identity")
					if path == "locked" {
						require.Equal(t, uint64(1), row.SessionId, "old device's higher session must not fence the new device")
					} else {
						require.Zero(t, row.SessionId)
						require.Equal(t, PresenceSourceInferred, row.PresenceSource)
					}
					// A reset discards every old child even when the new event has
					// no replacement for that key (or is a state-only event).
					for _, check := range []struct {
						table any
						key   string
					}{{&LatestMeasurement{}, "temperature"}, {&DroppedMeasurement{}, "text"}} {
						var n int64
						require.NoError(t, api.RDB.DB(ctx).Model(check.table).Where("name = ?", check.key).Count(&n).Error)
						require.Zero(t, n)
					}
				}
			})
		}
	}
}

func TestOwnershipIncarnationRules(t *testing.T) {
	for _, tc := range []struct {
		r, f          uint64
		gone, applies bool
	}{
		{23, 17, false, false}, {17, 23, true, true}, {23, 23, false, true}, {23, 0, false, true}, {0, 23, false, true},
	} {
		t.Run(fmt.Sprintf("%d/%d", tc.r, tc.f), func(t *testing.T) {
			api := newSQLiteProjectionApi(t, "ownership-incarnation")
			ctx := core.WithTenant(context.Background(), "a")
			at := time.Now().UTC().Add(-time.Hour)
			require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{withIncarnation(deletionUpdate("a", "d", at), tc.r)}))
			changedAt := at.Add(time.Minute)
			if tc.gone {
				changedAt = at.Add(-time.Hour)
			}
			applied, err := api.ApplyDeviceOwnership(ctx, "d", []string{"customer:new"}, changedAt, tc.f)
			require.NoError(t, err)
			require.Equal(t, tc.applies, applied)
			if tc.gone {
				requireLiveCounts(t, api, "a", "d", 0)
				return
			}
			var row DeviceState
			require.NoError(t, api.RDB.DB(ctx).First(&row).Error)
			want := "customer:old-owner"
			if tc.applies {
				want = "customer:new"
			}
			require.Equal(t, ownerSet([]string{want}), row.ScopeAnchors)
			require.Equal(t, eventIncarnation(tc.r), row.Incarnation)
		})
	}
}

func TestLateDeletedEventIsStampedAndReplaced(t *testing.T) {
	api := newSQLiteProjectionApi(t, "late-incarnation")
	assertLateDeletedEventIsStampedAndReplaced(t, api, false)
}

func assertLateDeletedEventIsStampedAndReplaced(t *testing.T, api *Api, scopedRead bool) {
	t.Helper()
	ctx := core.WithTenant(context.Background(), "a")
	at := time.Now().UTC().Add(-time.Hour)
	old := withIncarnation(deletionUpdate("a", "d", at), 17)
	require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{old}))
	_, err := api.ApplyDeviceDeleted(ctx, "d", 17, at.Add(time.Minute))
	require.NoError(t, err)
	requireLiveCounts(t, api, "a", "d", 0)
	// Resolution preceded deletion; processing did not. Its stamp stops the sweep
	// from adopting this orphan as the new device, independent of either clock.
	require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{old}))
	var row DeviceState
	require.NoError(t, api.RDB.DB(ctx).First(&row).Error)
	require.Equal(t, eventIncarnation(17), row.Incarnation)
	newEvent := withIncarnation(deletionUpdate("a", "d", at.Add(-time.Hour)), 23)
	newEvent.Identity = DeviceIdentity{Owners: Owners{Anchors: []string{"customer:new"}, At: at.Add(-time.Hour)}}
	newEvent.Measurements[0].Value = sql.NullFloat64{Float64: 23, Valid: true}
	newEvent.Dropped, newEvent.Locations = nil, nil
	require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{newEvent, old}))
	row = DeviceState{}
	require.NoError(t, api.RDB.DB(ctx).First(&row).Error)
	require.Equal(t, eventIncarnation(23), row.Incarnation)
	require.Equal(t, ownerSet([]string{"customer:new"}), row.ScopeAnchors)
	for _, table := range []any{&LatestLocation{}, &DroppedMeasurement{}} {
		var n int64
		require.NoError(t, api.RDB.DB(ctx).Model(table).Count(&n).Error)
		require.Zero(t, n)
	}
	readCtx := ctx
	if scopedRead {
		// Array overlap is PostgreSQL syntax; exercise the actual scoped read
		// against that database in the integration rig.
		readCtx = auth.WithClaims(ctx, &auth.Claims{ScopeAnchors: []string{"customer:new"}})
	}
	var readings []LatestMeasurement
	require.NoError(t, api.RDB.DB(readCtx).Find(&readings).Error)
	require.Len(t, readings, 1)
	require.Equal(t, float64(23), readings[0].Value.Float64)
	if scopedRead {
		readings = nil
		oldOwner := auth.WithClaims(ctx, &auth.Claims{ScopeAnchors: []string{"customer:old-owner"}})
		require.NoError(t, api.RDB.DB(oldOwner).Find(&readings).Error)
		require.Empty(t, readings)
	}
}
