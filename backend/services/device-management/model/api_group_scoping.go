// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"

	"github.com/devicechain-io/dc-device-management/internal/selector"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// lockMembershipFamily takes a transaction-scoped exclusive advisory lock keyed on
// (tenant, memberType), serializing ALL membership maintenance for one family within the
// tenant (both an incremental recompute and a Register). This is what makes same-txn
// recompute actually diverge-free: same-txn alone is not serializable, so two concurrent
// writes to DIFFERENT facets of the same entity — or a write racing a Register — could
// write-skew the read-model into a permanent stale/missed membership (there is no reconcile
// sweep). Serializing per family forces the second writer to re-evaluate against the first's
// committed state. It is Postgres-only (pg_advisory_xact_lock auto-releases at commit);
// under sqlite (tests) the single writer already serializes, so it is a no-op. Taken only
// when there is maintenance to do (a rule-referenced group exists for the family), so an
// unused tenant never pays for it.
func (api *Api) lockMembershipFamily(ctx context.Context, tx *gorm.DB, memberType string) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return core.ErrNoTenant
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(tenant + "\x00" + memberType))
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(h.Sum64())).Error
}

// membersMatchingFrozenOnTx resolves the full member set of a frozen version's selector on
// the given transaction (so a Register's backfill reads under its family lock). It lowers
// the FROZEN selector text — exactly what the pinned version matches — to the indexed
// predicate and reads every matching id/token in one query. It is an internal admin-time
// backfill (rule arming), so it is not externally paginated; the family lock bounds
// concurrency.
func (api *Api) membersMatchingFrozenOnTx(ctx context.Context, tx *gorm.DB, frozen *EntityGroupVersion) ([]EntityMember, error) {
	frag, args, table, err := api.lowerSelectorSource(ctx, frozen.MemberType, frozen.Selector)
	if err != nil {
		return nil, err
	}
	memberModel, _, _ := memberModelAndTable(frozen.MemberType)
	members := make([]EntityMember, 0)
	err = tx.Model(memberModel).Where(frag, args...).
		Select(table+".id", table+".token").Order(table + ".id").Find(&members).Error
	if err != nil {
		return nil, err
	}
	return members, nil
}

// RegisterGroupVersionForScoping enrolls a frozen group@version for membership
// maintenance (ADR-062 S4 calls this when a rule first references it): it extracts the
// frozen selector's facet keys into the reverse index and backfills the read-model with an
// authoritative set query, so the group@v is fully materialized before the rule arms. It is
// idempotent — re-registering an already-enrolled group@v rebuilds it — and rejects a
// static or unpublished-version reference (a rule can only scope to a published dynamic
// group@version). The whole enrollment — the family lock, the reverse-index rebuild, the
// backfill read, and the membership insert — runs in ONE transaction so a partial backfill
// can never arm a rule against a half-populated read-model, and so a concurrent recompute
// cannot interleave (it blocks on the same family lock and then self-heals its one entity).
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

	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize against every concurrent recompute (and other Register) of this family
		// so the backfill snapshot below cannot be raced into a permanent hole.
		if err := api.lockMembershipFamily(ctx, tx, frozen.MemberType); err != nil {
			return err
		}
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
		// Backfill under the lock: read the full member set on the tx, then batch-insert.
		members, err := api.membersMatchingFrozenOnTx(ctx, tx, frozen)
		if err != nil {
			return err
		}
		rows := make([]EntityGroupMembership, 0, len(members))
		for _, m := range members {
			rows = append(rows, EntityGroupMembership{
				EntityType:      frozen.MemberType,
				EntityId:        m.Id,
				GroupId:         group.ID,
				SelectorVersion: version,
				GroupToken:      group.Token,
			})
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(rows, 200).Error; err != nil {
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
	// Serialize this family's membership maintenance (only now that there is real work): a
	// concurrent write to a DIFFERENT facet of the same entity, or a Register, must not
	// write-skew the read-model. The second writer blocks here and re-evaluates against the
	// first's committed state.
	if err := api.lockMembershipFamily(ctx, db, entityType); err != nil {
		return err
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
// duplicate as success via ON CONFLICT DO NOTHING (the entity was already a member —
// recompute is idempotent, and the insert is race-safe on its own even independent of the
// family lock). The group token is denormalized onto the row for the resolver's stamp.
func (api *Api) upsertMembership(db *gorm.DB, entityType string, entityId, groupId uint, version int32, groupToken string) error {
	row := &EntityGroupMembership{
		EntityType:      entityType,
		EntityId:        entityId,
		GroupId:         groupId,
		SelectorVersion: version,
		GroupToken:      groupToken,
	}
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error
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
//
// S3 CACHE NOTE: S3 wraps this read with a negative-caching CachedApi decorator (an empty
// result is cached — the common case). When it does, it MUST evict the entity's cache key
// on EVERY path that mutates a membership row, or a missed eviction becomes a permanently
// missed/false detection: recomputeMembershipForAttr (per entity), purgeEntityMemberships
// (entity delete), purgeGroupScopingRows + RegisterGroupVersionForScoping +
// DeregisterGroupVersionForScoping (which touch many entities → a family/tenant-wide evict).
// No cache exists in S2, so these paths intentionally carry no eviction yet.
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
