// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// resolveMembershipMode validates a create request's membership mode + member
// family and returns the normalized mode. A nil mode defaults to static. Dynamic
// mode is rejected until the selector engine (G3) lands — a dynamic group would
// have no way to resolve its members. Static requests must carry no selector.
func resolveMembershipMode(request *EntityGroupCreateRequest) (MembershipMode, error) {
	if !ValidGroupMemberType(entity.Type(request.MemberType)) {
		return "", fmt.Errorf("invalid group member type %q (must be one of device, asset, area, customer)", request.MemberType)
	}
	mode := MembershipStatic
	if request.MembershipMode != nil {
		mode = MembershipMode(*request.MembershipMode)
	}
	switch mode {
	case MembershipStatic:
		if request.Selector != nil && *request.Selector != "" {
			return "", fmt.Errorf("a static entity group must not carry a selector")
		}
		return MembershipStatic, nil
	case MembershipDynamic:
		// The CEL selector engine (ADR-061 G3) is not yet wired; a dynamic group
		// cannot be resolved, so creating one fails closed until then.
		return "", fmt.Errorf("dynamic entity groups are not yet supported")
	default:
		return "", fmt.Errorf("invalid membership mode %q (must be static or dynamic)", mode)
	}
}

// CreateEntityGroup creates a new uniform entity group (ADR-061). It validates the
// member family and membership mode; v1 only admits static groups.
func (api *Api) CreateEntityGroup(ctx context.Context, request *EntityGroupCreateRequest) (*EntityGroup, error) {
	mode, err := resolveMembershipMode(request)
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
			Metadata: rdb.MetadataStrOf(request.Metadata),
		},
		MemberType:     request.MemberType,
		MembershipMode: string(mode),
		Selector:       sql.NullString{}, // static: no selector (G3 sets this for dynamic)
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
	// Membership mode + selector are identity too. Update only touches presentation
	// fields in v1; reject a request that tries to convert the group's mode or set a
	// selector so a caller never believes a silent no-op took effect (converting a
	// static group to dynamic is a G3 concern, gated by the selector engine).
	if request.MembershipMode != nil && MembershipMode(*request.MembershipMode) != MembershipMode(updated.MembershipMode) {
		return nil, fmt.Errorf("an entity group's membership mode is immutable (was %q, requested %q)",
			updated.MembershipMode, *request.MembershipMode)
	}
	if request.Selector != nil && *request.Selector != "" {
		return nil, fmt.Errorf("entity group selectors are not yet supported")
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

	result := api.RDB.DB(ctx).Save(updated)
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

// DeleteEntityGroup deletes an entity group and its relationship (membership)
// edges. deleteEdgeEntity tears down every edge the group participates in, so a
// static group's "member" edges are removed with it; a dynamic group has none.
func (api *Api) DeleteEntityGroup(ctx context.Context, token string) (bool, error) {
	return api.deleteEdgeEntity(ctx, entity.TypeGroup, &EntityGroup{}, token, nil)
}
