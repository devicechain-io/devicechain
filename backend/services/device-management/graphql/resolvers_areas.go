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

// -------------------------------
// Area relationship type resolver
// -------------------------------

type AreaRelationshipTypeResolver struct {
	M model.AreaRelationshipType
	S *SchemaResolver
	C context.Context
}

func (r *AreaRelationshipTypeResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *AreaRelationshipTypeResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *AreaRelationshipTypeResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *AreaRelationshipTypeResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *AreaRelationshipTypeResolver) Token() string {
	return r.M.Token
}

func (r *AreaRelationshipTypeResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *AreaRelationshipTypeResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *AreaRelationshipTypeResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

// ----------------------------------------------
// Area relationship type search results resolver
// ----------------------------------------------

type AreaRelationshipTypeSearchResultsResolver struct {
	M model.AreaRelationshipTypeSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *AreaRelationshipTypeSearchResultsResolver) Results() []*AreaRelationshipTypeResolver {
	resolvers := make([]*AreaRelationshipTypeResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&AreaRelationshipTypeResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *AreaRelationshipTypeSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// --------------------------
// Area relationship resolver
// --------------------------

type AreaRelationshipResolver struct {
	M model.AreaRelationship
	S *SchemaResolver
	C context.Context
}

func (r *AreaRelationshipResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *AreaRelationshipResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *AreaRelationshipResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *AreaRelationshipResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *AreaRelationshipResolver) Token() string {
	return r.M.Token
}

func (r *AreaRelationshipResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *AreaRelationshipResolver) SourceArea() *AreaResolver {
	return &AreaResolver{
		M: r.M.SourceArea,
		S: r.S,
		C: r.C,
	}
}

func (r *AreaRelationshipResolver) Targets() *EntityRelationshipResolver {
	return &EntityRelationshipResolver{
		M: r.M.EntityRelationship,
		S: r.S,
		C: r.C,
	}
}

func (r *AreaRelationshipResolver) RelationshipType() *AreaRelationshipTypeResolver {
	return &AreaRelationshipTypeResolver{
		M: r.M.RelationshipType,
		S: r.S,
		C: r.C,
	}
}

// -----------------------------------------
// Area relationship search results resolver
// -----------------------------------------

type AreaRelationshipSearchResultsResolver struct {
	M model.AreaRelationshipSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *AreaRelationshipSearchResultsResolver) Results() []*AreaRelationshipResolver {
	resolvers := make([]*AreaRelationshipResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&AreaRelationshipResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *AreaRelationshipSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
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

// -------------------------------------
// Area group relationship type resolver
// -------------------------------------

type AreaGroupRelationshipTypeResolver struct {
	M model.AreaGroupRelationshipType
	S *SchemaResolver
	C context.Context
}

func (r *AreaGroupRelationshipTypeResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *AreaGroupRelationshipTypeResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *AreaGroupRelationshipTypeResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *AreaGroupRelationshipTypeResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *AreaGroupRelationshipTypeResolver) Token() string {
	return r.M.Token
}

func (r *AreaGroupRelationshipTypeResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *AreaGroupRelationshipTypeResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *AreaGroupRelationshipTypeResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

// ----------------------------------------------------
// Area group relationship type search results resolver
// ----------------------------------------------------

type AreaGroupRelationshipTypeSearchResultsResolver struct {
	M model.AreaGroupRelationshipTypeSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *AreaGroupRelationshipTypeSearchResultsResolver) Results() []*AreaGroupRelationshipTypeResolver {
	resolvers := make([]*AreaGroupRelationshipTypeResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&AreaGroupRelationshipTypeResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *AreaGroupRelationshipTypeSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// --------------------------------
// Area group relationship resolver
// --------------------------------

type AreaGroupRelationshipResolver struct {
	M model.AreaGroupRelationship
	S *SchemaResolver
	C context.Context
}

func (r *AreaGroupRelationshipResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *AreaGroupRelationshipResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *AreaGroupRelationshipResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *AreaGroupRelationshipResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *AreaGroupRelationshipResolver) Token() string {
	return r.M.Token
}

func (r *AreaGroupRelationshipResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *AreaGroupRelationshipResolver) SourceAreaGroup() *AreaGroupResolver {
	return &AreaGroupResolver{
		M: r.M.SourceAreaGroup,
		S: r.S,
		C: r.C,
	}
}

func (r *AreaGroupRelationshipResolver) Targets() *EntityRelationshipResolver {
	return &EntityRelationshipResolver{
		M: r.M.EntityRelationship,
		S: r.S,
		C: r.C,
	}
}

func (r *AreaGroupRelationshipResolver) RelationshipType() *AreaGroupRelationshipTypeResolver {
	return &AreaGroupRelationshipTypeResolver{
		M: r.M.RelationshipType,
		S: r.S,
		C: r.C,
	}
}

// -----------------------------------------------
// Area group relationship search results resolver
// -----------------------------------------------

type AreaGroupRelationshipSearchResultsResolver struct {
	M model.AreaGroupRelationshipSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *AreaGroupRelationshipSearchResultsResolver) Results() []*AreaGroupRelationshipResolver {
	resolvers := make([]*AreaGroupRelationshipResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&AreaGroupRelationshipResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *AreaGroupRelationshipSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}
