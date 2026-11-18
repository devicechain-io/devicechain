// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
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

// enrollmentPageEvictor observes committed pages and can interleave a real
// deregistration before the repair starts its next transaction.
type enrollmentPageEvictor struct {
	captureEvictor
	afterPage func([]uint)
}

func (e *enrollmentPageEvictor) EvictMemberships(ctx context.Context, memberType string, ids []uint) {
	e.captureEvictor.EvictMemberships(ctx, memberType, ids)
	if e.afterPage != nil {
		e.afterPage(ids)
	}
}

func TestReconcileMembershipEnrollmentPages(t *testing.T) {
	previous := enrollmentRepairPageSize
	enrollmentRepairPageSize = 2
	t.Cleanup(func() { enrollmentRepairPageSize = previous })
	for _, pin := range []string{"refs", "rule", "access"} {
		t.Run(pin, func(t *testing.T) {
			api, ctx := newScopingTestApi(t)
			f := seedEnrollmentRepair(t, api, ctx, pin)
			// Leave an existing matching row between missing candidates. It must
			// neither consume a page slot nor be rewritten.
			existing := seedDevice(t, api, ctx, "existing")
			_, err := api.upsertMembership(api.RDB.DB(ctx), "device", existing, f.group.ID, f.version, f.group.Token)
			require.NoError(t, err)
			var raw Device
			require.NoError(t, api.RDB.DB(ctx).First(&raw, existing).Error)
			for i := 0; i < 3; i++ {
				raw.ID = 0
				raw.Token = fmt.Sprintf("legacy-page-%d", i)
				require.NoError(t, api.RDB.DB(ctx).Create(&raw).Error)
				f.missing = append(f.missing, raw.ID)
			}
			var preserved []EntityGroupMembership
			require.NoError(t, api.RDB.DB(ctx).Where("entity_id IN ?", []uint{existing, f.stale}).Order("id").Find(&preserved).Error)
			require.Len(t, preserved, 2)
			require.NoError(t, api.RDB.DB(ctx).Where("1 = 1").Delete(&PendingOwnershipFact{}).Error)
			evictor := &enrollmentPageEvictor{}
			api.CacheEvictor = evictor
			evictor.afterPage = func(ids []uint) {
				// Reads here are outside the page transaction and see its commit.
				for _, id := range ids {
					require.True(t, isMemberOf(t, api, ctx, "device", id, f.group.ID, f.version))
				}
				var markers int64
				require.NoError(t, api.RDB.DB(ctx).Model(&EntityGroupFacetRef{}).
					Where("group_id = ? AND facet_key = ?", f.group.ID, createEnrollFacetKey).Count(&markers).Error)
				require.EqualValues(t, 1, markers)
			}
			require.NoError(t, api.ReconcileMembershipEnrollment(context.Background()))
			require.Len(t, evictor.membershipEvicts, 3)
			var repaired []uint
			for i, page := range evictor.membershipEvicts {
				require.Equal(t, "device", page.entityType)
				require.LessOrEqual(t, len(page.entityIds), enrollmentRepairPageSize)
				if i < 2 {
					require.Len(t, page.entityIds, 2)
				}
				repaired = append(repaired, page.entityIds...)
			}
			require.Equal(t, f.missing, repaired, "each missing ID is repaired exactly once in ascending pages")
			var after []EntityGroupMembership
			require.NoError(t, api.RDB.DB(ctx).Where("entity_id IN ?", []uint{existing, f.stale}).Order("id").Find(&after).Error)
			require.Equal(t, preserved, after, "existing rows and stale positives are unchanged")
			var count int64
			require.NoError(t, api.RDB.DB(ctx).Model(&EntityGroupMembership{}).Where("group_id = ? AND selector_version = ?", f.group.ID, f.version).Count(&count).Error)
			require.EqualValues(t, len(f.missing)+2, count)
			var facts []PendingOwnershipFact
			require.NoError(t, api.RDB.DB(ctx).Order("device_id").Find(&facts).Error)
			if pin == "access" {
				var queued []uint
				for _, fact := range facts {
					queued = append(queued, fact.DeviceId)
				}
				require.Equal(t, f.missing, queued)
			} else {
				require.Empty(t, facts)
			}
			evictor.membershipEvicts = nil
			require.NoError(t, api.ReconcileMembershipEnrollment(context.Background()))
			require.Empty(t, evictor.membershipEvicts)
			var afterFacts []PendingOwnershipFact
			require.NoError(t, api.RDB.DB(ctx).Order("device_id").Find(&afterFacts).Error)
			require.Equal(t, facts, afterFacts, "rerunning does not requeue any access facts")
		})
	}
}

func TestReconcileMembershipEnrollmentStopsAfterDeregister(t *testing.T) {
	previous := enrollmentRepairPageSize
	enrollmentRepairPageSize = 1
	t.Cleanup(func() { enrollmentRepairPageSize = previous })
	api, ctx := newScopingTestApi(t)
	f := seedEnrollmentRepair(t, api, ctx, "refs")
	evictor := &enrollmentPageEvictor{}
	api.CacheEvictor = evictor
	pages := 0
	evictor.afterPage = func(ids []uint) {
		pages++
		require.Equal(t, f.missing[:1], ids)
		require.True(t, isMemberOf(t, api, ctx, "device", ids[0], f.group.ID, f.version))
		require.False(t, isMemberOf(t, api, ctx, "device", f.missing[1], f.group.ID, f.version))
		evictor.afterPage = nil // deregistration itself also evicts membership caches
		require.NoError(t, api.DeregisterGroupVersionForScoping(ctx, f.group.Token, f.version))
	}
	require.NoError(t, api.ReconcileMembershipEnrollment(context.Background()))
	require.Equal(t, 1, pages)
	for _, model := range []interface{}{&EntityGroupFacetRef{}, &EntityGroupMembership{}} {
		var count int64
		require.NoError(t, api.RDB.DB(ctx).Model(model).Where("group_id = ? AND selector_version = ?", f.group.ID, f.version).Count(&count).Error)
		require.Zero(t, count, "later pages must not resurrect a deregistered version")
	}
}
