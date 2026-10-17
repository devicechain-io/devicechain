// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-device-management/internal/selector"
	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/entity"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
		// Compile against the platform cost ceiling. MemberType is fixed for the group, so
		// the selector is checked for exactly this family.
		sel, err := selector.Compile(*request.Selector, request.MemberType)
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
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
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
			Metadata: metadataJSON,
		},
		MemberType:     request.MemberType,
		MembershipMode: string(mode),
		Selector:       selectorCol,
		SelectorSchema: selectorSchema,
	}
	// The token is issued and the group written in ONE transaction, as for a customer
	// (CreateCustomer): a token is in the ledger exactly when a group was created at it, and
	// the ledger's primary key refuses a second issue whether the first group is live or gone.
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&IssuedGroupToken{Token: request.Token}).Error; err != nil {
			return err
		}
		return tx.Create(created).Error
	})
	if err != nil {
		if conflict.Is(err) {
			if rerr := api.refuseRetiredGroupToken(ctx, request.Token); rerr != nil {
				return nil, rerr
			}
		}
		return nil, err
	}
	return created, nil
}

// ErrGroupTokenRetired refuses a create at the token of an entity group that has been
// deleted in the same tenant. The refusal is permanent: a deleted group's token is never
// handed to another group.
//
// 🔑 WHY A DELETED GROUP STILL HOLDS ITS TOKEN. A group that is an access scope is named
// by token, as the anchor "group:<token>", on records that outlive it: the scope of every
// member limited to it (user-management), and the stamps on the alarms, commands, update
// assignments, live device states and event history written while a device was in it. A
// new group at the same token would inherit all of it — every member scoped to the old
// group would see the new one's devices, and the new group's members the old one's history.
// It is the rule ErrCustomerTokenRetired states for customers, and it is applied to EVERY
// group rather than only to access scopes, because a member's scope may name any group
// token: a group that never was an access scope can still be the one a member was limited to.
//
// It is INVALID_VALUE and never CONFLICT, for the reason ErrCustomerTokenRetired gives.
var ErrGroupTokenRetired = integrity.NewRefusal(integrity.ClassInvalid,
	"that group token is retired: a deleted group held it, and member access and records "+
		"stamped with it (alarms, commands, update assignments, device states and event history) "+
		"still name that group, so it cannot be given to another one. Pick another token")

// refuseRetiredGroupToken reads a unique violation on a group create, as
// refuseRetiredCustomerToken does for a customer: with no live group at token, the ledger
// refused it, and the token is retired.
func (api *Api) refuseRetiredGroupToken(ctx context.Context, token string) error {
	var live int64
	if err := api.RDB.DB(ctx).Model(&EntityGroup{}).
		Where("token = ?", token).Count(&live).Error; err != nil {
		return err
	}
	if live == 0 {
		return fmt.Errorf("create entity group %q: %w", token, ErrGroupTokenRetired)
	}
	return nil
}

// retireGroupToken records token as issued, inside a group delete's transaction — a no-op
// for a group CreateEntityGroup recorded, and the record for one that reached the table any
// other way.
func retireGroupToken(tx *gorm.DB, token string) error {
	return tx.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&IssuedGroupToken{Token: token}).Error
}

// UpdateEntityGroup applies a PARTIAL update to an entity group's mutable fields: a
// field the caller did not name keeps its stored value, an explicit null clears a
// nullable one.
//
// The member family, the membership mode and the token are IDENTITY and are not in the
// input at all, so changing one is unrepresentable rather than refused — see
// EntityGroupUpdateRequest for why that is the stronger form of the same rule.
func (api *Api) UpdateEntityGroup(ctx context.Context, token string,
	request *EntityGroupUpdateRequest) (*EntityGroup, error) {
	matches, err := api.EntityGroupsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	updated := matches[0]
	// A dynamic group's selector is live-editable (SD-4 — a browse/filter consumer needs
	// no frozen version yet); a provided selector is re-compiled + cost-gated before it
	// replaces the stored one. It is REQUIRED rather than nullable: a dynamic group with
	// no selector matches nothing and cannot be repaired into a valid one, so a null is
	// refused. A static group must never carry a selector at all.
	//
	// The fold runs before the mode check so that `selector: null` reads the same on both
	// modes — the field cannot be cleared, whatever the group is.
	selectorSource, err := request.Selector.ApplyToRequired("selector", updated.Selector.String)
	if err != nil {
		return nil, err
	}
	if request.Selector.Set {
		if MembershipMode(updated.MembershipMode) != MembershipDynamic {
			return nil, fmt.Errorf("a static entity group must not carry a selector")
		}
		sel, err := selector.Compile(selectorSource, updated.MemberType)
		if err != nil {
			return nil, err
		}
		updated.Selector = sql.NullString{String: sel.Source(), Valid: true}
		updated.SelectorSchema = selector.SchemaVersion
	}
	updated.Name = request.Name.ApplyToNullString(updated.Name)
	updated.Description = request.Description.ApplyToNullString(updated.Description)
	updated.ImageUrl = request.ImageUrl.ApplyToNullString(updated.ImageUrl)
	updated.Icon = request.Icon.ApplyToNullString(updated.Icon)
	updated.BackgroundColor = request.BackgroundColor.ApplyToNullString(updated.BackgroundColor)
	updated.ForegroundColor = request.ForegroundColor.ApplyToNullString(updated.ForegroundColor)
	updated.BorderColor = request.BorderColor.ApplyToNullString(updated.BorderColor)
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.ApplyTo(dcgraphql.MetadataStr(updated.Metadata)))
	if err != nil {
		return nil, err
	}
	updated.Metadata = metadataJSON

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
	return rdb.FindByIds[EntityGroup](api.RDB.DB(ctx), ids)
}

// EntityGroupsByToken gets entity groups by token.
func (api *Api) EntityGroupsByToken(ctx context.Context, tokens []string) ([]*EntityGroup, error) {
	found := make([]*EntityGroup, 0)
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &found, "token", tokens); err != nil {
		return nil, err
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
		if criteria.AccessScope != nil {
			if *criteria.AccessScope {
				db = db.Where("access_scope_at IS NOT NULL")
			} else {
				db = db.Where("access_scope_at IS NULL")
			}
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
	// lock, so a concurrent publish enrolling a rule against this group serializes on the same
	// lock. The two orderings both resolve safely: either the publish commits its scope-ref
	// rows first and our guard sees them and refuses the delete (ErrEntityInUse), or our delete
	// commits first and the publish's enrollment reconcile then fails closed on the vanished
	// group (loadGroupVersionForScoping → not-found → the whole publish rolls back). Without
	// this lock+in-txn re-check, plain check-then-delete could drop a group a just-published
	// rule pins.
	//
	// An access scope may be deleted: the devices in it lose the group's anchor, so each one's
	// ownership fact is queued (the members read inside the transaction, under the lock, so a
	// concurrent enable cannot slip between), and the token is retired either way.
	var accessMembers []uint
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
		if accessMembers, err = api.accessMembersOnTx(tx, id); err != nil {
			return err
		}
		if err := tx.Unscoped().Where("entity_group_id = ?", id).Delete(&EntityGroupVersion{}).Error; err != nil {
			return err
		}
		if err := api.purgeGroupScopingRows(tx, id); err != nil {
			return err
		}
		if err := retireGroupToken(tx, token); err != nil {
			return err
		}
		// The members lose the group's anchor: their ownership facts are queued here, at the
		// end of the cascade. The one statement deleteEdgeEntity runs after it is the group
		// row's own delete, and every other writer of that row takes the family lock this
		// transaction already holds, so nothing that queues can be waiting on it.
		_, err = queueOwnershipOnTx(tx, accessMembers)
		return err
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
