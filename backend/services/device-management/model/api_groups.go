// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-device-management/internal/selector"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// resolvedMembership is the validated outcome of a create/update request's membership
// fields. For a dynamic group Sel is the compiled, cost-gated, guaranteed-lowerable
// selector (nil for static).
type resolvedMembership struct {
	Mode MembershipMode
	Sel  *selector.Selector
}

// resolveMembershipMode validates a create request's membership mode + member family and
// (for a dynamic group) compiles + cost-gates its selector (ADR-061 G3). A nil mode
// defaults to static. A static request must carry no selector; a dynamic request must
// carry one, which is compiled here so an un-lowerable or too-expensive selector fails
// closed at write rather than at resolve.
func resolveMembershipMode(request *EntityGroupCreateRequest) (*resolvedMembership, error) {
	if !ValidGroupMemberType(entity.Type(request.MemberType)) {
		return nil, fmt.Errorf("invalid group member type %q (must be one of device, asset, area, customer)", request.MemberType)
	}
	mode := MembershipStatic
	if request.MembershipMode != nil {
		mode = MembershipMode(*request.MembershipMode)
	}
	switch mode {
	case MembershipStatic:
		if request.Selector != nil && *request.Selector != "" {
			return nil, fmt.Errorf("a static entity group must not carry a selector")
		}
		return &resolvedMembership{Mode: MembershipStatic}, nil
	case MembershipDynamic:
		if request.Selector == nil || *request.Selector == "" {
			return nil, fmt.Errorf("a dynamic entity group requires a selector")
		}
		// Compile against the platform-default cost ceiling (0 → default; a per-tenant
		// override is a later ADR-023 wiring — never unlimited). MemberType is fixed for
		// the group, so the selector is checked for exactly this family.
		sel, err := selector.Compile(*request.Selector, request.MemberType, 0)
		if err != nil {
			return nil, err
		}
		return &resolvedMembership{Mode: MembershipDynamic, Sel: sel}, nil
	default:
		return nil, fmt.Errorf("invalid membership mode %q (must be static or dynamic)", mode)
	}
}

// CreateEntityGroup creates a new uniform entity group (ADR-061). It validates the
// member family and membership mode; a dynamic group's selector is compiled + cost-gated
// here (G3), and its lowerable CEL + the selector-env schema it was checked against are
// stamped onto the row so a resolve never runs an un-vetted selector.
func (api *Api) CreateEntityGroup(ctx context.Context, request *EntityGroupCreateRequest) (*EntityGroup, error) {
	membership, err := resolveMembershipMode(request)
	if err != nil {
		return nil, err
	}
	mode := membership.Mode
	selectorCol := sql.NullString{}
	selectorSchema := 0
	if membership.Sel != nil {
		selectorCol = sql.NullString{String: membership.Sel.Source(), Valid: true}
		selectorSchema = selector.SchemaVersion
	}
	created := &EntityGroup{
		TokenReference: rdb.TokenReference{
			Token: request.Token,
		},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		BrandedEntity: rdb.BrandedEntity{
			ImageUrl:        rdb.NullStrOf(request.ImageUrl),
			Icon:            rdb.NullStrOf(request.Icon),
			BackgroundColor: rdb.NullStrOf(request.BackgroundColor),
			ForegroundColor: rdb.NullStrOf(request.ForegroundColor),
			BorderColor:     rdb.NullStrOf(request.BorderColor),
		},
		MetadataEntity: rdb.MetadataEntity{
			Metadata: rdb.MetadataStrOf(request.Metadata),
		},
		MemberType:     request.MemberType,
		MembershipMode: string(mode),
		Selector:       selectorCol,
		SelectorSchema: selectorSchema,
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// UpdateEntityGroup updates an existing entity group's mutable presentation fields
// (name, description, branding, metadata). The member family and membership mode
// are identity and are immutable — changing MemberType would orphan the group's
// members, so a mismatch fails closed.
func (api *Api) UpdateEntityGroup(ctx context.Context, token string,
	request *EntityGroupCreateRequest) (*EntityGroup, error) {
	matches, err := api.EntityGroupsByToken(ctx, []string{request.Token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	updated := matches[0]
	if request.MemberType != "" && request.MemberType != updated.MemberType {
		return nil, fmt.Errorf("an entity group's member type is immutable (was %q, requested %q)",
			updated.MemberType, request.MemberType)
	}
	// Membership mode is identity — converting a static group to dynamic (or back)
	// would orphan its members, so a mismatch fails closed.
	if request.MembershipMode != nil && MembershipMode(*request.MembershipMode) != MembershipMode(updated.MembershipMode) {
		return nil, fmt.Errorf("an entity group's membership mode is immutable (was %q, requested %q)",
			updated.MembershipMode, *request.MembershipMode)
	}
	// A dynamic group's selector is live-editable (SD-4 — a browse/filter consumer needs
	// no frozen version yet); a provided selector is re-compiled + cost-gated before it
	// replaces the stored one. A static group must never carry a selector.
	if request.Selector != nil && *request.Selector != "" {
		if MembershipMode(updated.MembershipMode) != MembershipDynamic {
			return nil, fmt.Errorf("a static entity group must not carry a selector")
		}
		sel, err := selector.Compile(*request.Selector, updated.MemberType, 0)
		if err != nil {
			return nil, err
		}
		updated.Selector = sql.NullString{String: sel.Source(), Valid: true}
		updated.SelectorSchema = selector.SchemaVersion
	}
	updated.Token = request.Token
	updated.Name = rdb.NullStrOf(request.Name)
	updated.Description = rdb.NullStrOf(request.Description)
	updated.ImageUrl = rdb.NullStrOf(request.ImageUrl)
	updated.Icon = rdb.NullStrOf(request.Icon)
	updated.BackgroundColor = rdb.NullStrOf(request.BackgroundColor)
	updated.ForegroundColor = rdb.NullStrOf(request.ForegroundColor)
	updated.BorderColor = rdb.NullStrOf(request.BorderColor)
	updated.Metadata = rdb.MetadataStrOf(request.Metadata)

	// Omit active_version: a draft edit must never write the version pointer back.
	// The struct was loaded before this Save, so writing it whole would let an edit
	// racing a concurrent PublishEntityGroup/RollbackEntityGroup silently revert the
	// active pointer to its stale value — a rule-load-bearing field once S2/S3 read it
	// (ADR-062). The pointer is moved only by publish/rollback.
	result := api.RDB.DB(ctx).Omit("ActiveVersion").Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// EntityGroupsById gets entity groups by id.
func (api *Api) EntityGroupsById(ctx context.Context, ids []uint) ([]*EntityGroup, error) {
	found := make([]*EntityGroup, 0)
	result := api.RDB.DB(ctx).Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// EntityGroupsByToken gets entity groups by token.
func (api *Api) EntityGroupsByToken(ctx context.Context, tokens []string) ([]*EntityGroup, error) {
	found := make([]*EntityGroup, 0)
	result := api.RDB.DB(ctx).Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// EntityGroups searches for entity groups that meet criteria, optionally filtered
// by member family and/or membership mode.
func (api *Api) EntityGroups(ctx context.Context, criteria EntityGroupSearchCriteria) (*EntityGroupSearchResults, error) {
	results := make([]EntityGroup, 0)
	db, pag := api.RDB.ListOf(ctx, &EntityGroup{}, func(db *gorm.DB) *gorm.DB {
		if criteria.MemberType != nil {
			db = db.Where("member_type = ?", *criteria.MemberType)
		}
		if criteria.MembershipMode != nil {
			db = db.Where("membership_mode = ?", *criteria.MembershipMode)
		}
		return db
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	return &EntityGroupSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// DeleteEntityGroup deletes an entity group, its relationship (membership) edges,
// and its frozen versions. deleteEdgeEntity tears down every edge the group
// participates in, so a static group's "member" edges are removed with it (a dynamic
// group has none); the cascade removes the group's EntityGroupVersion history.
//
// It fails closed with ErrEntityInUse if a detection rule scopes to the group
// (ADR-062): a rule pinning group@v needs the version's frozen selector to stamp
// membership, so the group must outlive the rule.
func (api *Api) DeleteEntityGroup(ctx context.Context, token string) (bool, error) {
	group, err := api.entityGroupByToken(ctx, token)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil // deleting a nonexistent token is a no-op, mirroring deleteEdgeEntity
		}
		return false, err
	}
	// Collect the members that will lose this group's membership so their caches can be
	// evicted post-delete (ADR-062). Gathered before the cascade tears the rows down.
	var evictType string
	var evictIds []uint
	var members []EntityGroupMembership
	if err := api.RDB.DB(ctx).Where("group_id = ?", group.ID).Find(&members).Error; err != nil {
		return false, err
	}
	for _, m := range members {
		evictType = m.EntityType
		evictIds = append(evictIds, m.EntityId)
	}
	// Cascade the group's frozen versions AND its scoping rows (ADR-062 S2 membership +
	// reverse-index rows where it is the scoping group) with it: append-only history and
	// derived scoping rows have no meaning once the group is gone.
	//
	// The in-use guard runs INSIDE the delete transaction, under the group family's advisory
	// lock, so a concurrent publish that is enrolling a rule against this group serializes
	// behind us and either its ref rows are committed before our guard reads (we block) or our
	// delete commits before its enrollment (its register re-materializes nothing for a gone
	// group — and its guard-less publish is itself gated by the same lock). Without this,
	// check-then-delete could drop a group a just-published rule pins.
	deleted, err := api.deleteEdgeEntity(ctx, entity.TypeGroup, &EntityGroup{}, token, func(tx *gorm.DB, id uint) error {
		if err := api.lockMembershipFamily(ctx, tx, group.MemberType); err != nil {
			return err
		}
		inUse, err := api.entityGroupReferencedByRule(ctx, tx, id)
		if err != nil {
			return err
		}
		if inUse {
			return fmt.Errorf("%w: entity group %q is referenced by a detection rule", ErrEntityInUse, token)
		}
		if err := tx.Unscoped().Where("entity_group_id = ?", id).Delete(&EntityGroupVersion{}).Error; err != nil {
			return err
		}
		return api.purgeGroupScopingRows(tx, id)
	})
	if err != nil {
		return false, err
	}
	if deleted {
		api.evictMemberships(ctx, evictType, evictIds)
		api.evictScopedGroupsExist(ctx)
	}
	return deleted, nil
}
