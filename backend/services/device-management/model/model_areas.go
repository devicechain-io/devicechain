// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Data required to create an area type.
type AreaTypeCreateRequest struct {
	Token           string
	Name            *string
	Description     *string
	ImageUrl        *string
	Icon            *string
	BackgroundColor *string
	ForegroundColor *string
	BorderColor     *string
	Metadata        *string
}

// Represents an area type.
type AreaType struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity

	Areas []Area
}

// DefaultOrder implements rdb.Sortable with the registry default: newest first, token
// as the unique tiebreak (created_at alone is not total under a bulk seed).
func (AreaType) DefaultOrder() string {
	return "area_types.created_at DESC, area_types.token ASC"
}

// Search criteria for locating area types.
type AreaTypeSearchCriteria struct {
	rdb.Pagination
}

// Results for area type search.
type AreaTypeSearchResults struct {
	Results    []AreaType
	Pagination rdb.SearchResultsPagination
}

// Data required to create an area.
type AreaCreateRequest struct {
	Token         string
	Name          *string
	Description   *string
	AreaTypeToken string
	Metadata      *string
}

// Represents an area.
type Area struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	AreaTypeId uint
	AreaType   *AreaType
}

// DefaultOrder implements rdb.Sortable with the registry default: newest first, token
// as the unique tiebreak. Areas are also paged as a group member family, where the
// closure's own order leads and this one only tiebreaks.
func (Area) DefaultOrder() string {
	return "areas.created_at DESC, areas.token ASC"
}

// Search criteria for locating areas.
type AreaSearchCriteria struct {
	rdb.Pagination
	AreaTypeToken *string
}

// Results for area search.
type AreaSearchResults struct {
	Results    []Area
	Pagination rdb.SearchResultsPagination
}
