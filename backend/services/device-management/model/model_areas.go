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
