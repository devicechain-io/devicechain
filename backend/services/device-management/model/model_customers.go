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

// DefaultOrder implements rdb.Sortable with the registry default: newest first, token
// as the unique tiebreak (created_at alone is not total under a bulk seed).
func (CustomerType) DefaultOrder() string {
	return "customer_types.created_at DESC, customer_types.token ASC"
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

// DefaultOrder implements rdb.Sortable with the registry default: newest first, token
// as the unique tiebreak. Customers are also paged as a group member family, where the
// closure's own order leads and this one only tiebreaks.
func (Customer) DefaultOrder() string {
	return "customers.created_at DESC, customers.token ASC"
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
