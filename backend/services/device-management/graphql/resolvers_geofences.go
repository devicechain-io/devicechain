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

// ---------------------------
// Geofence resolver
// ---------------------------

type GeoFenceResolver struct {
	M model.GeoFence
	S *SchemaResolver
	C context.Context
}

func (r *GeoFenceResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *GeoFenceResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *GeoFenceResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *GeoFenceResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *GeoFenceResolver) Token() string { return r.M.Token }

func (r *GeoFenceResolver) Name() *string { return util.NullStr(r.M.Name) }

func (r *GeoFenceResolver) Description() *string { return util.NullStr(r.M.Description) }

func (r *GeoFenceResolver) Metadata() *string { return util.MetadataStr(r.M.Metadata) }

// Geometry is the self-describing geometry envelope, returned as its raw JSON text —
// the same document that was authored. It is opaque to this service; nothing joins
// against it.
func (r *GeoFenceResolver) Geometry() string { return string(r.M.Geometry) }

// Kind is the geometry kind discriminator, derived from the stored document rather than
// from a column of its own. Deriving it here is what keeps a client's kind and the
// geometry it renders from ever disagreeing — and it is why a future kind needs no
// schema change on this type.
func (r *GeoFenceResolver) Kind() string { return r.M.Kind() }

// --------------------------------------
// Geofence search results resolver
// --------------------------------------

type GeoFenceSearchResultsResolver struct {
	M model.GeoFenceSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *GeoFenceSearchResultsResolver) Results() []*GeoFenceResolver {
	resolvers := make([]*GeoFenceResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &GeoFenceResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *GeoFenceSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}
