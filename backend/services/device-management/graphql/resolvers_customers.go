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

// ----------------------
// Customer type resolver
// ----------------------

type CustomerTypeResolver struct {
	M model.CustomerType
	S *SchemaResolver
	C context.Context
}

func (r *CustomerTypeResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *CustomerTypeResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *CustomerTypeResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *CustomerTypeResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *CustomerTypeResolver) Token() string {
	return r.M.Token
}

func (r *CustomerTypeResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *CustomerTypeResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *CustomerTypeResolver) ImageUrl() *string {
	return util.NullStr(r.M.ImageUrl)
}

func (r *CustomerTypeResolver) Icon() *string {
	return util.NullStr(r.M.Icon)
}

func (r *CustomerTypeResolver) BackgroundColor() *string {
	return util.NullStr(r.M.BackgroundColor)
}

func (r *CustomerTypeResolver) ForegroundColor() *string {
	return util.NullStr(r.M.ForegroundColor)
}

func (r *CustomerTypeResolver) BorderColor() *string {
	return util.NullStr(r.M.BorderColor)
}

func (r *CustomerTypeResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

// -------------------------------------
// Customer type search results resolver
// -------------------------------------

type CustomerTypeSearchResultsResolver struct {
	M model.CustomerTypeSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *CustomerTypeSearchResultsResolver) Results() []*CustomerTypeResolver {
	resolvers := make([]*CustomerTypeResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&CustomerTypeResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *CustomerTypeSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// -----------------
// Customer resolver
// -----------------

type CustomerResolver struct {
	M model.Customer
	S *SchemaResolver
	C context.Context
}

func (r *CustomerResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *CustomerResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *CustomerResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *CustomerResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *CustomerResolver) Token() string {
	return r.M.Token
}

func (r *CustomerResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *CustomerResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *CustomerResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *CustomerResolver) CustomerType() *CustomerTypeResolver {
	if r.M.CustomerType != nil {
		return &CustomerTypeResolver{
			M: *r.M.CustomerType,
			S: r.S,
			C: r.C,
		}
	} else {
		ids := []string{fmt.Sprintf("%d", r.M.CustomerTypeId)}
		matches, err := r.S.CustomerTypesById(r.C, struct{ Ids []string }{Ids: ids})
		if err != nil {
			return nil
		}
		if len(matches) == 0 {
			return nil
		}
		return matches[0]
	}
}

// --------------------------------
// Customer search results resolver
// --------------------------------

type CustomerSearchResultsResolver struct {
	M model.CustomerSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *CustomerSearchResultsResolver) Results() []*CustomerResolver {
	resolvers := make([]*CustomerResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&CustomerResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *CustomerSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}
