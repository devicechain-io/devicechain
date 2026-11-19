// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/stretchr/testify/require"
)

func TestRolloutPendingDeletionCannotTargetReplacement(t *testing.T) {
	testRolloutReplacement(t, true)
}

func TestRolloutDelayedDeletionCannotTargetReplacement(t *testing.T) {
	testRolloutReplacement(t, false)
}

func testRolloutReplacement(t *testing.T, deliver bool) {
	t.Helper()
	e := newRolloutEnv(t, 2)
	ctx := tenantCtx("acme")
	e.devices.incarnations = map[string]uint64{"acme/dev-1": 17}
	e.create(t, "r1", 1, nil)
	_, err := e.api.StartRollout(ctx, "r1")
	require.NoError(t, err)
	require.Equal(t, TargetPending, e.targets(t, "r1", nil)[1].Target.Status)
	delete(e.devices.profiles, "acme/dev-1")
	if deliver {
		require.NoError(t, e.api.DeviceDeleted(ctx, "dev-1", 17, e.api.now()))
	}
	e.devices.profiles["acme/dev-1"] = "gateway"
	e.devices.incarnations["acme/dev-1"] = 23
	e.groups.set("acme", "fleet", "dev-0") // replacement is outside the group
	e.finishCurrent(t, ota.StateUpdated, "dev-0")
	e.step(t, "r1")
	require.Zero(t, e.assignmentCount(t, "dev-1"), "replacement must never receive an assignment")
	target := e.targets(t, "r1", nil)[1].Target
	require.Equal(t, TargetSkipped, target.Status)
	require.Contains(t, []string{CodeDeviceNotFound, "DEVICE_REPLACED"}, target.SkipCode)
}

// Direct rows represent targets written by a pod predating the nullable identity column.
func legacyRollout(t *testing.T, e *rolloutEnv, started sql.NullTime) uint {
	t.Helper()
	e.create(t, "r1", 1, nil)
	db := e.api.RDB.DB(tenantCtx("acme"))
	id := e.rollout(t, "r1").Rollout.ID
	require.NoError(t, db.Model(&UpdateRollout{}).Where("id = ?", id).Updates(map[string]any{
		"state": RolloutRunning, "started_at": started, "target_count": 1,
	}).Error)
	require.NoError(t, db.Create(&UpdateRolloutTarget{RolloutId: id, DeviceToken: "dev-0", Status: TargetPending}).Error)
	return id
}

func TestRolloutLegacyCreationFence(t *testing.T) {
	for _, tc := range []struct {
		name             string
		offset           time.Duration
		unknown, noStart bool
		status           string
	}{
		{"well after", 6 * time.Minute, false, false, TargetSkipped},
		{"well before", -6 * time.Minute, false, false, TargetDispatched},
		{"near after", time.Second, false, false, TargetPending},
		{"near before", -time.Second, false, false, TargetPending},
		{"upper boundary", 5 * time.Minute, false, false, TargetPending},
		{"lower boundary", -5 * time.Minute, false, false, TargetPending},
		{"equal", 0, false, false, TargetPending},
		{"unknown creation", 0, true, false, TargetPending},
		{"unknown start", -time.Second, false, true, TargetPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRolloutEnv(t, 1)
			start := e.api.now()
			created := start.Add(tc.offset)
			if tc.unknown {
				created = time.Time{}
			}
			e.devices.createdAt = map[string]time.Time{"acme/dev-0": created}
			legacyRollout(t, e, sql.NullTime{Time: start, Valid: !tc.noStart})
			e.step(t, "r1")
			target := e.targets(t, "r1", nil)[0].Target
			require.Equal(t, tc.status, target.Status)
			if tc.status == TargetSkipped {
				require.Equal(t, "DEVICE_REPLACED", target.SkipCode)
			}
			if tc.status != TargetDispatched {
				require.Zero(t, e.assignmentCount(t, "dev-0"))
			}
			if tc.status == TargetPending {
				require.Equal(t, RolloutRunning, e.rollout(t, "r1").Rollout.State)
			}
		})
	}
}

func TestRolloutOldDeletionPreservesNewerFrozenTarget(t *testing.T) {
	e := newRolloutEnv(t, 2)
	e.devices.incarnations = map[string]uint64{"acme/dev-1": 23}
	e.create(t, "r1", 1, nil)
	_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, e.api.DeviceDeleted(tenantCtx("acme"), "dev-1", 17, e.api.now().Add(time.Hour)))
	}
	require.Equal(t, TargetPending, e.targets(t, "r1", nil)[1].Target.Status)
	e.finishCurrent(t, ota.StateUpdated, "dev-0")
	require.Equal(t, 1, e.step(t, "r1").Dispatched)
}

type incarnationLookupFunc func(context.Context, string, []string) (map[string]uint64, error)

func (f incarnationLookupFunc) DeviceIncarnations(ctx context.Context, tenant string, tokens []string) (map[string]uint64, error) {
	return f(ctx, tenant, tokens)
}

func TestRolloutFreezeRefusesUnknownIdentity(t *testing.T) {
	for _, name := range []string{"unwired", "failed", "partial error", "nil answer", "zero", "overflow"} {
		t.Run(name, func(t *testing.T) {
			e := newRolloutEnv(t, 2)
			e.create(t, "r1", 1, nil)
			e.api.Incarnations = incarnationLookupFunc(func(context.Context, string, []string) (map[string]uint64, error) {
				switch name {
				case "failed":
					return nil, errors.New("lookup failed")
				case "partial error":
					return map[string]uint64{"dev-0": 17}, errors.New("second chunk failed")
				case "zero":
					return map[string]uint64{"dev-0": 0}, nil
				case "overflow":
					return map[string]uint64{"dev-0": math.MaxUint64}, nil
				default:
					return nil, nil
				}
			})
			if name == "unwired" {
				e.api.Incarnations = nil
			}
			_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
			requireRefusal(t, err, CodeRolloutGroupUnavailable)
			require.Equal(t, RolloutDraft, e.rollout(t, "r1").Rollout.State)
			require.Empty(t, e.targets(t, "r1", nil))
			require.Zero(t, e.assignmentCount(t, "dev-0"))
		})
	}
}

func TestRolloutFreezeAbsentDeviceStaysSkippedAfterRecreation(t *testing.T) {
	e := newRolloutEnv(t, 1)
	e.create(t, "r1", 1, nil)
	e.api.Incarnations = incarnationLookupFunc(func(_ context.Context, tenant string, tokens []string) (map[string]uint64, error) {
		require.Equal(t, "acme", tenant)
		require.Equal(t, []string{"dev-0"}, tokens)
		return map[string]uint64{}, nil // absent during freeze, present again at inline step
	})
	_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)
	target := e.targets(t, "r1", nil)[0].Target
	require.Equal(t, TargetSkipped, target.Status)
	require.Equal(t, CodeDeviceNotFound, target.SkipCode)
	require.False(t, target.DeviceIncarnation.Valid)
	require.Zero(t, e.assignmentCount(t, "dev-0"))
}

func TestRolloutFrozenIdentityCompareAndHold(t *testing.T) {
	for _, live := range []uint64{0, 16, 17, 23} {
		t.Run(fmt.Sprint(live), func(t *testing.T) {
			e := newRolloutEnv(t, 2)
			e.devices.incarnations = map[string]uint64{"acme/dev-1": 17}
			e.create(t, "r1", 1, nil)
			_, err := e.api.StartRollout(tenantCtx("acme"), "r1")
			require.NoError(t, err)
			before := e.targets(t, "r1", nil)[1].Target
			require.Equal(t, sql.NullInt64{Int64: 17, Valid: true}, before.DeviceIncarnation)
			e.devices.incarnations["acme/dev-1"] = live
			if live < 17 {
				e.devices.disabled["acme/dev-1"] = true
				e.devices.profiles["acme/dev-1"] = "incompatible"
			}
			e.finishCurrent(t, ota.StateUpdated, "dev-0")
			res := e.step(t, "r1")
			after := e.targets(t, "r1", nil)[1].Target
			switch {
			case live == 17:
				require.Equal(t, 1, res.Dispatched)
				require.Equal(t, TargetDispatched, after.Status)
			case live > 17:
				require.Equal(t, 1, res.Skipped)
				require.Equal(t, TargetSkipped, after.Status)
				require.Equal(t, CodeDeviceReplaced, after.SkipCode)
				require.Contains(t, after.SkipDetail, "17")
				require.Contains(t, after.SkipDetail, "23")
				require.Zero(t, e.assignmentCount(t, "dev-1"))
			default:
				require.Equal(t, RolloutStepResult{}, res)
				require.Equal(t, before, after, "HOLD must write nothing, even if other facts refuse the device")
				require.Equal(t, RolloutRunning, e.rollout(t, "r1").Rollout.State)
				require.Zero(t, e.assignmentCount(t, "dev-1"))
				e.devices.incarnations["acme/dev-1"] = 17
				e.devices.disabled["acme/dev-1"] = false
				e.devices.profiles["acme/dev-1"] = "gateway"
				require.Equal(t, 1, e.step(t, "r1").Dispatched, "the held target retries")
			}
		})
	}
}

func TestRolloutInvalidFrozenIdentityHolds(t *testing.T) {
	for _, id := range []int64{0, -1} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			e := newRolloutEnv(t, 1)
			legacyRollout(t, e, sql.NullTime{Time: e.api.now(), Valid: true})
			require.NoError(t, e.api.RDB.DB(tenantCtx("acme")).Model(&UpdateRolloutTarget{}).
				Where("device_token = ?", "dev-0").Update("device_incarnation", id).Error)
			require.Equal(t, RolloutStepResult{}, e.step(t, "r1"))
			require.Equal(t, TargetPending, e.targets(t, "r1", nil)[0].Target.Status)
		})
	}
}

func TestRolloutHeldWindowDoesNotStarveLaterTargets(t *testing.T) {
	const n = 30
	e := newRolloutEnv(t, n)
	e.create(t, "r1", 1, nil)
	id, start := e.rollout(t, "r1").Rollout.ID, e.api.now()
	db := e.api.RDB.DB(tenantCtx("acme"))
	require.NoError(t, db.Model(&UpdateRollout{}).Where("id = ?", id).
		Updates(map[string]any{"state": RolloutRunning, "started_at": start, "target_count": n}).Error)
	for i := range n {
		require.NoError(t, db.Create(&UpdateRolloutTarget{RolloutId: id, DeviceToken: fmt.Sprintf("dev-%d", i), Status: TargetPending}).Error)
	}
	e.devices.createdAt = map[string]time.Time{"acme/dev-29": start.Add(-6 * time.Minute)}
	for range 3 {
		e.step(t, "r1")
	}
	require.Equal(t, int64(1), e.assignmentCount(t, "dev-29"), "a window full of held targets must not block later targets")
	require.Equal(t, n-1, e.rollout(t, "r1").Progress.Pending)
	require.Equal(t, RolloutRunning, e.rollout(t, "r1").Rollout.State)
	e.devices.createdAt["acme/dev-0"] = start.Add(-6 * time.Minute)
	e.finishCurrent(t, ota.StateUpdated, "dev-29")
	require.Equal(t, 1, e.step(t, "r1").Dispatched, "the window wraps and retries earlier held targets")
	require.Equal(t, int64(1), e.assignmentCount(t, "dev-0"))
}

func TestRolloutDeletionPendingCoverage(t *testing.T) {
	for _, tc := range []struct {
		name         string
		frozen       sql.NullInt64
		deleted      uint64
		offset       time.Duration
		unknownStart bool
		want         string
	}{
		{"same", sql.NullInt64{Int64: 17, Valid: true}, 17, time.Hour, false, TargetSkipped},
		{"older frozen", sql.NullInt64{Int64: 16, Valid: true}, 17, time.Hour, false, TargetSkipped},
		{"newer frozen", sql.NullInt64{Int64: 23, Valid: true}, 17, -time.Hour, false, TargetPending},
		{"zero deletion stamped", sql.NullInt64{Int64: 17, Valid: true}, 0, -time.Hour, false, TargetPending},
		{"legacy before", sql.NullInt64{}, 17, -time.Second, false, TargetSkipped},
		{"legacy equal", sql.NullInt64{}, 17, 0, false, TargetSkipped},
		{"legacy after", sql.NullInt64{}, 17, time.Second, false, TargetPending},
		{"legacy no start", sql.NullInt64{}, 17, 0, true, TargetPending},
		{"legacy zero deletion before", sql.NullInt64{}, 0, -time.Second, false, TargetSkipped},
		{"legacy zero deletion after", sql.NullInt64{}, 0, time.Second, false, TargetPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRolloutEnv(t, 1)
			deleted := e.api.now()
			legacyRollout(t, e, sql.NullTime{Time: deleted.Add(tc.offset), Valid: !tc.unknownStart})
			db := e.api.RDB.DB(tenantCtx("acme"))
			require.NoError(t, db.Model(&UpdateRolloutTarget{}).Where("device_token = ?", "dev-0").
				Updates(map[string]any{"device_incarnation": tc.frozen, "created_at": deleted.Add(time.Hour)}).Error)
			for range 2 {
				require.NoError(t, e.api.DeviceDeleted(tenantCtx("acme"), "dev-0", tc.deleted, deleted))
			}
			target := e.targets(t, "r1", nil)[0].Target
			require.Equal(t, tc.want, target.Status)
			if tc.want == TargetSkipped {
				require.Equal(t, CodeDeviceNotFound, target.SkipCode)
			}
		})
	}
}

func TestRolloutDeletionPendingCASAndTenantScope(t *testing.T) {
	e := newRolloutEnv(t, 1)
	id := legacyRollout(t, e, sql.NullTime{Time: e.api.now(), Valid: true})
	ctx := tenantCtx("acme")
	for _, status := range []string{TargetCancelled, TargetSkipped, TargetDispatched} {
		require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateRolloutTarget{}).Where("rollout_id = ?", id).Update("status", status).Error)
		require.NoError(t, e.api.DeviceDeleted(ctx, "dev-0", 17, e.api.now()))
		require.Equal(t, status, e.targets(t, "r1", nil)[0].Target.Status)
	}
	require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateRolloutTarget{}).Where("rollout_id = ?", id).Update("status", TargetPending).Error)
	require.NoError(t, e.api.DeviceDeleted(tenantCtx("globex"), "dev-0", 17, e.api.now()))
	require.Equal(t, TargetPending, e.targets(t, "r1", nil)[0].Target.Status)
}
