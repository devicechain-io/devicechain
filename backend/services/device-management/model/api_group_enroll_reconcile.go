// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/devicechain-io/dc-device-management/internal/selector"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ReconcileMembershipEnrollment repairs pre-upgrade empty-match enrollment, once at
// process startup. It only adds rows; failures must never refuse service startup.
// The session lock serializes replicas, and the family lock serializes each repair
// with registration, deregistration, creation and attribute recomputation.
func (api *Api) ReconcileMembershipEnrollment(ctx context.Context) error {
	run := func() error { return api.reconcileMembershipEnrollment(ctx) }
	if api.RDB.Database.Dialector.Name() == "postgres" {
		return api.RDB.WithAdvisoryLock(ctx, rdb.AdvisoryLockKey("device-management/membership-enrollment"), run)
	}
	return run()
}

func (api *Api) reconcileMembershipEnrollment(ctx context.Context) error {
	started := time.Now()
	var tenantCount, groupCount, sentinels, memberships, attemptedTenants int
	defer func() {
		log.Info().Int("tenants", tenantCount).Int("groups", groupCount).
			Int("sentinels_added", sentinels).Int("memberships_added", memberships).
			Dur("duration", time.Since(started)).Msg("Membership enrollment reconcile finished")
	}()
	// System context is only for tenant discovery. Every subsequent query is tenant scoped.
	tenantsSet := map[string]bool{}
	for _, model := range []interface{}{&EntityGroupFacetRef{}, &DetectionRuleScopeRef{}, &EntityGroup{}} {
		var tenants []string
		if err := api.RDB.DB(core.WithSystemContext(ctx)).Model(model).Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error; err != nil {
			return fmt.Errorf("discover membership enrollment tenants: %w", err)
		}
		for _, tenant := range tenants {
			tenantsSet[tenant] = true
		}
	}
	tenants := make([]string, 0, len(tenantsSet))
	for tenant := range tenantsSet {
		tenants = append(tenants, tenant)
	}
	slices.Sort(tenants)
	for _, tenant := range tenants {
		tenantCount++
		tenantCtx := core.WithTenant(ctx, tenant)
		refs, err := enrollmentReconcileRefs(api.RDB.DB(tenantCtx))
		if err != nil {
			log.Error().Err(err).Str("tenant", tenant).Msg("Could not enumerate membership enrollment groups")
			continue
		}
		attemptedTenants++
		for _, ref := range refs {
			groupCount++
			addedSentinel, addedMembers, err := api.reconcileEnrollmentGroup(tenantCtx, ref)
			sentinels += addedSentinel
			memberships += addedMembers
			if err != nil {
				log.Error().Err(err).Str("tenant", tenant).Uint("group_id", ref.GroupId).
					Str("group", ref.GroupToken).Int32("version", ref.SelectorVersion).Msg("Could not reconcile membership enrollment")
				continue
			}
		}
	}
	if len(tenants) > 0 && attemptedTenants == 0 {
		return fmt.Errorf("membership enrollment could not enumerate groups for any of %d tenants", len(tenants))
	}
	return nil
}

// Include published pins even when registration left no facet rows at all.
func enrollmentReconcileRefs(db *gorm.DB) ([]EntityGroupFacetRef, error) {
	var refs []EntityGroupFacetRef
	if err := db.Model(&EntityGroupFacetRef{}).Distinct("group_id", "selector_version", "member_type", "group_token").Find(&refs).Error; err != nil {
		return nil, err
	}
	var pins []DetectionRuleScopeRef
	if err := db.Find(&pins).Error; err != nil {
		return nil, err
	}
	var groups []EntityGroup
	if err := db.Find(&groups).Error; err != nil {
		return nil, err
	}
	pinsByToken := make(map[string][]int32)
	for _, pin := range pins {
		pinsByToken[pin.GroupToken] = append(pinsByToken[pin.GroupToken], pin.GroupVersion)
	}
	for _, g := range groups {
		if g.IsAccessScope() && g.ActiveVersion.Valid {
			refs = append(refs, EntityGroupFacetRef{GroupId: g.ID, SelectorVersion: g.ActiveVersion.Int32, MemberType: g.MemberType, GroupToken: g.Token})
		}
		for _, version := range pinsByToken[g.Token] {
			refs = append(refs, EntityGroupFacetRef{GroupId: g.ID, SelectorVersion: version, MemberType: g.MemberType, GroupToken: g.Token})
		}
	}
	slices.SortFunc(refs, func(a, b EntityGroupFacetRef) int {
		if a.GroupId < b.GroupId {
			return -1
		}
		if a.GroupId > b.GroupId {
			return 1
		}
		if a.SelectorVersion < b.SelectorVersion {
			return -1
		}
		if a.SelectorVersion > b.SelectorVersion {
			return 1
		}
		return 0
	})
	return slices.CompactFunc(refs, func(a, b EntityGroupFacetRef) bool {
		return a.GroupId == b.GroupId && a.SelectorVersion == b.SelectorVersion
	}), nil
}

// enrollmentRepairPageSize bounds memory and time holding the family lock, including
// the ownership queue's per-page deduplication work.
var enrollmentRepairPageSize = 500

// enrollmentRegistrationOnTx must be called under the membership family lock.
// A missing registration is normal: deregistration can run between repair pages.
func enrollmentRegistrationOnTx(tx *gorm.DB, ref EntityGroupFacetRef) (registered, accessPinned bool, err error) {
	var group EntityGroup
	err = tx.Where("id = ?", ref.GroupId).First(&group).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	var refs, pins int64
	if err = tx.Model(&EntityGroupFacetRef{}).Where("group_id = ? AND selector_version = ?", ref.GroupId, ref.SelectorVersion).Count(&refs).Error; err != nil {
		return false, false, err
	}
	if err = tx.Model(&DetectionRuleScopeRef{}).Where("group_token = ? AND group_version = ?", group.Token, ref.SelectorVersion).Count(&pins).Error; err != nil {
		return false, false, err
	}
	accessPinned = group.IsAccessScope() && group.ActiveVersion.Valid && group.ActiveVersion.Int32 == ref.SelectorVersion
	return refs > 0 || pins > 0 || accessPinned, accessPinned, nil
}

func (api *Api) reconcileEnrollmentGroup(ctx context.Context, ref EntityGroupFacetRef) (int, int, error) {
	var sentinel int
	var frozen EntityGroupVersion
	var repair bool
	// Restore the creation marker in its own short transaction, so creates can
	// enroll immediately while the historical membership backfill progresses.
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := api.lockMembershipFamily(ctx, tx, ref.MemberType); err != nil {
			return err
		}
		registered, _, err := enrollmentRegistrationOnTx(tx, ref)
		if err != nil || !registered {
			return err
		}
		err = tx.Where("entity_group_id = ? AND version = ?", ref.GroupId, ref.SelectorVersion).First(&frozen).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		sel, err := selector.Compile(frozen.Selector, frozen.MemberType)
		if err != nil {
			return err
		}
		repair, err = sel.MatchesEmpty()
		if err != nil || !repair {
			return err
		}
		marker := EntityGroupFacetRef{GroupId: ref.GroupId, SelectorVersion: ref.SelectorVersion, GroupToken: ref.GroupToken, MemberType: frozen.MemberType, FacetKey: createEnrollFacetKey}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&marker)
		sentinel = int(res.RowsAffected)
		return res.Error
	})
	if err != nil {
		return 0, 0, err
	}
	if sentinel > 0 {
		api.evictScopedGroupsExist(ctx)
	}
	if !repair {
		return sentinel, 0, nil
	}
	frag, args, table, err := api.lowerSelectorSource(ctx, frozen.MemberType, frozen.Selector)
	if err != nil {
		return sentinel, 0, err
	}
	memberModel, _, _ := memberModelAndTable(frozen.MemberType)
	var lastId uint
	var added int
	for {
		var ids []uint
		var inserted int
		err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			if err := api.lockMembershipFamily(ctx, tx, frozen.MemberType); err != nil {
				return err
			}
			registered, accessPinned, err := enrollmentRegistrationOnTx(tx, ref)
			if err != nil || !registered {
				return err
			}
			existing := tx.Model(&EntityGroupMembership{}).Select("1").
				Where("group_id = ? AND selector_version = ? AND entity_type = ?", ref.GroupId, ref.SelectorVersion, frozen.MemberType).
				Where("entity_id = " + table + ".id")
			if err := tx.Model(memberModel).Where(table+".id > ?", lastId).
				Where(frag, args...).Where("NOT EXISTS (?)", existing).
				Order(table+".id").Limit(enrollmentRepairPageSize).Pluck(table+".id", &ids).Error; err != nil {
				return err
			}
			if len(ids) == 0 {
				return nil
			}
			rows := make([]EntityGroupMembership, 0, len(ids))
			for _, id := range ids {
				rows = append(rows, EntityGroupMembership{EntityType: frozen.MemberType, EntityId: id, GroupId: ref.GroupId, SelectorVersion: ref.SelectorVersion, GroupToken: ref.GroupToken})
			}
			res := rdb.CreateChunked(tx.Clauses(clause.OnConflict{DoNothing: true}), &rows)
			if res.Error != nil {
				return res.Error
			}
			inserted = int(res.RowsAffected)
			if accessPinned && frozen.MemberType == "device" {
				// LAST: no more locks after taking ownership outbox row locks.
				_, err = queueOwnershipOnTx(tx, ids)
				return err
			}
			return nil
		})
		if err != nil {
			return sentinel, added, err
		}
		if len(ids) == 0 {
			return sentinel, added, nil
		}
		added += inserted
		api.evictMemberships(ctx, frozen.MemberType, ids)
		lastId = ids[len(ids)-1]
	}
}
