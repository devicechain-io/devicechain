// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// This file holds the uniform entity-relationship API (ADR-013). One set of
// methods over the single EntityRelationship edge table replaces the per-family
// relationship CRUD that previously duplicated this logic four times.

// CreateEntityRelationshipType creates a new relationship type.
func (api *Api) CreateEntityRelationshipType(ctx context.Context,
	request *EntityRelationshipTypeCreateRequest) (*EntityRelationshipType, error) {
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	created := &EntityRelationshipType{
		TokenReference: rdb.TokenReference{Token: request.Token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		MetadataEntity: rdb.MetadataEntity{Metadata: metadataJSON},
		Tracked:        request.Tracked,
	}
	if err := api.RDB.DB(ctx).Create(created).Error; err != nil {
		return nil, err
	}
	return created, nil
}

// UpdateEntityRelationshipType applies a PARTIAL update to a relationship type: a
// field the caller did not name keeps its stored value, an explicit null clears a
// nullable one, and `tracked` — which has no nullable reading — refuses a null.
func (api *Api) UpdateEntityRelationshipType(ctx context.Context, token string,
	request *EntityRelationshipTypeUpdateRequest) (*EntityRelationshipType, error) {
	matches, err := api.EntityRelationshipTypesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	updated := matches[0]
	// Tracked resolves BEFORE anything is written, so a refused `tracked: null` refuses
	// the whole update rather than applying the other fields first.
	tracked, err := request.Tracked.ApplyToRequired("tracked", updated.Tracked)
	if err != nil {
		return nil, err
	}
	updated.Name = rdb.NullStrOf(request.Name.ApplyTo(dcgraphql.NullStr(updated.Name)))
	updated.Description = rdb.NullStrOf(request.Description.ApplyTo(dcgraphql.NullStr(updated.Description)))
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.ApplyTo(dcgraphql.MetadataStr(updated.Metadata)))
	if err != nil {
		return nil, err
	}
	updated.Metadata = metadataJSON
	updated.Tracked = tracked
	if err := api.RDB.DB(ctx).Save(updated).Error; err != nil {
		return nil, err
	}
	return updated, nil
}

// EntityRelationshipTypesById looks up relationship types by id.
func (api *Api) EntityRelationshipTypesById(ctx context.Context, ids []uint) ([]*EntityRelationshipType, error) {
	return rdb.FindByIds[EntityRelationshipType](api.RDB.DB(ctx), ids)
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
	// A reserved type is auto-provisioned on first use, exactly as the bulk path
	// does it. Without this a caller naming "contains" (or "member", or "assigned")
	// before anything else in the tenant had used it got ErrRecordNotFound for a
	// type the platform owns and would have created for them.
	reserved, err := api.ensureReservedTypeByToken(ctx, request.RelationshipType)
	if err != nil {
		return nil, err
	}
	rtmatches := []*EntityRelationshipType{}
	if reserved != nil {
		rtmatches = append(rtmatches, reserved)
	} else {
		rtmatches, err = api.EntityRelationshipTypesByToken(ctx, []string{request.RelationshipType})
		if err != nil {
			return nil, err
		}
	}
	if len(rtmatches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// The asset hierarchy's structural contract (ADR-072) is enforced HERE as well
	// as in SetAssetParent, because this generic mutation can create a "contains"
	// edge directly. An invariant checked only in the convenience door is an
	// invariant with a public bypass. No-op for every other relationship type.
	if err := api.admitContainmentEdge(api.RDB.DB(ctx), request.RelationshipType,
		request.SourceType, sourceId, request.TargetType, targetId); err != nil {
		return nil, err
	}

	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	created := &EntityRelationship{
		TokenReference:     rdb.TokenReference{Token: request.Token},
		MetadataEntity:     rdb.MetadataEntity{Metadata: metadataJSON},
		SourceType:         request.SourceType,
		SourceId:           sourceId,
		TargetType:         request.TargetType,
		TargetId:           targetId,
		TargetToken:        request.Target,
		RelationshipTypeId: rtmatches[0].ID,
	}
	if err := api.RDB.DB(ctx).Create(created).Error; err != nil {
		return nil, err
	}
	// Populate the association from the type we already resolved, so a resolver
	// selecting relationshipType on the mutation result gets real values. Read
	// paths Preload it; the create path must set it explicitly or it stays zero.
	created.RelationshipType = *rtmatches[0]
	return created, nil
}

// EntityRelationshipsById looks up relationships by id.
func (api *Api) EntityRelationshipsById(ctx context.Context, ids []uint) ([]*EntityRelationship, error) {
	return rdb.FindByIds[EntityRelationship](api.RDB.DB(ctx).Preload("RelationshipType"), ids)
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
		if criteria.TargetId != nil {
			result = result.Where("target_id = ?", *criteria.TargetId)
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
