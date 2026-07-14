// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// -------------------
// Facet key resolver
// -------------------

// FacetKeyResolver resolves a FacetKey — a per-tenant declaration that an
// EntityAttribute key, for a member family, is a classification facet (ADR-061 G2).
type FacetKeyResolver struct {
	M model.FacetKey
	S *SchemaResolver
	C context.Context
}

func (r *FacetKeyResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *FacetKeyResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *FacetKeyResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *FacetKeyResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *FacetKeyResolver) MemberType() string {
	return r.M.MemberType
}

func (r *FacetKeyResolver) Key() string {
	return r.M.Key
}

func (r *FacetKeyResolver) ValueType() string {
	return r.M.ValueType
}

func (r *FacetKeyResolver) Source() string {
	return r.M.Source
}

// Values decodes the optional value vocabulary (stored as a JSON string array).
// A nil column or an unparseable blob resolves to null rather than erroring — the
// vocabulary is a UX hint, not load-bearing.
func (r *FacetKeyResolver) Values() *[]string {
	if r.M.Values == nil {
		return nil
	}
	var values []string
	if err := json.Unmarshal(*r.M.Values, &values); err != nil {
		return nil
	}
	return &values
}

func (r *FacetKeyResolver) Label() *string {
	return util.NullStr(r.M.Label)
}

// ---------------------------------
// Facet key search results resolver
// ---------------------------------

type FacetKeySearchResultsResolver struct {
	M model.FacetKeySearchResults
	S *SchemaResolver
	C context.Context
}

func (r *FacetKeySearchResultsResolver) Results() []*FacetKeyResolver {
	resolvers := make([]*FacetKeyResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &FacetKeyResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *FacetKeySearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}
