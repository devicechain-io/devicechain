// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-device-management/internal/selector"
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
	sel, err := selector.Compile(group.Selector.String, group.MemberType, 0)
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
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(version).Error; err != nil {
			return err
		}
		res := tx.Model(&EntityGroup{}).Where("id = ?", group.ID).
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
		// ADR-062 S2 maintenance hook goes HERE, inside the publish txn after the
		// repoint: if this group is already rule-referenced, enroll the new group@v for
		// membership maintenance (backfill the read-model, extend the reverse index)
		// enlisted in this same transaction so the read-model advances atomically with
		// the active pointer. No-op in S1 — no rule can reference a group until S4 adds
		// the DetectionRule scope column, so there is nothing to enroll yet.
		return nil
	})
	if err != nil {
		return nil, err
	}
	return version, nil
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

	res := api.RDB.DB(ctx).Model(&EntityGroup{}).Where("id = ?", group.ID).
		Update("active_version", version)
	if res.Error != nil {
		return nil, res.Error
	}
	// The group was deleted between the existence check and here.
	if res.RowsAffected == 0 {
		return nil, fmt.Errorf("%w: entity group %q", gorm.ErrRecordNotFound, token)
	}
	// Reload so the returned group carries the freshly-bumped updated_at.
	return api.entityGroupByToken(ctx, token)
}

// EntityGroupVersions lists a group's published versions, newest first. Returns
// gorm.ErrRecordNotFound if the group does not exist.
func (api *Api) EntityGroupVersions(ctx context.Context, token string) ([]*EntityGroupVersion, error) {
	group, err := api.entityGroupByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	versions := make([]*EntityGroupVersion, 0)
	result := api.RDB.DB(ctx).Where("entity_group_id = ?", group.ID).
		Order("version DESC").Find(&versions)
	if result.Error != nil {
		return nil, result.Error
	}
	return versions, nil
}

// entityGroupReferencedByEnabledRule reports whether any enabled DetectionRule
// scopes to this group (ADR-062 reference-counting), gating DeleteEntityGroup with
// ErrEntityInUse. A rule scoped to group@v needs the version's frozen selector to
// stamp membership at resolution, so the group must outlive the rule.
//
// DetectionRules gain their optional EntityGroup@version scope in ADR-062 S4; until
// that column exists no rule can reference any group, so this is structurally false.
// S4 replaces the body with the enabled-rule count over the rule scope column. This
// is a forward seam that keeps DeleteEntityGroup's fail-closed contract stable across
// the slice boundary — not a compat shim for an old shape.
func (api *Api) entityGroupReferencedByEnabledRule(_ context.Context, _ uint) (bool, error) {
	return false, nil
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
