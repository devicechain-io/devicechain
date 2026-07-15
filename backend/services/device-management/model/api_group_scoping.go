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
// under sqlite (tests) the single writer already serializes, so it is a no-op.
//
// CORRECTNESS DEPENDS ON READ COMMITTED (Postgres's default): the serialization argument
// relies on each statement taking a FRESH snapshot after the lock is acquired, so a
// transaction that blocks behind the lock then sees the holder's committed writes. Under
// REPEATABLE READ / SERIALIZABLE the blocked transaction's snapshot predates the lock wait and
// the skew reopens silently — do NOT raise default_transaction_isolation for these connections
// without revisiting recomputeMembershipForAttr and registerGroupVersionOnTx.
//
// Taken by each Register/Deregister and by every eligible (SHARED-scope, anchor-able)
// attribute-write recompute — which now acquires it BEFORE reading the reverse index (see
// recomputeMembershipForAttr), so an eligible write pays an uncontended lock even absent scoped
// groups. A tenant doing no scoped registration and no eligible attribute write never pays.
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

// RegisterGroupVersionForScoping enrolls a frozen group@version for membership maintenance:
// it extracts the frozen selector's facet keys into the reverse index and backfills the
// read-model with an authoritative set query, so the group@v is fully materialized. It is
// idempotent — re-registering an already-enrolled group@v rebuilds it — and rejects a static
// or unpublished-version reference (only a published dynamic group@version is scopeable). The
// whole enrollment — the family lock, the reverse-index rebuild, the backfill read, and the
// membership insert — runs in ONE transaction so a partial backfill can never leave a
// half-populated read-model, and a concurrent recompute cannot interleave (it blocks on the
// same family lock). This is the standalone (whole-transaction) form; the S4 publish/rollback
// path composes the read-model reconcile into its own txn via registerGroupVersionOnTx and
// owns the refcount that decides register-vs-deregister (reconcileEnrollmentOnTx), so this
// wrapper is an admin/test entry point and does NOT itself consult a reference count.
func (api *Api) RegisterGroupVersionForScoping(ctx context.Context, groupToken string, version int32) error {
	group, frozen, keys, err := api.loadGroupVersionForScoping(ctx, api.RDB.DB(ctx), groupToken, version)
	if err != nil {
		return err
	}
	var evictIds []uint
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		evictIds, err = api.registerGroupVersionOnTx(ctx, tx, group, frozen, keys)
		return err
	})
	if err != nil {
		return err
	}
	// Evict the backfilled members' membership caches post-commit so the resolver stamps
	// the newly-materialized memberships (the ADR-062 arming visibility invariant), and the
	// tenant's scoped-groups-exist gate so the resolver stops short-circuiting.
	api.evictMemberships(ctx, frozen.MemberType, evictIds)
	api.evictScopedGroupsExist(ctx)
	return nil
}

// loadGroupVersionForScoping resolves a (groupToken, version) to the group, its frozen
// version, and the frozen selector's referenced facet keys — the immutable inputs a
// registration needs. It rejects a static-group reference (only a dynamic group is
// scopeable) and fails closed if the group or version is absent. It runs on the caller's db
// handle: a reconcile INSIDE the publish/rollback transaction passes its tx (so the reads
// share that connection + the family advisory lock it already holds, rather than borrowing a
// second pool connection mid-transaction); the standalone path passes the plain handle. The
// tenant callback applies identically to either. The group's mode and the frozen version are
// effectively immutable, so which snapshot the reads see does not affect correctness.
func (api *Api) loadGroupVersionForScoping(ctx context.Context, db *gorm.DB, groupToken string, version int32) (*EntityGroup, *EntityGroupVersion, []string, error) {
	var group EntityGroup
	if err := db.Where("token = ?", groupToken).First(&group).Error; err != nil {
		return nil, nil, nil, err
	}
	if MembershipMode(group.MembershipMode) != MembershipDynamic {
		return nil, nil, nil, fmt.Errorf("only a dynamic entity group can be scoped (group %q is %q)", groupToken, group.MembershipMode)
	}
	var frozen EntityGroupVersion
	if err := db.Where("entity_group_id = ? AND version = ?", group.ID, version).First(&frozen).Error; err != nil {
		return nil, nil, nil, err
	}
	// Compile the frozen selector to extract its referenced facet keys. It already
	// cleared the publish gate, so this cannot reject; compile is how Keys() is reached.
	sel, err := selector.Compile(frozen.Selector, frozen.MemberType, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	return &group, &frozen, sel.Keys(), nil
}

// registerGroupVersionOnTx is the transaction body of a registration: it (re)builds the
// reverse-index + read-model rows for one frozen group@v under the family lock, returning
// the entity ids whose membership cache must be evicted post-commit (both prior and new
// members). It runs on a caller-provided tx so the S4 publish/rollback path can enroll a
// group@v in the SAME transaction that flips a profile's active version (ADR-062 arming: the
// read-model and the version that makes a scoped rule live commit atomically, so a rule can
// never go live against a half-built read-model). RegisterGroupVersionForScoping wraps it in
// its own transaction for the standalone admin/test path; the caller owns eviction.
func (api *Api) registerGroupVersionOnTx(ctx context.Context, tx *gorm.DB, group *EntityGroup, frozen *EntityGroupVersion, keys []string) ([]uint, error) {
	// Serialize against every concurrent recompute (and other Register) of this family
	// so the backfill snapshot below cannot be raced into a permanent hole.
	if err := api.lockMembershipFamily(ctx, tx, frozen.MemberType); err != nil {
		return nil, err
	}
	// Rebuild is register-idempotent: clear any prior enrollment for this exact
	// group@v, then re-insert. Scoped to (group_id, selector_version) so a different
	// version's rows are untouched. Hard-delete (Unscoped): these are derived rows whose
	// unique indexes exclude deleted_at, so a soft-delete tombstone would collide with
	// the re-insert below.
	if err := tx.Unscoped().Where("group_id = ? AND selector_version = ?", group.ID, frozen.Version).
		Delete(&EntityGroupFacetRef{}).Error; err != nil {
		return nil, err
	}
	// Collect the PRIOR members before wiping them, so a re-register that drops a
	// no-longer-matching entity evicts its (now stale-positive) cache too — not only the
	// new member set (the wipe exists precisely to correct drift).
	var evictIds []uint
	var prior []EntityGroupMembership
	if err := tx.Where("group_id = ? AND selector_version = ?", group.ID, frozen.Version).Find(&prior).Error; err != nil {
		return nil, err
	}
	for _, m := range prior {
		evictIds = append(evictIds, m.EntityId)
	}
	if err := tx.Unscoped().Where("group_id = ? AND selector_version = ?", group.ID, frozen.Version).
		Delete(&EntityGroupMembership{}).Error; err != nil {
		return nil, err
	}
	for _, k := range keys {
		ref := &EntityGroupFacetRef{
			FacetKey:        k,
			MemberType:      frozen.MemberType,
			GroupId:         group.ID,
			SelectorVersion: frozen.Version,
			GroupToken:      group.Token,
		}
		if err := tx.Create(ref).Error; err != nil {
			return nil, err
		}
	}
	// Backfill under the lock: read the full member set on the tx, then batch-insert.
	members, err := api.membersMatchingFrozenOnTx(ctx, tx, frozen)
	if err != nil {
		return nil, err
	}
	rows := make([]EntityGroupMembership, 0, len(members))
	for _, m := range members {
		rows = append(rows, EntityGroupMembership{
			EntityType:      frozen.MemberType,
			EntityId:        m.Id,
			GroupId:         group.ID,
			SelectorVersion: frozen.Version,
			GroupToken:      group.Token,
		})
		evictIds = append(evictIds, m.Id)
	}
	if len(rows) > 0 {
		if err := tx.CreateInBatches(rows, 200).Error; err != nil {
			return nil, err
		}
	}
	return evictIds, nil
}

// DeregisterGroupVersionForScoping GCs a group@version's read-model + reverse-index rows. It
// is safe to call for a never-registered group@v (deletes nothing). This is the standalone
// (whole-transaction) admin/test form and does NOT itself consult a reference count — the S4
// publish/rollback path decides register-vs-deregister from the DetectionRuleScopeRef refcount
// (reconcileEnrollmentOnTx) and composes deregisterGroupVersionOnTx into its own txn. Do NOT
// wire this into a live path without a refcount guard: it tears the rows down unconditionally.
func (api *Api) DeregisterGroupVersionForScoping(ctx context.Context, groupToken string, version int32) error {
	group, err := api.entityGroupByToken(ctx, groupToken)
	if err != nil {
		return err
	}
	var evictType string
	var evictIds []uint
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// Take the family lock, mirroring registerGroupVersionOnTx (ADR-062 S5): the reconcile
		// path locks via lockAffectedFamiliesOnTx before composing deregisterGroupVersionOnTx,
		// but this standalone wrapper is the caller that would otherwise tear rows down lock-free
		// and race a concurrent recompute into an orphan membership row. Advisory xact locks are
		// re-entrant, so this is harmless if a caller already holds it.
		if err := api.lockMembershipFamily(ctx, tx, group.MemberType); err != nil {
			return err
		}
		evictType, evictIds, err = api.deregisterGroupVersionOnTx(ctx, tx, group.ID, version)
		return err
	})
	if err != nil {
		return err
	}
	api.evictMemberships(ctx, evictType, evictIds)
	api.evictScopedGroupsExist(ctx)
	return nil
}

// deregisterGroupVersionOnTx is the transaction body of a deregistration: it tears down one
// group@v's read-model + reverse-index rows on the caller's tx, returning the entity family
// and ids to evict post-commit. It runs on a caller-provided tx so the S4 publish/rollback
// reconcile can GC a no-longer-referenced group@v in the same transaction that flips the
// profile's active version.
func (api *Api) deregisterGroupVersionOnTx(ctx context.Context, tx *gorm.DB, groupId uint, version int32) (string, []uint, error) {
	// Collect the affected members before tearing their rows down, so their caches can
	// be evicted post-commit (they lose this membership).
	var evictType string
	var evictIds []uint
	var members []EntityGroupMembership
	if err := tx.Where("group_id = ? AND selector_version = ?", groupId, version).Find(&members).Error; err != nil {
		return "", nil, err
	}
	for _, m := range members {
		evictType = m.EntityType
		evictIds = append(evictIds, m.EntityId)
	}
	if err := tx.Unscoped().Where("group_id = ? AND selector_version = ?", groupId, version).
		Delete(&EntityGroupFacetRef{}).Error; err != nil {
		return "", nil, err
	}
	if err := tx.Unscoped().Where("group_id = ? AND selector_version = ?", groupId, version).
		Delete(&EntityGroupMembership{}).Error; err != nil {
		return "", nil, err
	}
	return evictType, evictIds, nil
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
// The returned bool reports whether any rule-referenced group existed for the key
// (i.e. real work happened) — the caller evicts the entity's membership cache post-commit
// only then, preserving pay-nothing for tenants with no rule-scoped groups.
func (api *Api) recomputeMembershipForAttr(ctx context.Context, db *gorm.DB, entityType string, entityId uint, attrKey string) (bool, error) {
	// Serialize this family's membership maintenance BEFORE reading the reverse index (ADR-062
	// S5): a concurrent write to a DIFFERENT facet of the same entity, a Register, or a
	// Deregister must not write-skew the read-model. Reading refs FIRST (the prior shape) let
	// two hazards through under READ COMMITTED, because an MVCC select neither sees nor blocks
	// on another transaction's uncommitted rows:
	//   (a) missed enrollment — a concurrent Register whose facet-ref insert had not yet
	//       committed was read as an empty ref set, so this write skipped the lock and its
	//       recompute entirely; the entity was then silently never enrolled (and, absent a
	//       reconcile sweep, stayed missing — the at-most-once hole ADR-062 rejects).
	//   (b) orphan resurrection — a stale ref read before a concurrent Deregister committed
	//       re-upserted a membership row for a group@v whose reverse index was being torn down,
	//       leaving a membership row with no facet ref.
	// Taking the family lock first forces whichever transaction holds it to commit before the
	// other reads, so the ref set read here reflects all committed Register/Deregister work and
	// the backfill-vs-attribute race resolves deterministically in the lock holder's favor. The
	// cost is that an eligible (SHARED-scope, anchor-able) attribute write now takes the
	// tenant+family advisory lock even for a tenant with no scoped groups; attribute writes are
	// low-volume metadata ops and the lock is uncontended in that case, so it is acceptable.
	if err := api.lockMembershipFamily(ctx, db, entityType); err != nil {
		return false, err
	}
	refs := make([]EntityGroupFacetRef, 0)
	if err := db.
		Where("facet_key = ? AND member_type = ?", attrKey, entityType).
		Find(&refs).Error; err != nil {
		return false, err
	}
	if len(refs) == 0 {
		return false, nil
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
			return false, err
		}
		isMember, err := api.isMemberBySelector(ctx, db, frozen.MemberType, frozen.Selector, entityId)
		if err != nil {
			return false, err
		}
		if isMember {
			if err := api.upsertMembership(db, entityType, entityId, ref.GroupId, ref.SelectorVersion, ref.GroupToken); err != nil {
				return false, err
			}
		} else {
			// Hard-delete: a soft-delete tombstone would block a later re-join's insert
			// (the unique index excludes deleted_at).
			if err := db.Unscoped().
				Where("entity_type = ? AND entity_id = ? AND group_id = ? AND selector_version = ?",
					entityType, entityId, ref.GroupId, ref.SelectorVersion).
				Delete(&EntityGroupMembership{}).Error; err != nil {
				return false, err
			}
		}
	}
	return true, nil
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

// AnyScopedGroups reports whether the tenant has ANY registered rule-scoped group@v (any
// reverse-index row). The resolver checks it once per event (cached) to short-circuit the
// per-entity membership reads entirely for a tenant not using rule scoping — ADR-062
// Decision 7 pay-nothing: zero scoped groups ⇒ no stamp, no per-target lookups.
func (api *Api) AnyScopedGroups(ctx context.Context) (bool, error) {
	rows := make([]EntityGroupFacetRef, 0, 1)
	if err := api.RDB.DB(ctx).Select("id").Limit(1).Find(&rows).Error; err != nil {
		return false, err
	}
	return len(rows) > 0, nil
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
	var touched bool
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := write(tx); err != nil {
			return err
		}
		t, err := api.recomputeMembershipForAttr(ctx, tx, entityType, entityId, attrKey)
		touched = t
		return err
	})
	if err != nil {
		return err
	}
	// Evict the entity's membership cache post-commit — only if a rule-referenced group
	// existed for the key (otherwise no membership could have changed, and an unused tenant
	// pays nothing).
	if touched {
		api.evictMemberships(ctx, entityType, []uint{entityId})
	}
	return nil
}

// MembershipsForEntity returns the group@versions the entity currently belongs to (its
// positive rows). It is the hot read the resolver (S3) stamps onto an event; an entity in
// no rule-scoped group returns an empty slice (the pay-nothing common case).
//
// CACHE (S3): the CachedApi decorator wraps this read with negative caching (an empty
// result is cached — the common case). Every path that mutates a membership row evicts the
// affected entities' cache keys post-commit so a stale stamp is never served: the attribute
// set/delete recompute (writeAttrThenRecompute / DeleteEntityAttribute — per entity), entity
// delete (deleteEdgeEntity), and RegisterGroupVersionForScoping / DeregisterGroupVersionForScoping
// / DeleteEntityGroup (which evict every affected member). The TTL is a self-healing backstop.
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
