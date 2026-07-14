// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// This file holds the facet-key registry API (ADR-061 SD-5). A facet key declares
// that an EntityAttribute key, for a member family, is a classification facet the
// console can offer as a browse/filter axis. It stores no values (those stay in
// EntityAttribute, SD-1); it only declares keys, their value type, an optional
// value vocabulary, and a label. Addressed by the natural key (MemberType, Key);
// SetFacetKey upserts on it, like SetEntityAttribute.

// facetValuesJSON marshals an optional value vocabulary to storable JSON. A nil
// slice clears the column (free-form facet).
func facetValuesJSON(values *[]string) (*datatypes.JSON, error) {
	if values == nil {
		return nil, nil
	}
	b, err := json.Marshal(*values)
	if err != nil {
		return nil, fmt.Errorf("marshal facet values: %w", err)
	}
	j := datatypes.JSON(b)
	return &j, nil
}

// validateFacetKeyRequest checks the member family + value type vocabularies a
// facet-key write must satisfy. The member family must be a group-collectable
// entity type; the value type must be a known AttributeValueType.
func validateFacetKeyRequest(request *FacetKeySetRequest) error {
	if !ValidGroupMemberType(entity.Type(request.MemberType)) {
		return fmt.Errorf("invalid facet member type %q (must be one of device, asset, area, customer)", request.MemberType)
	}
	if request.Key == "" {
		return fmt.Errorf("a facet key must have a non-empty key")
	}
	// Fail closed on an over-long key rather than surfacing a raw "value too long
	// for character varying(128)" from Postgres (the sqlite test harness would take
	// it silently). Matches the attr_key column width.
	if len(request.Key) > 128 {
		return fmt.Errorf("facet key %q is too long (max 128 characters)", request.Key)
	}
	if !AttributeValueType(request.ValueType).Valid() {
		return fmt.Errorf("invalid facet value type %q (must be one of STRING, LONG, DOUBLE, BOOLEAN, JSON)", request.ValueType)
	}
	return nil
}

// SetFacetKey declares (upserts) a facet key by its natural key (MemberType, Key).
// v1 writes only attribute-source facets (SD-5): the system-source arm is a
// declared-but-unbuilt seam, so a set that would land on an existing system facet
// is refused (system facets are predefined and non-editable by tenants).
func (api *Api) SetFacetKey(ctx context.Context, request *FacetKeySetRequest) (*FacetKey, error) {
	if err := validateFacetKeyRequest(request); err != nil {
		return nil, err
	}
	values, err := facetValuesJSON(request.Values)
	if err != nil {
		return nil, err
	}

	// Upsert on the natural key. The tenant predicate is added by the rdb scope
	// callback, so this matches only the caller-tenant's row.
	existing := &FacetKey{}
	result := api.RDB.DB(ctx).Where(
		"member_type = ? AND attr_key = ?", request.MemberType, request.Key).First(existing)
	if result.Error == nil {
		if FacetSource(existing.Source) == FacetSourceSystem {
			return nil, fmt.Errorf("facet %q for %s is a system facet and cannot be modified", request.Key, request.MemberType)
		}
		existing.ValueType = request.ValueType
		existing.Values = values
		existing.Label = rdb.NullStrOf(request.Label)
		existing.Source = string(FacetSourceAttribute)
		if err := api.RDB.DB(ctx).Save(existing).Error; err != nil {
			return nil, err
		}
		return existing, nil
	}
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, result.Error
	}

	created := &FacetKey{
		MemberType: request.MemberType,
		Key:        request.Key,
		ValueType:  request.ValueType,
		Source:     string(FacetSourceAttribute),
		Values:     values,
		Label:      rdb.NullStrOf(request.Label),
	}
	if err := api.RDB.DB(ctx).Create(created).Error; err != nil {
		return nil, err
	}
	return created, nil
}

// FacetKeys searches the facet registry, optionally filtered to one member family.
func (api *Api) FacetKeys(ctx context.Context, criteria FacetKeySearchCriteria) (*FacetKeySearchResults, error) {
	results := make([]FacetKey, 0)
	db, pag := api.RDB.ListOf(ctx, &FacetKey{}, func(db *gorm.DB) *gorm.DB {
		if criteria.MemberType != nil {
			db = db.Where("member_type = ?", *criteria.MemberType)
		}
		return db
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &FacetKeySearchResults{Results: results, Pagination: pag}, nil
}

// DeleteFacetKey removes a facet key by its natural key (MemberType, Key). A
// system-source facet is non-deletable (SD-5) and its deletion is refused. Returns
// whether a row was removed (a gorm soft-delete, so the natural key frees for reuse
// via the partial-unique index).
func (api *Api) DeleteFacetKey(ctx context.Context, memberType string, key string) (bool, error) {
	existing := &FacetKey{}
	result := api.RDB.DB(ctx).Where(
		"member_type = ? AND attr_key = ?", memberType, key).First(existing)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if result.Error != nil {
		return false, result.Error
	}
	if FacetSource(existing.Source) == FacetSourceSystem {
		return false, fmt.Errorf("facet %q for %s is a system facet and cannot be deleted", key, memberType)
	}
	del := api.RDB.DB(ctx).Delete(existing)
	if del.Error != nil {
		return false, del.Error
	}
	return del.RowsAffected > 0, nil
}
