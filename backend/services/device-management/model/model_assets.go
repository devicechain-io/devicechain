// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Data required to create an asset type.
type AssetTypeCreateRequest struct {
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

// AssetTypeUpdateRequest is the PARTIAL-update counterpart of
// AssetTypeCreateRequest. Every field carries the three states the Optional*
// types express: omitted leaves the stored value alone, an explicit null clears it,
// a value sets it.
//
// The shape this replaces reused AssetTypeCreateRequest, which made an update a
// FULL REPLACE — a caller renaming an asset type wiped its imageUrl, icon, three
// colours and its metadata, successfully, and got the emptied entity back with a 200.
// The console papered over that by reading the whole record and writing it back
// (assetTypePreserved), which bought a lost update in exchange: two operators on
// two tabs each wrote the other's fields back from their own stale snapshot.
//
// 🔴 TOKEN IS DELIBERATELY ABSENT, exactly as on DeviceTypeUpdateRequest. The old
// path did not merely allow a token move, it made the payload token the LOOKUP KEY
// and ignored the mutation's own `token` argument entirely — so a caller who sent
// a `token` argument naming one type and a `request.token` naming another silently
// updated the second and got a 200 for it. The argument is now the only identity
// channel, and a token move is unrepresentable rather than merely refused.
type AssetTypeUpdateRequest struct {
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

// Represents an asset type.
type AssetType struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity

	Assets []Asset
}

// DefaultOrder implements rdb.Sortable with the registry default: newest first, token
// as the unique tiebreak (created_at alone is not total under a bulk seed).
func (AssetType) DefaultOrder() string {
	return "asset_types.created_at DESC, asset_types.token ASC"
}

// Search criteria for locating asset types.
type AssetTypeSearchCriteria struct {
	rdb.Pagination
}

// Results for asset type search.
type AssetTypeSearchResults struct {
	Results    []AssetType
	Pagination rdb.SearchResultsPagination
}

// Data required to create an asset.
type AssetCreateRequest struct {
	Token          string
	Name           *string
	Description    *string
	AssetTypeToken string
	Metadata       *string
}

// AssetUpdateRequest is the PARTIAL-update counterpart of AssetCreateRequest.
// Omitted leaves the stored value alone, an explicit null clears it, a value sets it.
//
// 🔴 TOKEN IS DELIBERATELY ABSENT — see AssetTypeUpdateRequest for why; the same
// dead-argument defect applied here.
type AssetUpdateRequest struct {
	Name        dcgraphql.OptionalString
	Description dcgraphql.OptionalString
	// AssetTypeToken re-points the AssetType this asset belongs to. Omitted keeps the
	// current type. Unlike DeviceType's profileToken, an explicit NULL IS REFUSED: the
	// asset_type_id column is NOT NULL, so "no type" is not a state a asset can be in,
	// and accepting a null here would either write a dangling zero FK or silently
	// ignore what the caller asked for. An unknown token is refused too, and the
	// refusal is total — nothing is written.
	AssetTypeToken dcgraphql.OptionalString
	Metadata       dcgraphql.OptionalString
}

// Represents an asset.
type Asset struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	AssetTypeId uint
	AssetType   *AssetType
}

// DefaultOrder implements rdb.Sortable with the registry default: newest first, token
// as the unique tiebreak. Assets are also paged as a group member family, where the
// closure's own order leads and this one only tiebreaks.
func (Asset) DefaultOrder() string {
	return "assets.created_at DESC, assets.token ASC"
}

// Search criteria for locating assets.
type AssetSearchCriteria struct {
	rdb.Pagination
	AssetTypeToken *string
}

// Results for asset search.
type AssetSearchResults struct {
	Results    []Asset
	Pagination rdb.SearchResultsPagination
}
