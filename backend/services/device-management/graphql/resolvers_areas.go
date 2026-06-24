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

// ------------------
// Area type resolver
// ------------------

type AreaTypeResolver struct {
	M model.AreaType
	S *SchemaResolver
	C context.Context
}

func (r *AreaTypeResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *AreaTypeResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *AreaTypeResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *AreaTypeResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *AreaTypeResolver) Token() string {
	return r.M.Token
}

func (r *AreaTypeResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *AreaTypeResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *AreaTypeResolver) ImageUrl() *string {
	return util.NullStr(r.M.ImageUrl)
}

func (r *AreaTypeResolver) Icon() *string {
	return util.NullStr(r.M.Icon)
}

func (r *AreaTypeResolver) BackgroundColor() *string {
	return util.NullStr(r.M.BackgroundColor)
}

func (r *AreaTypeResolver) ForegroundColor() *string {
	return util.NullStr(r.M.ForegroundColor)
}

func (r *AreaTypeResolver) BorderColor() *string {
	return util.NullStr(r.M.BorderColor)
}

func (r *AreaTypeResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

// ---------------------------------
// Area type search results resolver
// ---------------------------------

type AreaTypeSearchResultsResolver struct {
	M model.AreaTypeSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *AreaTypeSearchResultsResolver) Results() []*AreaTypeResolver {
	resolvers := make([]*AreaTypeResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&AreaTypeResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *AreaTypeSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// -------------
// Area resolver
// -------------

type AreaResolver struct {
	M model.Area
	S *SchemaResolver
	C context.Context
}

func (r *AreaResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *AreaResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *AreaResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *AreaResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *AreaResolver) Token() string {
	return r.M.Token
}

func (r *AreaResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *AreaResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *AreaResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *AreaResolver) AreaType() *AreaTypeResolver {
	if r.M.AreaType != nil {
		return &AreaTypeResolver{
			M: *r.M.AreaType,
			S: r.S,
			C: r.C,
		}
	} else {
		ids := []string{fmt.Sprintf("%d", r.M.AreaTypeId)}
		matches, err := r.S.AreaTypesById(r.C, struct{ Ids []string }{Ids: ids})
		if err != nil {
			return nil
		}
		if len(matches) == 0 {
			return nil
		}
		return matches[0]
	}
}

// ----------------------------
// Area search results resolver
// ----------------------------

type AreaSearchResultsResolver struct {
	M model.AreaSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *AreaSearchResultsResolver) Results() []*AreaResolver {
	resolvers := make([]*AreaResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&AreaResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *AreaSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// -------------------
// Area group resolver
// -------------------

type AreaGroupResolver struct {
	M model.AreaGroup
	S *SchemaResolver
	C context.Context
}

func (r *AreaGroupResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *AreaGroupResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *AreaGroupResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *AreaGroupResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *AreaGroupResolver) Token() string {
	return r.M.Token
}

func (r *AreaGroupResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *AreaGroupResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *AreaGroupResolver) ImageUrl() *string {
	return util.NullStr(r.M.ImageUrl)
}

func (r *AreaGroupResolver) Icon() *string {
	return util.NullStr(r.M.Icon)
}

func (r *AreaGroupResolver) BackgroundColor() *string {
	return util.NullStr(r.M.BackgroundColor)
}

func (r *AreaGroupResolver) ForegroundColor() *string {
	return util.NullStr(r.M.ForegroundColor)
}

func (r *AreaGroupResolver) BorderColor() *string {
	return util.NullStr(r.M.BorderColor)
}

func (r *AreaGroupResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

// ----------------------------------
// Area group search results resolver
// ----------------------------------

type AreaGroupSearchResultsResolver struct {
	M model.AreaGroupSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *AreaGroupSearchResultsResolver) Results() []*AreaGroupResolver {
	resolvers := make([]*AreaGroupResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&AreaGroupResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *AreaGroupSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}
