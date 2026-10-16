// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/devicechain-io/dc-device-management/internal/selector"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// This file implements DYNAMIC entity-group versioning (ADR-062 S1), mirroring
// device-profile versioning (ADR-045 slice c / api_profile_versions.go): a dynamic
// group's live Selector is the mutable DRAFT; PublishEntityGroup re-compiles +
// cost-gates that draft and freezes it into the next immutable EntityGroupVersion,
// then points EntityGroup.ActiveVersion at it; RollbackEntityGroup re-points the
// active pointer at an earlier version (non-destructive; the draft is untouched). A
// rule scopes to a specific {group}@{version} (ADR-062 S4), so its target set can
// never drift under a later selector edit. Static groups are never versioned.
//
// Unlike a profile publish, this emits NO cross-service fact: a group version is
// consumed only inside device-management (the resolver reads the frozen selector
// through the membership read-model, ADR-062 S2/S3), never by another service.

// entityGroupByToken loads the single group addressed by token, returning
// gorm.ErrRecordNotFound when absent so the versioning entry points fail closed
// (mirrors deviceProfileByToken).
func (api *Api) entityGroupByToken(ctx context.Context, token string) (*EntityGroup, error) {
	matches, err := api.EntityGroupsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return matches[0], nil
}

// PublishEntityGroup freezes a dynamic group's current draft selector into a new
// immutable version — the next monotonic integer for that group — and points the
// group's ActiveVersion at it. The draft selector is re-compiled + cost-gated before
// freezing, so a published version can never carry an un-vetted (non-lowerable or
// over-budget) selector; a rejection fails the publish closed with the selector
// package's typed author error. label/description are optional annotations;
// publishedBy is the caller's identity. Concurrent publishes are safe: the unique
// (entity_group_id, version) index rejects a duplicate version number. Publishing a
// static group is rejected — it has no selector to freeze.
func (api *Api) PublishEntityGroup(ctx context.Context, token string,
	label, description *string, publishedBy string) (*EntityGroupVersion, error) {
	group, err := api.entityGroupByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if MembershipMode(group.MembershipMode) != MembershipDynamic {
		return nil, fmt.Errorf("only a dynamic entity group can be published (group %q is %q)",
			token, group.MembershipMode)
	}
	if !group.Selector.Valid || group.Selector.String == "" {
		return nil, fmt.Errorf("dynamic entity group %q has no selector to publish", token)
	}

	// Re-compile + cost-gate the draft one last time before freezing: the selector was
	// already vetted at create/update, but re-checking here guarantees validated ≡
	// frozen even if the selector-env schema advanced since the draft was last written.
	sel, err := selector.Compile(group.Selector.String, group.MemberType)
	if err != nil {
		return nil, err
	}

	var maxVersion int32
	if err := api.RDB.DB(ctx).Model(&EntityGroupVersion{}).
		Where("entity_group_id = ?", group.ID).
		Select("COALESCE(MAX(version), 0)").Scan(&maxVersion).Error; err != nil {
		return nil, err
	}

	version := &EntityGroupVersion{
		EntityGroupId:  group.ID,
		Version:        maxVersion + 1,
		Selector:       sel.Source(),
		MemberType:     group.MemberType,
		SelectorSchema: selector.SchemaVersion,
		Label:          rdb.NullStrOf(label),
		Description:    rdb.NullStrOf(description),
		PublishedBy:    publishedBy,
	}
	// Insert the version and advance the active pointer atomically: a pointer update
	// that failed after the insert would leave an orphan version and rules resolving
	// the stale one, so wrap both.
	var evictions []membershipEviction
	var changed bool
	var members []uint
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// The family lock FIRST, before the group row is touched (see movePointerOnTx).
		from, err := api.lockFamilyAndReadPointerOnTx(ctx, tx, group)
		if err != nil {
			return err
		}
		if err := tx.Create(version).Error; err != nil {
			return err
		}
		res := tx.Model(group).Where("id = ?", group.ID).
			Update("active_version", version.Version)
		if res.Error != nil {
			return res.Error
		}
		// The group was deleted between the load and here (its cascade already removed
		// the version rows): roll the whole publish back rather than commit a version
		// row no rule can ever resolve.
		if res.RowsAffected == 0 {
			return fmt.Errorf("%w: entity group %q", gorm.ErrRecordNotFound, token)
		}
		// A rule pins a VERSION, so a publish enrolls nothing for rules. An access scope
		// follows the pointer (ADR-081): its new version is materialized, and the old torn
		// down unless a rule pins it, in this same transaction.
		evictions, changed, members, err = api.moveAccessScopeOnTx(ctx, tx, group.ID, from)
		return err
	})
	if err != nil {
		return nil, err
	}
	api.afterAccessScopeMoved(ctx, evictions, changed, members)
	return version, nil
}

// afterAccessScopeMoved fires, post-commit, what moving an access scope's pointer produced:
// the read-model evictions, the members' cached memberships (their access marker may have
// changed even where no row did), and an ownership-changed fact for each member device.
func (api *Api) afterAccessScopeMoved(ctx context.Context, evictions []membershipEviction, changed bool, members []uint) {
	api.fireScopingEvictions(ctx, evictions, changed)
	api.evictMemberships(ctx, string(entity.TypeDevice), members)
	api.emitOwnershipChanged(ctx, members)
}

// lockFamilyAndReadPointerOnTx takes the group's family lock and returns the published
// pointer as it is NOW, under that lock, for a pointer move on tx. Two things depend on the
// lock coming before the group row is touched:
//
//   - the order of locks. SetEntityGroupAccessScope and DeleteEntityGroup take the family
//     lock and then the group row; a move that took the row first and the family lock
//     second (inside moveAccessScopeOnTx) would be the opposite order, and the engine
//     answers that with a deadlock for one of the two callers.
//   - what the switch reads. SetEntityGroupAccessScope enrolls the pointer it reads under
//     the family lock; a move committed between its read and its write would leave the
//     scope enabled at a version nobody materialized, and its members seeing nothing.
//
// The pointer is re-read rather than taken from the group loaded before the transaction,
// for the same reason: a move that committed in between is the one this move is after.
func (api *Api) lockFamilyAndReadPointerOnTx(ctx context.Context, tx *gorm.DB, group *EntityGroup) (sql.NullInt32, error) {
	if err := api.lockMembershipFamily(ctx, tx, group.MemberType); err != nil {
		return sql.NullInt32{}, err
	}
	var current EntityGroup
	if err := tx.Where("id = ?", group.ID).First(&current).Error; err != nil {
		return sql.NullInt32{}, err
	}
	return current.ActiveVersion, nil
}

// RollbackEntityGroup re-points the group's active version at an existing version,
// so rules scoped to the group resolve that earlier selector again. It is a
// non-destructive pointer flip (mirrors RollbackDeviceProfile): history is
// append-only and the mutable draft is untouched, so a bad publish is reverted
// instantly and can be rolled forward again. Returns gorm.ErrRecordNotFound if the
// group or the target version does not exist.
func (api *Api) RollbackEntityGroup(ctx context.Context, token string, version int32) (*EntityGroup, error) {
	group, err := api.entityGroupByToken(ctx, token)
	if err != nil {
		return nil, err
	}

	var count int64
	if err := api.RDB.DB(ctx).Model(&EntityGroupVersion{}).
		Where("entity_group_id = ? AND version = ?", group.ID, version).
		Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, fmt.Errorf("%w: entity group %q has no version %d", gorm.ErrRecordNotFound, token, version)
	}

	// The pointer flip and an access scope's re-enrollment commit together (see
	// PublishEntityGroup), under the family lock, as there.
	var evictions []membershipEviction
	var changed bool
	var members []uint
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		from, err := api.lockFamilyAndReadPointerOnTx(ctx, tx, group)
		if err != nil {
			return err
		}
		res := tx.Model(group).Where("id = ?", group.ID).
			Update("active_version", version)
		if res.Error != nil {
			return res.Error
		}
		// The group was deleted between the existence check and here.
		if res.RowsAffected == 0 {
			return fmt.Errorf("%w: entity group %q", gorm.ErrRecordNotFound, token)
		}
		evictions, changed, members, err = api.moveAccessScopeOnTx(ctx, tx, group.ID, from)
		return err
	})
	if err != nil {
		return nil, err
	}
	api.afterAccessScopeMoved(ctx, evictions, changed, members)
	// Reload so the returned group carries the freshly-bumped updated_at.
	return api.entityGroupByToken(ctx, token)
}

// EntityGroupVersions lists a group's published versions, newest first, at most
// rdb.MaxPageSize per call (page narrows or pages further). Returns
// gorm.ErrRecordNotFound if the group does not exist.
func (api *Api) EntityGroupVersions(ctx context.Context, token string, page *VersionListArgs) ([]*EntityGroupVersion, error) {
	group, err := api.entityGroupByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	versions := make([]*EntityGroupVersion, 0)
	limit, offset, err := page.window()
	if err != nil {
		return nil, err
	}
	result := api.RDB.DB(ctx).Where("entity_group_id = ?", group.ID).
		Order("version DESC").Limit(limit).Offset(offset).Find(&versions)
	if result.Error != nil {
		return nil, result.Error
	}
	return versions, nil
}

// entityGroupReferencedByRule reports whether a group is referenced by any detection rule —
// either a LIVE published scoped rule (a DetectionRuleScopeRef row) OR a DRAFT scoped rule
// (an authoring DetectionRule row, enabled or not) — at any version, gating DeleteEntityGroup
// with ErrEntityInUse (ADR-062). A live rule pinning group@v needs the version's frozen
// selector stamped at resolution, so the group must outlive it. A draft pin counts too: it
// would fail its next publish closed if the group vanished, so blocking the delete surfaces
// the conflict at delete time rather than at a later, more surprising publish. It runs on the
// caller's handle so DeleteEntityGroup can re-check it INSIDE its lock+txn (closing the
// check-then-delete race against a concurrent publish).
func (api *Api) entityGroupReferencedByRule(ctx context.Context, db *gorm.DB, groupId uint) (bool, error) {
	var token string
	if err := db.Model(&EntityGroup{}).Where("id = ?", groupId).
		Select("token").Scan(&token).Error; err != nil {
		return false, err
	}
	if token == "" {
		return false, nil
	}
	var published int64
	if err := db.Model(&DetectionRuleScopeRef{}).
		Where("group_token = ?", token).Count(&published).Error; err != nil {
		return false, err
	}
	if published > 0 {
		return true, nil
	}
	var draft int64
	if err := db.Model(&DetectionRule{}).
		Where("entity_group_token = ?", token).Count(&draft).Error; err != nil {
		return false, err
	}
	return draft > 0, nil
}

// EntityGroupVersionByNumber loads a single frozen version of a group by number, returning
// gorm.ErrRecordNotFound when the group or version is absent. It is the read a
// rule-scoped resolve uses (ADR-062 S2/S3) to obtain the frozen selector a rule
// pinned, independent of the group's current draft or active pointer.
func (api *Api) EntityGroupVersionByNumber(ctx context.Context, groupId uint, version int32) (*EntityGroupVersion, error) {
	var v EntityGroupVersion
	err := api.RDB.DB(ctx).Where("entity_group_id = ? AND version = ?", groupId, version).First(&v).Error
	if err != nil {
		return nil, err
	}
	return &v, nil
}
