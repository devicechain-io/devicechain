// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-outbound-connectors/model"
	gql "github.com/graph-gophers/graphql-go"
)

// ConnectorResolver resolves the fields of a single connector.
type ConnectorResolver struct {
	M model.Connector
	S *SchemaResolver
	C context.Context
}

func (r *ConnectorResolver) Id() gql.ID { return gql.ID(fmt.Sprint(r.M.ID)) }

func (r *ConnectorResolver) CreatedAt() *string { return util.FormatTime(r.M.CreatedAt) }

func (r *ConnectorResolver) UpdatedAt() *string { return util.FormatTime(r.M.UpdatedAt) }

func (r *ConnectorResolver) Token() string { return r.M.Token }

func (r *ConnectorResolver) Name() *string { return util.NullStr(r.M.Name) }

func (r *ConnectorResolver) Description() *string { return util.NullStr(r.M.Description) }

func (r *ConnectorResolver) Type() string { return r.M.Type }

func (r *ConnectorResolver) Config() string { return string(r.M.Config) }

// HasSecret reports whether an outbound credential is configured, without exposing it.
// The secret is write-only (accepted on create/update, never returned) and lives in the
// envelope-encrypted secret store (ADR-059), so this is a store existence check rather
// than a column read — a connector:read holder learns only the boolean.
func (r *ConnectorResolver) HasSecret() (bool, error) {
	ref, err := model.ConnectorSecretRef(r.C, r.M.ID)
	if err != nil {
		return false, err
	}
	return r.S.GetApi(r.C).Secrets.Exists(r.C, ref)
}

// ConnectorVersionResolver resolves the fields of a published connector version.
type ConnectorVersionResolver struct {
	M model.ConnectorVersion
	S *SchemaResolver
	C context.Context
}

func (r *ConnectorVersionResolver) Version() int32 { return r.M.Version }

func (r *ConnectorVersionResolver) Type() string { return r.M.Type }

func (r *ConnectorVersionResolver) Label() *string { return util.NullStr(r.M.Label) }

func (r *ConnectorVersionResolver) Description() *string { return util.NullStr(r.M.Description) }

func (r *ConnectorVersionResolver) PublishedAt() string {
	// publishedAt is non-null in the schema and CreatedAt is always set on a persisted
	// version, so a nil format (zero time) collapses to empty rather than a panic.
	if s := util.FormatTime(r.M.CreatedAt); s != nil {
		return *s
	}
	return ""
}

func (r *ConnectorVersionResolver) PublishedBy() *string {
	if r.M.PublishedBy == "" {
		return nil
	}
	return &r.M.PublishedBy
}

// SearchResultsPaginationResolver resolves pagination info on a result page.
type SearchResultsPaginationResolver struct {
	M rdb.SearchResultsPagination
	S *SchemaResolver
	C context.Context
}

func (r *SearchResultsPaginationResolver) PageStart() *int32 { return &r.M.PageStart }

func (r *SearchResultsPaginationResolver) PageEnd() *int32 { return &r.M.PageEnd }

func (r *SearchResultsPaginationResolver) TotalRecords() *int32 { return &r.M.TotalRecords }

// ConnectorSearchResultsResolver resolves a page of connectors.
type ConnectorSearchResultsResolver struct {
	M model.ConnectorSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *ConnectorSearchResultsResolver) Results() []*ConnectorResolver {
	resolvers := make([]*ConnectorResolver, 0, len(r.M.Results))
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &ConnectorResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *ConnectorSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}
