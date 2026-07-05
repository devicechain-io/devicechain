// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// -----------------------
// Device profile resolver
// -----------------------

type DeviceProfileResolver struct {
	M model.DeviceProfile
	S *SchemaResolver
	C context.Context
}

func (r *DeviceProfileResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DeviceProfileResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DeviceProfileResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *DeviceProfileResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *DeviceProfileResolver) Token() string {
	return r.M.Token
}

func (r *DeviceProfileResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *DeviceProfileResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *DeviceProfileResolver) Category() *string {
	return util.NullStr(r.M.Category)
}

func (r *DeviceProfileResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

// --------------------------------------
// Device profile search results resolver
// --------------------------------------

type DeviceProfileSearchResultsResolver struct {
	M model.DeviceProfileSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceProfileSearchResultsResolver) Results() []*DeviceProfileResolver {
	resolvers := make([]*DeviceProfileResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &DeviceProfileResolver{
			M: current,
			S: r.S,
			C: r.C,
		})
	}
	return resolvers
}

func (r *DeviceProfileSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}
