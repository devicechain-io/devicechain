// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// This file holds the uniform entity-relationship API (ADR-013). One set of
// methods over the single EntityRelationship edge table replaces the per-family
// relationship CRUD that previously duplicated this logic four times.

// CreateEntityRelationshipType creates a new relationship type.
func (api *Api) CreateEntityRelationshipType(ctx context.Context,
	request *EntityRelationshipTypeCreateRequest) (*EntityRelationshipType, error) {
	created := &EntityRelationshipType{
		TokenReference: rdb.TokenReference{Token: request.Token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		MetadataEntity: rdb.MetadataEntity{Metadata: rdb.MetadataStrOf(request.Metadata)},
		Tracked:        request.Tracked,
	}
	if err := api.RDB.DB(ctx).Create(created).Error; err != nil {
		return nil, err
	}
	return created, nil
}

// UpdateEntityRelationshipType updates an existing relationship type by token.
func (api *Api) UpdateEntityRelationshipType(ctx context.Context, token string,
	request *EntityRelationshipTypeCreateRequest) (*EntityRelationshipType, error) {
	matches, err := api.EntityRelationshipTypesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	updated := matches[0]
	updated.Token = request.Token
	updated.Name = rdb.NullStrOf(request.Name)
	updated.Description = rdb.NullStrOf(request.Description)
	updated.Metadata = rdb.MetadataStrOf(request.Metadata)
	updated.Tracked = request.Tracked
	if err := api.RDB.DB(ctx).Save(updated).Error; err != nil {
		return nil, err
	}
	return updated, nil
}

// EntityRelationshipTypesById looks up relationship types by id.
func (api *Api) EntityRelationshipTypesById(ctx context.Context, ids []uint) ([]*EntityRelationshipType, error) {
	found := make([]*EntityRelationshipType, 0)
	if err := api.RDB.DB(ctx).Find(&found, ids).Error; err != nil {
		return nil, err
	}
	return found, nil
}

// EntityRelationshipTypesByToken looks up relationship types by token.
func (api *Api) EntityRelationshipTypesByToken(ctx context.Context, tokens []string) ([]*EntityRelationshipType, error) {
	found := make([]*EntityRelationshipType, 0)
	if err := api.RDB.DB(ctx).Find(&found, "token in ?", tokens).Error; err != nil {
		return nil, err
	}
	return found, nil
}

// EntityRelationshipTypes searches relationship types.
func (api *Api) EntityRelationshipTypes(ctx context.Context,
	criteria EntityRelationshipTypeSearchCriteria) (*EntityRelationshipTypeSearchResults, error) {
	results := make([]EntityRelationshipType, 0)
	db, pag := api.RDB.ListOf(ctx, &EntityRelationshipType{}, nil, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &EntityRelationshipTypeSearchResults{Results: results, Pagination: pag}, nil
}

// CreateEntityRelationship creates a relationship edge. The source and target are
// each resolved from (type, token) to a row id via the entity-type registry,
// which validates both the type and the entity's existence (ADR-013).
func (api *Api) CreateEntityRelationship(ctx context.Context,
	request *EntityRelationshipCreateRequest) (*EntityRelationship, error) {
	sourceId, err := api.ResolveEntityToken(ctx, request.SourceType, request.Source)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	targetId, err := api.ResolveEntityToken(ctx, request.TargetType, request.Target)
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	rtmatches, err := api.EntityRelationshipTypesByToken(ctx, []string{request.RelationshipType})
	if err != nil {
		return nil, err
	}
	if len(rtmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	created := &EntityRelationship{
		TokenReference:     rdb.TokenReference{Token: request.Token},
		MetadataEntity:     rdb.MetadataEntity{Metadata: rdb.MetadataStrOf(request.Metadata)},
		SourceType:         request.SourceType,
		SourceId:           sourceId,
		TargetType:         request.TargetType,
		TargetId:           targetId,
		RelationshipTypeId: rtmatches[0].ID,
	}
	if err := api.RDB.DB(ctx).Create(created).Error; err != nil {
		return nil, err
	}
	return created, nil
}

// EntityRelationshipsById looks up relationships by id.
func (api *Api) EntityRelationshipsById(ctx context.Context, ids []uint) ([]*EntityRelationship, error) {
	found := make([]*EntityRelationship, 0)
	if err := api.RDB.DB(ctx).Preload("RelationshipType").Find(&found, ids).Error; err != nil {
		return nil, err
	}
	return found, nil
}

// EntityRelationshipsByToken looks up relationships by token.
func (api *Api) EntityRelationshipsByToken(ctx context.Context, tokens []string) ([]*EntityRelationship, error) {
	found := make([]*EntityRelationship, 0)
	if err := api.RDB.DB(ctx).Preload("RelationshipType").Find(&found, "token in ?", tokens).Error; err != nil {
		return nil, err
	}
	return found, nil
}

// EntityRelationships searches relationships. Source is matched by the resolved
// (SourceType, SourceId); Tracked filters by the relationship type's flag.
func (api *Api) EntityRelationships(ctx context.Context,
	criteria EntityRelationshipSearchCriteria) (*EntityRelationshipSearchResults, error) {
	results := make([]EntityRelationship, 0)
	db, pag := api.RDB.ListOf(ctx, &EntityRelationship{}, func(result *gorm.DB) *gorm.DB {
		if criteria.SourceType != nil {
			result = result.Where("source_type = ?", *criteria.SourceType)
		}
		if criteria.SourceId != nil {
			result = result.Where("source_id = ?", *criteria.SourceId)
		}
		if criteria.TargetType != nil {
			result = result.Where("target_type = ?", *criteria.TargetType)
		}
		if criteria.RelationshipType != nil {
			result = result.Where("relationship_type_id = (?)",
				api.RDB.DB(ctx).Model(&EntityRelationshipType{}).Select("id").Where("token = ?", *criteria.RelationshipType))
		}
		if criteria.Tracked != nil {
			result = result.Where("relationship_type_id in (?)",
				api.RDB.DB(ctx).Model(&EntityRelationshipType{}).Select("id").Where("tracked = ?", *criteria.Tracked))
		}
		return result
	}, criteria.Pagination)
	db.Preload("RelationshipType").Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &EntityRelationshipSearchResults{Results: results, Pagination: pag}, nil
}
