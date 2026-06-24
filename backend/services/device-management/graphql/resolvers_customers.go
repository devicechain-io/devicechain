/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

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

// -----------------------------------
// Customer relationship type resolver
// -----------------------------------

type CustomerRelationshipTypeResolver struct {
	M model.CustomerRelationshipType
	S *SchemaResolver
	C context.Context
}

func (r *CustomerRelationshipTypeResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *CustomerRelationshipTypeResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *CustomerRelationshipTypeResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *CustomerRelationshipTypeResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *CustomerRelationshipTypeResolver) Token() string {
	return r.M.Token
}

func (r *CustomerRelationshipTypeResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *CustomerRelationshipTypeResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *CustomerRelationshipTypeResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

// --------------------------------------------------
// Customer relationship type search results resolver
// --------------------------------------------------

type CustomerRelationshipTypeSearchResultsResolver struct {
	M model.CustomerRelationshipTypeSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *CustomerRelationshipTypeSearchResultsResolver) Results() []*CustomerRelationshipTypeResolver {
	resolvers := make([]*CustomerRelationshipTypeResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&CustomerRelationshipTypeResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *CustomerRelationshipTypeSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// ------------------------------
// Customer relationship resolver
// ------------------------------

type CustomerRelationshipResolver struct {
	M model.CustomerRelationship
	S *SchemaResolver
	C context.Context
}

func (r *CustomerRelationshipResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *CustomerRelationshipResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *CustomerRelationshipResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *CustomerRelationshipResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *CustomerRelationshipResolver) Token() string {
	return r.M.Token
}

func (r *CustomerRelationshipResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *CustomerRelationshipResolver) SourceCustomer() *CustomerResolver {
	return &CustomerResolver{
		M: r.M.SourceCustomer,
		S: r.S,
		C: r.C,
	}
}

func (r *CustomerRelationshipResolver) Targets() *EntityRelationshipResolver {
	return &EntityRelationshipResolver{
		M: r.M.EntityRelationship,
		S: r.S,
		C: r.C,
	}
}

func (r *CustomerRelationshipResolver) RelationshipType() *CustomerRelationshipTypeResolver {
	return &CustomerRelationshipTypeResolver{
		M: r.M.RelationshipType,
		S: r.S,
		C: r.C,
	}
}

// ---------------------------------------------
// Customer relationship search results resolver
// ---------------------------------------------

type CustomerRelationshipSearchResultsResolver struct {
	M model.CustomerRelationshipSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *CustomerRelationshipSearchResultsResolver) Results() []*CustomerRelationshipResolver {
	resolvers := make([]*CustomerRelationshipResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&CustomerRelationshipResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *CustomerRelationshipSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// -----------------------
// Customer group resolver
// -----------------------

type CustomerGroupResolver struct {
	M model.CustomerGroup
	S *SchemaResolver
	C context.Context
}

func (r *CustomerGroupResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *CustomerGroupResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *CustomerGroupResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *CustomerGroupResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *CustomerGroupResolver) Token() string {
	return r.M.Token
}

func (r *CustomerGroupResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *CustomerGroupResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *CustomerGroupResolver) ImageUrl() *string {
	return util.NullStr(r.M.ImageUrl)
}

func (r *CustomerGroupResolver) Icon() *string {
	return util.NullStr(r.M.Icon)
}

func (r *CustomerGroupResolver) BackgroundColor() *string {
	return util.NullStr(r.M.BackgroundColor)
}

func (r *CustomerGroupResolver) ForegroundColor() *string {
	return util.NullStr(r.M.ForegroundColor)
}

func (r *CustomerGroupResolver) BorderColor() *string {
	return util.NullStr(r.M.BorderColor)
}

func (r *CustomerGroupResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

// --------------------------------------
// Customer group search results resolver
// --------------------------------------

type CustomerGroupSearchResultsResolver struct {
	M model.CustomerGroupSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *CustomerGroupSearchResultsResolver) Results() []*CustomerGroupResolver {
	resolvers := make([]*CustomerGroupResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&CustomerGroupResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *CustomerGroupSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// -----------------------------------------
// Customer group relationship type resolver
// -----------------------------------------

type CustomerGroupRelationshipTypeResolver struct {
	M model.CustomerGroupRelationshipType
	S *SchemaResolver
	C context.Context
}

func (r *CustomerGroupRelationshipTypeResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *CustomerGroupRelationshipTypeResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *CustomerGroupRelationshipTypeResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *CustomerGroupRelationshipTypeResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *CustomerGroupRelationshipTypeResolver) Token() string {
	return r.M.Token
}

func (r *CustomerGroupRelationshipTypeResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *CustomerGroupRelationshipTypeResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *CustomerGroupRelationshipTypeResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

// -------------------------------------------------
// Customer group relationship type results resolver
// -------------------------------------------------

type CustomerGroupRelationshipTypeSearchResultsResolver struct {
	M model.CustomerGroupRelationshipTypeSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *CustomerGroupRelationshipTypeSearchResultsResolver) Results() []*CustomerGroupRelationshipTypeResolver {
	resolvers := make([]*CustomerGroupRelationshipTypeResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&CustomerGroupRelationshipTypeResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *CustomerGroupRelationshipTypeSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// ------------------------------------
// Customer group relationship resolver
// ------------------------------------

type CustomerGroupRelationshipResolver struct {
	M model.CustomerGroupRelationship
	S *SchemaResolver
	C context.Context
}

func (r *CustomerGroupRelationshipResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *CustomerGroupRelationshipResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *CustomerGroupRelationshipResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *CustomerGroupRelationshipResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *CustomerGroupRelationshipResolver) Token() string {
	return r.M.Token
}

func (r *CustomerGroupRelationshipResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *CustomerGroupRelationshipResolver) SourceCustomerGroup() *CustomerGroupResolver {
	return &CustomerGroupResolver{
		M: r.M.SourceCustomerGroup,
		S: r.S,
		C: r.C,
	}
}

func (r *CustomerGroupRelationshipResolver) Targets() *EntityRelationshipResolver {
	return &EntityRelationshipResolver{
		M: r.M.EntityRelationship,
		S: r.S,
		C: r.C,
	}
}

func (r *CustomerGroupRelationshipResolver) RelationshipType() *CustomerGroupRelationshipTypeResolver {
	return &CustomerGroupRelationshipTypeResolver{
		M: r.M.RelationshipType,
		S: r.S,
		C: r.C,
	}
}

// --------------------------------------------
// Customer group relationship results resolver
// --------------------------------------------

type CustomerGroupRelationshipSearchResultsResolver struct {
	M model.CustomerGroupRelationshipSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *CustomerGroupRelationshipSearchResultsResolver) Results() []*CustomerGroupRelationshipResolver {
	resolvers := make([]*CustomerGroupRelationshipResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&CustomerGroupRelationshipResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *CustomerGroupRelationshipSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}
