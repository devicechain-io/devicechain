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

// --------------------
// Device type resolver
// --------------------

type DeviceTypeResolver struct {
	M model.DeviceType
	S *SchemaResolver
	C context.Context
}

func (r *DeviceTypeResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DeviceTypeResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DeviceTypeResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *DeviceTypeResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *DeviceTypeResolver) Token() string {
	return r.M.Token
}

func (r *DeviceTypeResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *DeviceTypeResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *DeviceTypeResolver) ImageUrl() *string {
	return util.NullStr(r.M.ImageUrl)
}

func (r *DeviceTypeResolver) Icon() *string {
	return util.NullStr(r.M.Icon)
}

func (r *DeviceTypeResolver) BackgroundColor() *string {
	return util.NullStr(r.M.BackgroundColor)
}

func (r *DeviceTypeResolver) ForegroundColor() *string {
	return util.NullStr(r.M.ForegroundColor)
}

func (r *DeviceTypeResolver) BorderColor() *string {
	return util.NullStr(r.M.BorderColor)
}

func (r *DeviceTypeResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

// -----------------------------------
// Device type search results resolver
// -----------------------------------

type DeviceTypeSearchResultsResolver struct {
	M model.DeviceTypeSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceTypeSearchResultsResolver) Results() []*DeviceTypeResolver {
	resolvers := make([]*DeviceTypeResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&DeviceTypeResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *DeviceTypeSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// ---------------
// Device resolver
// ---------------

type DeviceResolver struct {
	M model.Device
	S *SchemaResolver
	C context.Context
}

func (r *DeviceResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DeviceResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DeviceResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *DeviceResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *DeviceResolver) Token() string {
	return r.M.Token
}

func (r *DeviceResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *DeviceResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *DeviceResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *DeviceResolver) DeviceType() *DeviceTypeResolver {
	if r.M.DeviceType != nil {
		return &DeviceTypeResolver{
			M: *r.M.DeviceType,
			S: r.S,
			C: r.C,
		}
	} else {
		ids := []string{fmt.Sprintf("%d", r.M.DeviceTypeId)}
		rez, err := r.S.DeviceTypesById(r.C, struct{ Ids []string }{Ids: ids})
		if err != nil {
			return nil
		}
		return rez[0]
	}
}

// ------------------------------
// Device search results resolver
// ------------------------------

type DeviceSearchResultsResolver struct {
	M model.DeviceSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceSearchResultsResolver) Results() []*DeviceResolver {
	resolvers := make([]*DeviceResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&DeviceResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *DeviceSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// ---------------------------------
// Device relationship type resolver
// ---------------------------------

type DeviceRelationshipTypeResolver struct {
	M model.DeviceRelationshipType
	S *SchemaResolver
	C context.Context
}

func (r *DeviceRelationshipTypeResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DeviceRelationshipTypeResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DeviceRelationshipTypeResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *DeviceRelationshipTypeResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *DeviceRelationshipTypeResolver) Token() string {
	return r.M.Token
}

func (r *DeviceRelationshipTypeResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *DeviceRelationshipTypeResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *DeviceRelationshipTypeResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *DeviceRelationshipTypeResolver) Tracked() bool {
	return r.M.Tracked
}

// ------------------------------------------------
// Device relationship type search results resolver
// ------------------------------------------------

type DeviceRelationshipTypeSearchResultsResolver struct {
	M model.DeviceRelationshipTypeSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceRelationshipTypeSearchResultsResolver) Results() []*DeviceRelationshipTypeResolver {
	resolvers := make([]*DeviceRelationshipTypeResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&DeviceRelationshipTypeResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *DeviceRelationshipTypeSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// ----------------------------
// Device relationship resolver
// ----------------------------

type DeviceRelationshipResolver struct {
	M model.DeviceRelationship
	S *SchemaResolver
	C context.Context
}

func (r *DeviceRelationshipResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DeviceRelationshipResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DeviceRelationshipResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *DeviceRelationshipResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *DeviceRelationshipResolver) Token() string {
	return r.M.Token
}

func (r *DeviceRelationshipResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *DeviceRelationshipResolver) SourceDevice() *DeviceResolver {
	return &DeviceResolver{
		M: r.M.SourceDevice,
		S: r.S,
		C: r.C,
	}
}

func (r *DeviceRelationshipResolver) Targets() *EntityRelationshipResolver {
	return &EntityRelationshipResolver{
		M: r.M.EntityRelationship,
		S: r.S,
		C: r.C,
	}
}

func (r *DeviceRelationshipResolver) RelationshipType() *DeviceRelationshipTypeResolver {
	return &DeviceRelationshipTypeResolver{
		M: r.M.RelationshipType,
		S: r.S,
		C: r.C,
	}
}

// -------------------------------------------
// Device relationship search results resolver
// -------------------------------------------

type DeviceRelationshipSearchResultsResolver struct {
	M model.DeviceRelationshipSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceRelationshipSearchResultsResolver) Results() []*DeviceRelationshipResolver {
	resolvers := make([]*DeviceRelationshipResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&DeviceRelationshipResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *DeviceRelationshipSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// ---------------------
// Device group resolver
// ---------------------

type DeviceGroupResolver struct {
	M model.DeviceGroup
	S *SchemaResolver
	C context.Context
}

func (r *DeviceGroupResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DeviceGroupResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DeviceGroupResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *DeviceGroupResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *DeviceGroupResolver) Token() string {
	return r.M.Token
}

func (r *DeviceGroupResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *DeviceGroupResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *DeviceGroupResolver) ImageUrl() *string {
	return util.NullStr(r.M.ImageUrl)
}

func (r *DeviceGroupResolver) Icon() *string {
	return util.NullStr(r.M.Icon)
}

func (r *DeviceGroupResolver) BackgroundColor() *string {
	return util.NullStr(r.M.BackgroundColor)
}

func (r *DeviceGroupResolver) ForegroundColor() *string {
	return util.NullStr(r.M.ForegroundColor)
}

func (r *DeviceGroupResolver) BorderColor() *string {
	return util.NullStr(r.M.BorderColor)
}

func (r *DeviceGroupResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

// ------------------------------------
// Device group search results resolver
// ------------------------------------

type DeviceGroupSearchResultsResolver struct {
	M model.DeviceGroupSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceGroupSearchResultsResolver) Results() []*DeviceGroupResolver {
	resolvers := make([]*DeviceGroupResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&DeviceGroupResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *DeviceGroupSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// ---------------------------------------
// Device group relationship type resolver
// ---------------------------------------

type DeviceGroupRelationshipTypeResolver struct {
	M model.DeviceGroupRelationshipType
	S *SchemaResolver
	C context.Context
}

func (r *DeviceGroupRelationshipTypeResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DeviceGroupRelationshipTypeResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DeviceGroupRelationshipTypeResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *DeviceGroupRelationshipTypeResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *DeviceGroupRelationshipTypeResolver) Token() string {
	return r.M.Token
}

func (r *DeviceGroupRelationshipTypeResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *DeviceGroupRelationshipTypeResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *DeviceGroupRelationshipTypeResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

// -----------------------------------------------
// Device group relationship type results resolver
// -----------------------------------------------

type DeviceGroupRelationshipTypeSearchResultsResolver struct {
	M model.DeviceGroupRelationshipTypeSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceGroupRelationshipTypeSearchResultsResolver) Results() []*DeviceGroupRelationshipTypeResolver {
	resolvers := make([]*DeviceGroupRelationshipTypeResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&DeviceGroupRelationshipTypeResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *DeviceGroupRelationshipTypeSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// ----------------------------------
// Device group relationship resolver
// ----------------------------------

type DeviceGroupRelationshipResolver struct {
	M model.DeviceGroupRelationship
	S *SchemaResolver
	C context.Context
}

func (r *DeviceGroupRelationshipResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DeviceGroupRelationshipResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DeviceGroupRelationshipResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *DeviceGroupRelationshipResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *DeviceGroupRelationshipResolver) Token() string {
	return r.M.Token
}

func (r *DeviceGroupRelationshipResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *DeviceGroupRelationshipResolver) SourceDeviceGroup() *DeviceGroupResolver {
	return &DeviceGroupResolver{
		M: r.M.SourceDeviceGroup,
		S: r.S,
		C: r.C,
	}
}

func (r *DeviceGroupRelationshipResolver) Targets() *EntityRelationshipResolver {
	return &EntityRelationshipResolver{
		M: r.M.EntityRelationship,
		S: r.S,
		C: r.C,
	}
}

func (r *DeviceGroupRelationshipResolver) RelationshipType() *DeviceGroupRelationshipTypeResolver {
	return &DeviceGroupRelationshipTypeResolver{
		M: r.M.RelationshipType,
		S: r.S,
		C: r.C,
	}
}

// ------------------------------------------
// Device group relationship results resolver
// ------------------------------------------

type DeviceGroupRelationshipSearchResultsResolver struct {
	M model.DeviceGroupRelationshipSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceGroupRelationshipSearchResultsResolver) Results() []*DeviceGroupRelationshipResolver {
	resolvers := make([]*DeviceGroupRelationshipResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&DeviceGroupRelationshipResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *DeviceGroupRelationshipSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}
