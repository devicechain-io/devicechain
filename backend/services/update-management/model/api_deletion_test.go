// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/stretchr/testify/require"
)

func TestAReCreatedDeviceIsNotOfferedTheDeletedDevicesUpdate(t *testing.T) {
	e := newUpdateEnv(t)
	ctx := tenantCtx("acme")
	e.devices.profiles["acme/unit-12"] = "pump-v1"
	e.devices.incarnations = map[string]uint64{"acme/unit-12": 17}
	a := e.availableArtifact(t, "acme", "fw-A", "1.0.0", []byte("pump firmware"))
	require.NoError(t, e.api.RDB.DB(ctx).Model(a).Update("compatible_profiles", []byte(`["pump-v1"]`)).Error)
	v, err := e.api.AssignArtifact(ctx, "unit-12", "fw-A")
	require.NoError(t, err)
	require.Equal(t, sql.NullInt64{Int64: 17, Valid: true}, v.Assignment.DeviceIncarnation)
	grant, err := e.api.GrantDownload(ctx, "unit-12", v.Assignment.Token)
	require.NoError(t, err)
	require.NotEmpty(t, grant.URL)
	e.devices.profiles["acme/unit-12"] = "pump-v2"
	e.devices.incarnations["acme/unit-12"] = 23
	e.advance(DisabledCacheTTL)
	views, err := e.api.CurrentUpdates(ctx, "unit-12")
	require.NoError(t, err)
	require.Empty(t, views)
	_, err = e.api.GrantDownload(ctx, "unit-12", v.Assignment.Token)
	var refusal *GrantRefusal
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, GrantUnknownAssignment, refusal.Reason)
	verdict, err := e.api.ApplyReport(ctx, "unit-12", progress(v, 1, ota.StageReceived, "boot"))
	require.NoError(t, err)
	require.Equal(t, VerdictUnknownAssignment, verdict)
	_, err = e.api.ResolveDownload(ctx, &Capability{DeviceToken: "unit-12", AssignmentID: v.Assignment.Token,
		Digest: v.Attempt.TargetDigest, ArtifactTok: a.Token})
	require.ErrorIs(t, err, ErrDownloadStale)
	// Inventory must not reconcile the previous incarnation's UNKNOWN installer either.
	require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAttempt{}).Where("id = ?", v.Attempt.ID).Update("state", string(ota.StateUnknown)).Error)
	inv := running(v, 2, "other-boot")
	inv.Kind = ota.KindInventory
	verdict, err = e.api.ApplyReport(ctx, "unit-12", inv)
	require.NoError(t, err)
	require.Equal(t, VerdictUnknownAssignment, verdict)
	require.Equal(t, ota.StateUnknown, e.state(t, "acme", v))
}

func TestTheSameIncarnationCanDownloadAfterDisableAndEnable(t *testing.T) {
	e := newUpdateEnv(t)
	ctx := tenantCtx("acme")
	e.availableArtifact(t, "acme", "fw-A", "1.0.0", []byte("firmware"))
	v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-A")
	require.NoError(t, err)
	e.devices.disabled = map[string]bool{"acme/dev-1": true}
	views, err := e.api.CurrentUpdates(ctx, "dev-1")
	require.ErrorIs(t, err, ErrDeviceDisabled)
	require.Empty(t, views)
	e.devices.disabled["acme/dev-1"] = false
	e.advance(DisabledCacheTTL)
	views, err = e.api.CurrentUpdates(ctx, "dev-1")
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.Equal(t, v.Assignment.Token, views[0].Assignment.Token)
	_, err = e.api.GrantDownload(ctx, "dev-1", v.Assignment.Token)
	require.NoError(t, err)
}

func TestLegacyAssignmentIsFencedByDeviceCreationTime(t *testing.T) {
	for _, delta := range []time.Duration{-time.Second, 0, time.Second} {
		t.Run(delta.String(), func(t *testing.T) {
			e := newUpdateEnv(t)
			ctx := tenantCtx("acme")
			e.availableArtifact(t, "acme", "fw-A", "1.0.0", []byte("firmware"))
			v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-A")
			require.NoError(t, err)
			require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAssignment{}).Where("id = ?", v.Assignment.ID).Update("device_incarnation", nil).Error)
			e.devices.createdAt = map[string]time.Time{"acme/dev-1": v.Assignment.CreatedAt.Add(delta)}
			views, err := e.api.CurrentUpdates(ctx, "dev-1")
			require.NoError(t, err)
			_, grantErr := e.api.GrantDownload(ctx, "dev-1", v.Assignment.Token)
			if delta > 0 {
				require.Empty(t, views)
				require.Error(t, grantErr)
			} else {
				require.Len(t, views, 1)
				require.NoError(t, grantErr)
			}
		})
	}
}

func TestMissingDeviceHasNoAssignments(t *testing.T) {
	e := newUpdateEnv(t)
	ctx := tenantCtx("acme")
	e.availableArtifact(t, "acme", "fw-A", "1.0.0", []byte("firmware"))
	v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-A")
	require.NoError(t, err)
	delete(e.devices.profiles, "acme/dev-1")
	views, err := e.api.CurrentUpdates(ctx, "dev-1")
	require.NoError(t, err)
	require.Empty(t, views)
	_, err = e.api.GrantDownload(ctx, "dev-1", v.Assignment.Token)
	var refusal *GrantRefusal
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, GrantUnknownAssignment, refusal.Reason)
}

func TestDeviceDeletedVoidsOnlyCoveredAssignmentsAndForgetsCachedFacts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		id     sql.NullInt64
		offset time.Duration
		ended  bool
	}{
		{"deleted", sql.NullInt64{Int64: 17, Valid: true}, 0, true},
		{"older", sql.NullInt64{Int64: 16, Valid: true}, 0, true},
		{"newer", sql.NullInt64{Int64: 23, Valid: true}, 0, false},
		{"legacy before", sql.NullInt64{}, -time.Second, true},
		{"legacy equal", sql.NullInt64{}, 0, true},
		{"legacy after", sql.NullInt64{}, time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newUpdateEnv(t)
			ctx := tenantCtx("acme")
			e.devices.incarnations = map[string]uint64{"acme/dev-1": 17}
			e.availableArtifact(t, "acme", "fw-A", "1.0.0", []byte("firmware"))
			v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-A")
			require.NoError(t, err)
			_, err = e.api.CurrentUpdates(ctx, "dev-1")
			require.NoError(t, err)
			deleted := v.Assignment.CreatedAt
			require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAssignment{}).Where("id = ?", v.Assignment.ID).
				Updates(map[string]any{"device_incarnation": tc.id, "created_at": deleted.Add(tc.offset)}).Error)
			e.devices.incarnations["acme/dev-1"] = 23
			for range 2 {
				require.NoError(t, e.api.DeviceDeleted(ctx, "dev-1", 17, deleted))
			}
			facts, found, err := e.api.deviceGate(ctx, "dev-1")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, uint64(23), facts.Incarnation)
			state := e.state(t, "acme", v)
			if tc.ended {
				require.Equal(t, ota.StateCancelled, state)
			} else {
				require.Equal(t, ota.StateQueued, state)
			}
		})
	}
}

func TestDeletionSupersedesAttemptsThatCannotFinishCancellationAndCancelsRolloutTargets(t *testing.T) {
	for _, state := range []ota.State{ota.StateQueued, ota.StateDownloading, ota.StateUpdating, ota.StateUnknown} {
		t.Run(string(state), func(t *testing.T) {
			e := newRolloutEnv(t, 1)
			ctx := tenantCtx("acme")
			e.create(t, "r1", 1, nil)
			_, err := e.api.StartRollout(ctx, "r1")
			require.NoError(t, err)
			var asg UpdateAssignment
			require.NoError(t, e.api.RDB.DB(ctx).Where("device_token = ?", "dev-0").First(&asg).Error)
			require.Equal(t, sql.NullInt64{Int64: 1, Valid: true}, asg.DeviceIncarnation, "rollout materialisation stamps identity")
			require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAttempt{}).Where("assignment_id = ?", asg.ID).Update("state", string(state)).Error)
			for range 2 {
				require.NoError(t, e.api.DeviceDeleted(ctx, "dev-0", 1, asg.CreatedAt))
			}
			v, err := e.api.AssignmentByToken(ctx, asg.Token)
			require.NoError(t, err)
			if state == ota.StateQueued {
				require.Equal(t, string(ota.StateCancelled), v.Attempt.State)
			} else {
				require.True(t, v.Assignment.SupersededAt.Valid)
				require.Equal(t, string(state), v.Attempt.State)
				require.Equal(t, state == ota.StateDownloading, v.Attempt.CancelRequested)
			}
			targets := e.targets(t, "r1", ptr(OutcomeCancelled))
			require.Len(t, targets, 1)
			require.Equal(t, TargetCancelled, targets[0].Target.Status)
		})
	}
}
