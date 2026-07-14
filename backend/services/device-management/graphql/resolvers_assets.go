// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// -------------------
// Asset type resolver
// -------------------

type AssetTypeResolver struct {
	M model.AssetType
	S *SchemaResolver
	C context.Context
}

func (r *AssetTypeResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *AssetTypeResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *AssetTypeResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *AssetTypeResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *AssetTypeResolver) Token() string {
	return r.M.Token
}

func (r *AssetTypeResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *AssetTypeResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *AssetTypeResolver) ImageUrl() *string {
	return util.NullStr(r.M.ImageUrl)
}

func (r *AssetTypeResolver) Icon() *string {
	return util.NullStr(r.M.Icon)
}

func (r *AssetTypeResolver) BackgroundColor() *string {
	return util.NullStr(r.M.BackgroundColor)
}

func (r *AssetTypeResolver) ForegroundColor() *string {
	return util.NullStr(r.M.ForegroundColor)
}

func (r *AssetTypeResolver) BorderColor() *string {
	return util.NullStr(r.M.BorderColor)
}

func (r *AssetTypeResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

// ----------------------------------
// Asset type search results resolver
// ----------------------------------

type AssetTypeSearchResultsResolver struct {
	M model.AssetTypeSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *AssetTypeSearchResultsResolver) Results() []*AssetTypeResolver {
	resolvers := make([]*AssetTypeResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&AssetTypeResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *AssetTypeSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// --------------
// Asset resolver
// --------------

type AssetResolver struct {
	M model.Asset
	S *SchemaResolver
	C context.Context
}

func (r *AssetResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *AssetResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *AssetResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *AssetResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *AssetResolver) Token() string {
	return r.M.Token
}

func (r *AssetResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *AssetResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *AssetResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *AssetResolver) AssetType() *AssetTypeResolver {
	if r.M.AssetType != nil {
		return &AssetTypeResolver{
			M: *r.M.AssetType,
			S: r.S,
			C: r.C,
		}
	} else {
		ids := []string{fmt.Sprintf("%d", r.M.AssetTypeId)}
		matches, err := r.S.AssetTypesById(r.C, struct{ Ids []string }{Ids: ids})
		if err != nil {
			return nil
		}
		if len(matches) == 0 {
			return nil
		}
		return matches[0]
	}
}

// -----------------------------
// Asset search results resolver
// -----------------------------

type AssetSearchResultsResolver struct {
	M model.AssetSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *AssetSearchResultsResolver) Results() []*AssetResolver {
	resolvers := make([]*AssetResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&AssetResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *AssetSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}
