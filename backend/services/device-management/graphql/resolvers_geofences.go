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

// ------------------------------------------
// Frozen fence-set snapshot resolvers
// ------------------------------------------

// GeoFenceSetSnapshotResolver resolves one fence-set version's FROZEN fence set (ADR-078).
//
// It resolves a Go-internal document rather than a table, which is why it has no Model
// fields (no id, no timestamps): a snapshot is not an entity that can be addressed, edited
// or deleted — it is the immutable content of a version. Exposing it as its own type
// rather than as a JSON string is what lets a consumer read a fence's token without
// knowing the snapshot's encoding.
type GeoFenceSetSnapshotResolver struct {
	M model.GeoFenceSetSnapshot
	S *SchemaResolver
	C context.Context
}

// Version is the fence-set version this set is the frozen state of. 0 means the tenant had
// never created a fence, whose fence list is empty as knowledge rather than as absence.
func (r *GeoFenceSetSnapshotResolver) Version() int32 { return r.M.Version }

// Fences are the fences as of this version, ordered by token (the mint path orders them,
// so the document is a function of the fence set alone).
func (r *GeoFenceSetSnapshotResolver) Fences() []*GeoFenceSnapshotEntryResolver {
	resolvers := make([]*GeoFenceSnapshotEntryResolver, 0, len(r.M.Fences))
	for _, current := range r.M.Fences {
		resolvers = append(resolvers, &GeoFenceSnapshotEntryResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

// GeoFenceSnapshotEntryResolver resolves one fence inside a frozen snapshot: the token a
// rule names it by and the geometry containment is evaluated against. Name and description
// are absent because the snapshot does not freeze them — they are presentation and do not
// change what a rule computes.
type GeoFenceSnapshotEntryResolver struct {
	M model.GeoFenceSnapshotRef
	S *SchemaResolver
	C context.Context
}

// Token is the stable per-tenant token a rule names this fence by.
func (r *GeoFenceSnapshotEntryResolver) Token() string { return r.M.Token }

// Geometry is the frozen geometry envelope as its raw JSON text — the document as it stood
// at this version, NOT the fence's current geometry. That distinction is the entire reason
// the snapshot exists: re-reading the live fence would answer about the fences that are
// live now, which is what the version stamp was introduced to stop doing.
func (r *GeoFenceSnapshotEntryResolver) Geometry() string { return string(r.M.Geometry) }

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
