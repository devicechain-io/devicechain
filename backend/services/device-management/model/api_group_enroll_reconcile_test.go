// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-device-management/internal/selector"
	"github.com/stretchr/testify/require"
)

type enrollmentRepairFixture struct {
	group   *EntityGroup
	version int32
	missing []uint
	stale   uint
}

// Build the real registration/create state, then remove precisely what an old pod
// could have missed. The direct insert represents an entity predating enrollment.
func seedEnrollmentRepair(t *testing.T, api *Api, ctx context.Context, pin string) enrollmentRepairFixture {
	t.Helper()
	g := createDynamicGroup(t, api, ctx, "fresh", `!("retired" in attr)`)
	v, err := api.PublishEntityGroup(ctx, g.Token, nil, nil, "tester")
	require.NoError(t, err)
	require.NoError(t, api.RegisterGroupVersionForScoping(ctx, g.Token, v.Version))
	if pin == "access" {
		_, err = api.SetEntityGroupAccessScope(ctx, g.Token, true)
		require.NoError(t, err)
	}
	if pin == "rule" {
		require.NoError(t, api.RDB.DB(ctx).Create(&DetectionRuleScopeRef{DeviceProfileId: g.ID, RuleToken: "r", GroupToken: g.Token, GroupVersion: v.Version}).Error)
	}
	d := seedDevice(t, api, ctx, "after-register")
	require.True(t, isMemberOf(t, api, ctx, "device", d, g.ID, v.Version))
	require.NoError(t, api.RDB.DB(ctx).Unscoped().Where("group_id = ? AND selector_version = ?", g.ID, v.Version).Delete(&EntityGroupMembership{}).Error)
	q := api.RDB.DB(ctx).Unscoped().Where("group_id = ? AND selector_version = ?", g.ID, v.Version)
	if pin == "refs" {
		q = q.Where("facet_key = ?", createEnrollFacetKey)
	}
	require.NoError(t, q.Delete(&EntityGroupFacetRef{}).Error)
	var raw Device
	require.NoError(t, api.RDB.DB(ctx).First(&raw, d).Error)
	raw.ID = 0
	raw.Token = "legacy-direct"
	require.NoError(t, api.RDB.DB(ctx).Create(&raw).Error)
	stale := seedDevice(t, api, ctx, "stale")
	value := "yes"
	_, err = api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{EntityType: "device", Entity: "stale", Scope: string(AttributeScopeShared), AttrKey: "retired", ValueType: string(AttributeValueString), Value: &value})
	require.NoError(t, err)
	_, err = api.upsertMembership(api.RDB.DB(ctx), "device", stale, g.ID, v.Version, g.Token)
	require.NoError(t, err)
	for _, id := range []uint{d, raw.ID} {
		require.False(t, isMemberOf(t, api, ctx, "device", id, g.ID, v.Version), "negative control: membership is missing before reconcile")
	}
	require.NoError(t, api.RDB.DB(ctx).Where("1 = 1").Delete(&PendingOwnershipFact{}).Error)
	return enrollmentRepairFixture{g, v.Version, []uint{d, raw.ID}, stale}
}

func assertEnrollmentRepair(t *testing.T, api *Api, ctx context.Context, f enrollmentRepairFixture) {
	t.Helper()
	for _, id := range append(append([]uint{}, f.missing...), f.stale) {
		require.True(t, isMemberOf(t, api, ctx, "device", id, f.group.ID, f.version))
	}
	var markers int64
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityGroupFacetRef{}).Where("group_id = ? AND selector_version = ? AND facet_key = ?", f.group.ID, f.version, createEnrollFacetKey).Count(&markers).Error)
	require.EqualValues(t, 1, markers)
	var rows int64
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityGroupMembership{}).Where("group_id = ? AND selector_version = ?", f.group.ID, f.version).Count(&rows).Error)
	require.EqualValues(t, 3, rows, "exactly two repairs plus the preserved stale positive")
}

func TestReconcileMembershipEnrollment(t *testing.T) {
	// Compile rejects constants/non-facet predicates: a zero-key selector cannot
	// currently be authored. Removing ALL refs exercises both pin-only discovery paths.
	for _, source := range []string{`true`, `!false`, `memberType == "device"`} {
		_, err := selector.Compile(source, "device")
		require.Error(t, err)
	}
	for _, pin := range []string{"refs", "rule", "access"} {
		t.Run(pin, func(t *testing.T) {
			api, ctx := newScopingTestApi(t)
			f := seedEnrollmentRepair(t, api, ctx, pin)
			evictor := &captureEvictor{}
			api.CacheEvictor = evictor
			require.NoError(t, api.ReconcileMembershipEnrollment(context.Background()))
			assertEnrollmentRepair(t, api, ctx, f)
			require.Len(t, evictor.membershipEvicts, 1)
			require.Equal(t, "device", evictor.membershipEvicts[0].entityType)
			require.ElementsMatch(t, f.missing, evictor.membershipEvicts[0].entityIds)
			var queued []PendingOwnershipFact
			require.NoError(t, api.RDB.DB(ctx).Order("device_id").Find(&queued).Error)
			if pin == "access" {
				var ids []uint
				for _, row := range queued {
					ids = append(ids, row.DeviceId)
				}
				require.ElementsMatch(t, f.missing, ids)
			} else {
				require.Empty(t, queued)
			}
			var beforeRefs []EntityGroupFacetRef
			var beforeMembers []EntityGroupMembership
			require.NoError(t, api.RDB.DB(ctx).Order("id").Find(&beforeRefs).Error)
			require.NoError(t, api.RDB.DB(ctx).Order("id").Find(&beforeMembers).Error)
			evictor.membershipEvicts = nil
			require.NoError(t, api.ReconcileMembershipEnrollment(context.Background()))
			assertEnrollmentRepair(t, api, ctx, f)
			require.Empty(t, evictor.membershipEvicts)
			var afterRefs []EntityGroupFacetRef
			var afterMembers []EntityGroupMembership
			var afterQueue []PendingOwnershipFact
			require.NoError(t, api.RDB.DB(ctx).Order("id").Find(&afterRefs).Error)
			require.NoError(t, api.RDB.DB(ctx).Order("id").Find(&afterMembers).Error)
			require.NoError(t, api.RDB.DB(ctx).Order("device_id").Find(&afterQueue).Error)
			require.Equal(t, beforeRefs, afterRefs)
			require.Equal(t, beforeMembers, afterMembers)
			require.Equal(t, queued, afterQueue, "idempotent: facts are not requeued")
		})
	}
}

func TestReconcileMembershipEnrollmentContinuesAfterGroupFailure(t *testing.T) {
	api, ctx := newScopingTestApi(t)
	broken := createDynamicGroup(t, api, ctx, "broken", `!("retired" in attr)`)
	v, err := api.PublishEntityGroup(ctx, broken.Token, nil, nil, "tester")
	require.NoError(t, err)
	require.NoError(t, api.RegisterGroupVersionForScoping(ctx, broken.Token, v.Version))
	f := seedEnrollmentRepair(t, api, ctx, "refs")
	// Corrupt an earlier group's frozen selector to force a per-group failure.
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityGroupVersion{}).
		Where("entity_group_id = ? AND version = ?", broken.ID, v.Version).
		Update("selector", "invalid(").Error)
	require.NoError(t, api.ReconcileMembershipEnrollment(context.Background()), "a group failure is nonfatal")
	assertEnrollmentRepair(t, api, ctx, f)
}
