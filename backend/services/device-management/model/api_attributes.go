// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// This file holds the current-state EntityAttribute API (ADR-012). Attributes
// are addressed by uniform (entity_type, entity_id) (ADR-013) and resolved via
// the entity-type registry; unlike events, a write to the same (entity, scope,
// key) upserts the existing row.

// SetEntityAttribute upserts a single current-state attribute. The owner is
// resolved from (type, token) to a row id via the entity-type registry, which
// validates both the type and the entity's existence (ADR-012).
func (api *Api) SetEntityAttribute(ctx context.Context,
	request *EntityAttributeSetRequest) (*EntityAttribute, error) {
	// Validate the scope and value-type vocabularies.
	if !AttributeScope(request.Scope).Valid() {
		return nil, fmt.Errorf("invalid attribute scope: %s", request.Scope)
	}
	if !AttributeValueType(request.ValueType).Valid() {
		return nil, fmt.Errorf("invalid attribute value type: %s", request.ValueType)
	}

	// Resolve and validate the owner entity reference.
	entityId, err := api.ResolveEntityToken(ctx, request.EntityType, request.Entity)
	if err != nil {
		return nil, err
	}

	// Upsert: update an existing (entity, scope, key) row or create a new one.
	existing := &EntityAttribute{}
	result := api.RDB.DB(ctx).Where(
		"entity_type = ? AND entity_id = ? AND scope = ? AND attr_key = ?",
		request.EntityType, entityId, request.Scope, request.AttrKey).First(existing)
	if result.Error == nil {
		existing.ValueType = request.ValueType
		existing.Value = rdb.NullStrOf(request.Value)
		existing.LastUpdated = time.Now()
		if err := api.RDB.DB(ctx).Save(existing).Error; err != nil {
			return nil, err
		}
		return existing, nil
	}
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, result.Error
	}

	created := &EntityAttribute{
		EntityType:  request.EntityType,
		EntityId:    entityId,
		Scope:       request.Scope,
		AttrKey:     request.AttrKey,
		ValueType:   request.ValueType,
		Value:       rdb.NullStrOf(request.Value),
		LastUpdated: time.Now(),
	}
	if err := api.RDB.DB(ctx).Create(created).Error; err != nil {
		return nil, err
	}
	return created, nil
}

// EntityAttributes searches attributes. The owner is matched by the resolved
// (EntityType, EntityId); a caller-supplied Entity token is resolved first, and
// if it names no entity the search yields zero rows.
func (api *Api) EntityAttributes(ctx context.Context,
	criteria EntityAttributeSearchCriteria) (*EntityAttributeSearchResults, error) {
	// Resolve the owner token to an id up front (mirrors EntityRelationships).
	entityId := criteria.EntityId
	if criteria.Entity != nil && criteria.EntityType != nil {
		id, err := api.ResolveEntityToken(ctx, *criteria.EntityType, *criteria.Entity)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// No such entity: the search yields zero rows.
				return &EntityAttributeSearchResults{
					Results:    make([]EntityAttribute, 0),
					Pagination: rdb.SearchResultsPagination{},
				}, nil
			}
			return nil, err
		}
		entityId = &id
	}

	results := make([]EntityAttribute, 0)
	db, pag := api.RDB.ListOf(ctx, &EntityAttribute{}, func(result *gorm.DB) *gorm.DB {
		if criteria.EntityType != nil {
			result = result.Where("entity_type = ?", *criteria.EntityType)
		}
		if entityId != nil {
			result = result.Where("entity_id = ?", *entityId)
		}
		if criteria.Scope != nil {
			result = result.Where("scope = ?", *criteria.Scope)
		}
		if criteria.AttrKeys != nil && len(*criteria.AttrKeys) > 0 {
			result = result.Where("attr_key in ?", *criteria.AttrKeys)
		}
		return result
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &EntityAttributeSearchResults{Results: results, Pagination: pag}, nil
}

// DeleteEntityAttribute removes a single current-state attribute by its natural
// key (entity, scope, key). Returns whether a row was deleted. The delete is a
// gorm soft-delete (DeletedAt).
func (api *Api) DeleteEntityAttribute(ctx context.Context, entityType string,
	entity string, scope string, attrKey string) (bool, error) {
	entityId, err := api.ResolveEntityToken(ctx, entityType, entity)
	if err != nil {
		return false, err
	}
	result := api.RDB.DB(ctx).Where(
		"entity_type = ? AND entity_id = ? AND scope = ? AND attr_key = ?",
		entityType, entityId, scope, attrKey).Delete(&EntityAttribute{})
	return result.RowsAffected > 0, result.Error
}

// EntityAttributesByEntity loads all attributes for an entity (optionally scoped)
// without pagination. This is the direct loader the future device-state /
// shared-attribute-push path uses (ADR-012).
func (api *Api) EntityAttributesByEntity(ctx context.Context, entityType string,
	entityId uint, scope *string) ([]*EntityAttribute, error) {
	found := make([]*EntityAttribute, 0)
	result := api.RDB.DB(ctx).Where("entity_type = ? AND entity_id = ?", entityType, entityId)
	if scope != nil {
		result = result.Where("scope = ?", *scope)
	}
	if err := result.Find(&found).Error; err != nil {
		return nil, err
	}
	return found, nil
}
