// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
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

// CustomerTypeUpdateRequest is the PARTIAL-update counterpart of
// CustomerTypeCreateRequest. Every field carries the three states the Optional*
// types express: omitted leaves the stored value alone, an explicit null clears it,
// a value sets it.
//
// The shape this replaces reused CustomerTypeCreateRequest, which made an update a
// FULL REPLACE — a caller renaming a customer type wiped its imageUrl, icon, three
// colours and its metadata, successfully, and got the emptied entity back with a 200.
// The console papered over that by reading the whole record and writing it back
// (customerTypePreserved), which bought a lost update in exchange: two operators on
// two tabs each wrote the other's fields back from their own stale snapshot.
//
// 🔴 TOKEN IS DELIBERATELY ABSENT, exactly as on DeviceTypeUpdateRequest. The old
// path did not merely allow a token move, it made the payload token the LOOKUP KEY
// and ignored the mutation's own `token` argument entirely — so a caller who sent
// a `token` argument naming one type and a `request.token` naming another silently
// updated the second and got a 200 for it. The argument is now the only identity
// channel, and a token move is unrepresentable rather than merely refused.
type CustomerTypeUpdateRequest struct {
	Name            dcgraphql.OptionalString
	Description     dcgraphql.OptionalString
	ImageUrl        dcgraphql.OptionalString
	Icon            dcgraphql.OptionalString
	BackgroundColor dcgraphql.OptionalString
	ForegroundColor dcgraphql.OptionalString
	BorderColor     dcgraphql.OptionalString
	// Metadata is an opaque JSON string in the schema rather than a map, so it
	// replaces wholesale when sent and clears on null. There is no per-key merge to
	// choose between: the API has never been able to address an individual key.
	Metadata dcgraphql.OptionalString
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

// CustomerUpdateRequest is the PARTIAL-update counterpart of CustomerCreateRequest.
// Omitted leaves the stored value alone, an explicit null clears it, a value sets it.
//
// 🔴 TOKEN IS DELIBERATELY ABSENT — see CustomerTypeUpdateRequest for why; the same
// dead-argument defect applied here.
type CustomerUpdateRequest struct {
	Name        dcgraphql.OptionalString
	Description dcgraphql.OptionalString
	// CustomerTypeToken re-points the CustomerType this customer belongs to. Omitted keeps the
	// current type. Unlike DeviceType's profileToken, an explicit NULL IS REFUSED: the
	// customer_type_id column is NOT NULL, so "no type" is not a state a customer can be in,
	// and accepting a null here would either write a dangling zero FK or silently
	// ignore what the caller asked for. An unknown token is refused too, and the
	// refusal is total — nothing is written.
	CustomerTypeToken dcgraphql.OptionalString
	Metadata          dcgraphql.OptionalString
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
