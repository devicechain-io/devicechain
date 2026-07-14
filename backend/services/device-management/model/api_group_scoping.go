// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-device-management/internal/selector"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// This file implements the ADR-062 S2 membership subsystem inside device-management:
// the maintenance of the entity_group_membership read-model + the entity_group_facet_ref
// reverse index. It has three entry points:
//
//   - RegisterGroupVersionForScoping / DeregisterGroupVersionForScoping — enroll or GC a
//     frozen group@version (called by S4 when a rule first references / last un-references
//     it): extract the frozen selector's facet keys into the reverse index and backfill the
//     read-model with a set query.
//   - recomputeMembershipForAttr — the incremental hook the attribute set/delete path
//     calls: re-evaluate the one changed entity against exactly the group@versions whose
//     selector references the changed facet key (reverse-index bounded), upserting/deleting
//     its membership rows.
//   - MembershipsForEntity — the hot read the resolver (S3) stamps onto an event.
//
// Everything runs against the FROZEN version selector (EntityGroupVersion.Selector), never
// the group's live draft — a rule's target set must not drift under a draft edit. CLIENT
// (device-reported) attributes are structurally excluded: the lowering pins facets to
// SHARED, so recompute only ever fires on SHARED writes.

// membershipScopeEligible reports whether an attribute write can affect group membership:
// its family must be anchor-able (device/area/customer/asset — the families a group
// collects and the resolver anchors) and its scope must be SHARED (the only scope the
// selector lowering reads; a CLIENT attribute can never change membership, the ADR-062
// security invariant). It is the recompute-hook analogue of dynamicThresholdEligible.
func membershipScopeEligible(entityType, scope string) bool {
	if !ValidGroupMemberType(entity.Type(entityType)) {
		return false
	}
	return AttributeScope(scope) == AttributeScopeShared
}

// isMemberBySelector tests whether one entity satisfies an explicit (frozen) selector
// source — the single-entity membership test off the FROZEN version's selector, distinct
// from IsGroupMember which uses a group's live draft. It mirrors IsGroupMember's dynamic
// branch but takes the selector text explicitly (so a caller evaluates the version a rule
// pinned, not the current draft) and runs the count on the caller's db handle (a tx during
// same-txn recompute). lowerSelectorSource executes no query — it reads the dialect for the
// numeric cast and returns the SQL string — so passing api.RDB.DB(ctx) there while running
// the actual count on db is correct.
func (api *Api) isMemberBySelector(ctx context.Context, db *gorm.DB, memberType, source string, entityId uint) (bool, error) {
	frag, args, table, err := api.lowerSelectorSource(ctx, memberType, source)
	if err != nil {
		return false, err
	}
	memberModel, _, _ := memberModelAndTable(memberType)
	var count int64
	err = db.Model(memberModel).
		Where(table+".id = ?", entityId).
		Where(frag, args...).
		Limit(1).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// RegisterGroupVersionForScoping enrolls a frozen group@version for membership
// maintenance (ADR-062 S4 calls this when a rule first references it): it extracts the
// frozen selector's facet keys into the reverse index and backfills the read-model with an
// authoritative set query, so the group@v is fully materialized before the rule arms. It is
// idempotent — re-registering an already-enrolled group@v rebuilds it — and rejects a
// static or unpublished-version reference (a rule can only scope to a published dynamic
// group@version). The whole enrollment runs in one transaction so a partial backfill can
// never arm a rule against a half-populated read-model.
func (api *Api) RegisterGroupVersionForScoping(ctx context.Context, groupToken string, version int32) error {
	group, err := api.entityGroupByToken(ctx, groupToken)
	if err != nil {
		return err
	}
	if MembershipMode(group.MembershipMode) != MembershipDynamic {
		return fmt.Errorf("only a dynamic entity group can be scoped (group %q is %q)", groupToken, group.MembershipMode)
	}
	frozen, err := api.EntityGroupVersionByNumber(ctx, group.ID, version)
	if err != nil {
		return err
	}

	// Compile the frozen selector to extract its referenced facet keys. It already
	// cleared the publish gate, so this cannot reject; compile is how Keys() is reached.
	sel, err := selector.Compile(frozen.Selector, frozen.MemberType, 0)
	if err != nil {
		return err
	}
	keys := sel.Keys()

	// Resolve the full member set outside the write transaction (a bounded, paginated
	// set query); the rows are then written transactionally with the reverse index.
	members, err := api.resolveAllMembersByFrozenSelector(ctx, frozen)
	if err != nil {
		return err
	}

	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// Rebuild is register-idempotent: clear any prior enrollment for this exact
		// group@v, then re-insert. Scoped to (group_id, selector_version) so a different
		// version's rows are untouched. Hard-delete (Unscoped): these are derived rows whose
		// unique indexes exclude deleted_at, so a soft-delete tombstone would collide with
		// the re-insert below.
		if err := tx.Unscoped().Where("group_id = ? AND selector_version = ?", group.ID, version).
			Delete(&EntityGroupFacetRef{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("group_id = ? AND selector_version = ?", group.ID, version).
			Delete(&EntityGroupMembership{}).Error; err != nil {
			return err
		}
		for _, k := range keys {
			ref := &EntityGroupFacetRef{
				FacetKey:        k,
				MemberType:      frozen.MemberType,
				GroupId:         group.ID,
				SelectorVersion: version,
				GroupToken:      group.Token,
			}
			if err := tx.Create(ref).Error; err != nil {
				return err
			}
		}
		for _, m := range members {
			row := &EntityGroupMembership{
				EntityType:      frozen.MemberType,
				EntityId:        m.Id,
				GroupId:         group.ID,
				SelectorVersion: version,
				GroupToken:      group.Token,
			}
			if err := tx.Create(row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// DeregisterGroupVersionForScoping GCs a group@version's read-model + reverse-index rows
// (ADR-062 S4 calls this when the last rule un-references it). It is safe to call for a
// never-registered group@v (deletes nothing). Reference-counting — deciding when the LAST
// rule stops referencing group@v — is S4's concern; this just tears the rows down.
func (api *Api) DeregisterGroupVersionForScoping(ctx context.Context, groupToken string, version int32) error {
	group, err := api.entityGroupByToken(ctx, groupToken)
	if err != nil {
		return err
	}
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where("group_id = ? AND selector_version = ?", group.ID, version).
			Delete(&EntityGroupFacetRef{}).Error; err != nil {
			return err
		}
		return tx.Unscoped().Where("group_id = ? AND selector_version = ?", group.ID, version).
			Delete(&EntityGroupMembership{}).Error
	})
}

// resolveAllMembersByFrozenSelector pages the full member set of a frozen version's
// selector (a bounded set query, the backfill authority). It reuses PreviewSelector — the
// same compile-gate + lowering + bounded page a saved group's resolve takes — over the
// FROZEN selector text, so what is backfilled is exactly what the pinned version matches.
func (api *Api) resolveAllMembersByFrozenSelector(ctx context.Context, frozen *EntityGroupVersion) ([]EntityMember, error) {
	const pageSize int32 = 500
	all := make([]EntityMember, 0)
	// PageNumber is 1-based: rdb.ListOf clamps < 1 to 1, so starting at 0 would fetch
	// page 1 twice and skip the last page (duplicate inserts on a >pageSize backfill).
	for page := int32(1); ; page++ {
		res, err := api.PreviewSelector(ctx, frozen.MemberType, frozen.Selector,
			rdb.Pagination{PageNumber: page, PageSize: pageSize})
		if err != nil {
			return nil, err
		}
		all = append(all, res.Results...)
		// Stop when this page reached the last record, or returned nothing (a defensive
		// backstop). PageEnd is the effective (post-clamp) span end, so a clamped PageSize
		// is handled correctly.
		if len(res.Results) == 0 || res.Pagination.PageEnd >= res.Pagination.TotalRecords {
			break
		}
	}
	return all, nil
}

// recomputeMembershipForAttr re-evaluates one entity's membership after a SHARED-scope
// facet write, bounded by the reverse index (ADR-062 §3): it finds the group@versions
// whose frozen selector references the changed facet key FOR THIS FAMILY, re-tests the one
// entity against each frozen selector, and upserts (member) or deletes (non-member) its
// membership row. O(groups-referencing-K), usually zero — with no rule-referenced group
// referencing the key, the reverse-index lookup is empty and this is a no-op.
//
// It runs on the caller's db handle (the attribute-write tx — same-txn recompute, the
// exec-spec decision): a recompute failure rolls the attribute write back rather than
// leaving the attribute changed but membership stale (which, absent a reconcile sweep,
// would be a silently-dropped membership update — the at-most-once hole ADR-062 rejects).
func (api *Api) recomputeMembershipForAttr(ctx context.Context, db *gorm.DB, entityType string, entityId uint, attrKey string) error {
	refs := make([]EntityGroupFacetRef, 0)
	if err := db.
		Where("facet_key = ? AND member_type = ?", attrKey, entityType).
		Find(&refs).Error; err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}
	for _, ref := range refs {
		var frozen EntityGroupVersion
		err := db.Where("entity_group_id = ? AND version = ?", ref.GroupId, ref.SelectorVersion).First(&frozen).Error
		if err != nil {
			// The referenced version vanished (a group deleted mid-flight): skip it; the
			// deregister/teardown path removes the stale ref, and a resolve can't stamp a
			// version whose group is gone.
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return err
		}
		isMember, err := api.isMemberBySelector(ctx, db, frozen.MemberType, frozen.Selector, entityId)
		if err != nil {
			return err
		}
		if isMember {
			if err := api.upsertMembership(db, entityType, entityId, ref.GroupId, ref.SelectorVersion, ref.GroupToken); err != nil {
				return err
			}
		} else {
			// Hard-delete: a soft-delete tombstone would block a later re-join's insert
			// (the unique index excludes deleted_at).
			if err := db.Unscoped().
				Where("entity_type = ? AND entity_id = ? AND group_id = ? AND selector_version = ?",
					entityType, entityId, ref.GroupId, ref.SelectorVersion).
				Delete(&EntityGroupMembership{}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// upsertMembership inserts a positive membership row on the given db handle, treating a
// duplicate as success (the entity was already a member — recompute is idempotent). The
// group token is denormalized onto the row for the resolver's stamp.
func (api *Api) upsertMembership(db *gorm.DB, entityType string, entityId, groupId uint, version int32, groupToken string) error {
	var count int64
	if err := db.Model(&EntityGroupMembership{}).
		Where("entity_type = ? AND entity_id = ? AND group_id = ? AND selector_version = ?",
			entityType, entityId, groupId, version).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	row := &EntityGroupMembership{
		EntityType:      entityType,
		EntityId:        entityId,
		GroupId:         groupId,
		SelectorVersion: version,
		GroupToken:      groupToken,
	}
	return db.Create(row).Error
}

// writeAttrThenRecompute runs an attribute write and, when the write can affect
// dynamic-group membership (an anchor-able family + SHARED scope), the membership recompute
// in ONE transaction (ADR-062 S2 same-txn recompute) so a recompute failure rolls the write
// back — the attribute row and the read-model can never diverge. An ineligible write
// (CLIENT scope, a non-member family) runs bare: no transaction overhead, and the
// reverse-index lookup would be empty anyway.
func (api *Api) writeAttrThenRecompute(ctx context.Context, entityType string, entityId uint,
	attrKey, scope string, write func(db *gorm.DB) error) error {
	if !membershipScopeEligible(entityType, scope) {
		return write(api.RDB.DB(ctx))
	}
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := write(tx); err != nil {
			return err
		}
		return api.recomputeMembershipForAttr(ctx, tx, entityType, entityId, attrKey)
	})
}

// MembershipsForEntity returns the group@versions the entity currently belongs to (its
// positive rows). It is the hot read the resolver (S3) stamps onto an event; an entity in
// no rule-scoped group returns an empty slice (the pay-nothing common case).
func (api *Api) MembershipsForEntity(ctx context.Context, entityType string, entityId uint) ([]GroupMembership, error) {
	rows := make([]EntityGroupMembership, 0)
	if err := api.RDB.DB(ctx).
		Where("entity_type = ? AND entity_id = ?", entityType, entityId).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]GroupMembership, 0, len(rows))
	for _, r := range rows {
		out = append(out, GroupMembership{GroupId: r.GroupId, GroupToken: r.GroupToken, SelectorVersion: r.SelectorVersion})
	}
	return out, nil
}

// purgeEntityMemberships removes an entity's membership rows — called from the
// deleteEdgeEntity cascade so a deleted member leaves no dangling membership (its rows
// address it by (entity_type, entity_id) with no DB foreign key, mirroring the attribute
// cascade). Reverse-index rows are keyed by group, not entity, so they are untouched here.
func (api *Api) purgeEntityMemberships(tx *gorm.DB, entityType string, entityId uint) error {
	return tx.Unscoped().Where("entity_type = ? AND entity_id = ?", entityType, entityId).
		Delete(&EntityGroupMembership{}).Error
}

// purgeGroupScopingRows removes ALL of a group's scoping rows — both its membership
// rows (where it is the scoping group) and its reverse-index rows — across every version.
// It is called from DeleteEntityGroup's cascade so a deleted group leaves no orphan
// read-model or reverse-index rows. Hard-delete for the same tombstone-vs-unique-index
// reason as the rest of the subsystem.
func (api *Api) purgeGroupScopingRows(tx *gorm.DB, groupId uint) error {
	if err := tx.Unscoped().Where("group_id = ?", groupId).Delete(&EntityGroupMembership{}).Error; err != nil {
		return err
	}
	return tx.Unscoped().Where("group_id = ?", groupId).Delete(&EntityGroupFacetRef{}).Error
}
