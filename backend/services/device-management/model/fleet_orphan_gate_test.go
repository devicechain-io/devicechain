// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The start and resume gate asks whether any orphaned attribute row remains. "Could not tell" is
// neither answer: a check that fails (a timeout, the database) refuses with a retryable code of its
// own, not with BULK_CHANGES_NOT_ACTIVATED, which tells the operator to wait for a sweep that may
// have nothing to do; and it never lets the start through.
func TestFleetOrphanGateTellsAFailedCheckFromOrphans(t *testing.T) {
	armed(t)
	api, ctx := fleetFixture(t, 2)
	op := readyOp(t, api, ctx, "s1", FleetSafetyRequest{}, "d1")

	// The check fails: refused as unverified, and nothing changes.
	failure := errors.New("canceling statement due to statement timeout")
	const hook = "test:orphan-probe-fails"
	require.NoError(t, api.RDB.Database.Callback().Query().Before("gorm:query").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "entity_attributes" {
			_ = tx.AddError(failure)
		}
	}))
	_, err := api.StartFleetOperation(ctx, "s1", op.ContractDigest, alice)
	requireFleetCode(t, err, FleetCodeActivationUnverified)
	got, err := api.fleetOperationByToken(ctx, "s1")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchReady, got.Dispatch, "an unverified check never lets a start through")
	require.NoError(t, api.RDB.Database.Callback().Query().Remove(hook))

	// An orphan remains: not activated.
	require.NoError(t, api.RDB.DB(ctx).Create(&EntityAttribute{EntityType: "device", EntityId: 9_999_999, Scope: "SHARED", AttrKey: "k"}).Error)
	_, err = api.StartFleetOperation(ctx, "s1", op.ContractDigest, alice)
	requireFleetCode(t, err, FleetCodeNotActivated)

	// None remains: the start lands (the negative control).
	require.NoError(t, api.RDB.DB(ctx).Unscoped().Where("entity_id = ?", 9_999_999).Delete(&EntityAttribute{}).Error)
	_, err = api.StartFleetOperation(ctx, "s1", op.ContractDigest, alice)
	require.NoError(t, err)
}

// A generation head with no device is an orphan too, asked of its own table.
func TestFleetOrphanProbeFindsAHeadOnlyOrphan(t *testing.T) {
	api, ctx := fleetFixture(t, 1)
	found, err := api.HasOrphanedDeviceAttributes(ctx)
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, api.RDB.DB(ctx).Create(&AttributeGeneration{DeviceId: 9_999_998, Scope: "SHARED", AttrKey: "k", Generation: 1}).Error)
	found, err = api.HasOrphanedDeviceAttributes(ctx)
	require.NoError(t, err)
	require.True(t, found)
}
