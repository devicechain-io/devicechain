// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Data required to create a customer type.
type CustomerTypeCreateRequest struct {
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

// Represents a customer type.
type CustomerType struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity

	Customers []Customer
}

// Search criteria for locating customer types.
type CustomerTypeSearchCriteria struct {
	rdb.Pagination
}

// Results for customer type search.
type CustomerTypeSearchResults struct {
	Results    []CustomerType
	Pagination rdb.SearchResultsPagination
}

// Data required to create a customer.
type CustomerCreateRequest struct {
	Token             string
	Name              *string
	Description       *string
	CustomerTypeToken string
	Metadata          *string
}

// Represents a customer.
type Customer struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	CustomerTypeId uint
	CustomerType   *CustomerType
}

// Search criteria for locating customers.
type CustomerSearchCriteria struct {
	rdb.Pagination
	CustomerTypeToken *string
}

// Results for customer search.
type CustomerSearchResults struct {
	Results    []Customer
	Pagination rdb.SearchResultsPagination
}

// Data required to create a customer group.
type CustomerGroupCreateRequest struct {
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

// Represents a group of customers.
type CustomerGroup struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity
}

// Search criteria for locating customer groups.
type CustomerGroupSearchCriteria struct {
	rdb.Pagination
}

// Results for customer group search.
type CustomerGroupSearchResults struct {
	Results    []CustomerGroup
	Pagination rdb.SearchResultsPagination
}
