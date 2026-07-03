// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-dashboard-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	gql "github.com/graph-gophers/graphql-go"
)

// DashboardResolver resolves the fields of a single dashboard.
type DashboardResolver struct {
	M model.Dashboard
	S *SchemaResolver
	C context.Context
}

func (r *DashboardResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DashboardResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DashboardResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *DashboardResolver) Token() string {
	return r.M.Token
}

func (r *DashboardResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *DashboardResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *DashboardResolver) Definition() string {
	return string(r.M.Definition)
}

// DashboardVersionResolver resolves the fields of a published dashboard version.
type DashboardVersionResolver struct {
	M model.DashboardVersion
	S *SchemaResolver
	C context.Context
}

func (r *DashboardVersionResolver) Version() int32 {
	return r.M.Version
}

func (r *DashboardVersionResolver) Label() *string {
	return util.NullStr(r.M.Label)
}

func (r *DashboardVersionResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *DashboardVersionResolver) PublishedAt() string {
	// publishedAt is non-null in the schema and CreatedAt is always set on a
	// persisted version, so a nil format (zero time) collapses to empty rather
	// than a resolver panic.
	if s := util.FormatTime(r.M.CreatedAt); s != nil {
		return *s
	}
	return ""
}

func (r *DashboardVersionResolver) PublishedBy() *string {
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

func (r *SearchResultsPaginationResolver) PageStart() *int32 {
	return &r.M.PageStart
}

func (r *SearchResultsPaginationResolver) PageEnd() *int32 {
	return &r.M.PageEnd
}

func (r *SearchResultsPaginationResolver) TotalRecords() *int32 {
	return &r.M.TotalRecords
}

// DashboardSearchResultsResolver resolves a page of dashboards.
type DashboardSearchResultsResolver struct {
	M model.DashboardSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DashboardSearchResultsResolver) Results() []*DashboardResolver {
	resolvers := make([]*DashboardResolver, 0, len(r.M.Results))
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &DashboardResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *DashboardSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}
