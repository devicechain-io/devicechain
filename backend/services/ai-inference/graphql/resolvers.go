// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-ai-inference/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	gql "github.com/graph-gophers/graphql-go"
)

// AIProviderResolver resolves the fields of a single provider.
type AIProviderResolver struct {
	M model.AIProvider
	S *SchemaResolver
	C context.Context
}

func (r *AIProviderResolver) Id() gql.ID { return gql.ID(fmt.Sprint(r.M.ID)) }

func (r *AIProviderResolver) CreatedAt() *string { return util.FormatTime(r.M.CreatedAt) }

func (r *AIProviderResolver) UpdatedAt() *string { return util.FormatTime(r.M.UpdatedAt) }

func (r *AIProviderResolver) Token() string { return r.M.Token }

func (r *AIProviderResolver) Name() *string { return util.NullStr(r.M.Name) }

func (r *AIProviderResolver) Description() *string { return util.NullStr(r.M.Description) }

func (r *AIProviderResolver) Kind() string { return r.M.Kind }

func (r *AIProviderResolver) Endpoint() *string {
	if r.M.Endpoint == "" {
		return nil
	}
	return &r.M.Endpoint
}

func (r *AIProviderResolver) Model() string { return r.M.ModelID }

func (r *AIProviderResolver) Params() *string {
	if len(r.M.Params) == 0 {
		return nil
	}
	s := string(r.M.Params)
	return &s
}

func (r *AIProviderResolver) Enabled() bool { return r.M.Enabled }

func (r *AIProviderResolver) Active() bool { return r.M.Active }

// HasSecret reports whether an API key is configured, without exposing it. The key is
// write-only (accepted on create/update, never returned) and lives in the envelope-
// encrypted secret store (ADR-059), so this is a store existence check rather than a
// column read — an ai:admin holder learns only the boolean.
func (r *AIProviderResolver) HasSecret() (bool, error) {
	return r.S.GetApi(r.C).Secrets.Exists(r.C, model.AIProviderSecretRef(r.M.ID))
}

// InferenceResultResolver resolves a single inference result: the candidate the
// provider produced, the model that answered, and the provider token that served it.
// The candidate is returned verbatim — it is validated by the deterministic compiler
// downstream (event-processing rules.Compile), never trusted here.
type InferenceResultResolver struct {
	candidate string
	model     string
	provider  string
}

func (r *InferenceResultResolver) Candidate() string { return r.candidate }

func (r *InferenceResultResolver) Model() string { return r.model }

func (r *InferenceResultResolver) Provider() string { return r.provider }

// SearchResultsPaginationResolver resolves pagination info on a result page.
type SearchResultsPaginationResolver struct {
	M rdb.SearchResultsPagination
	S *SchemaResolver
	C context.Context
}

func (r *SearchResultsPaginationResolver) PageStart() *int32 { return &r.M.PageStart }

func (r *SearchResultsPaginationResolver) PageEnd() *int32 { return &r.M.PageEnd }

func (r *SearchResultsPaginationResolver) TotalRecords() *int32 { return &r.M.TotalRecords }

// AIProviderSearchResultsResolver resolves a page of providers.
type AIProviderSearchResultsResolver struct {
	M model.AIProviderSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *AIProviderSearchResultsResolver) Results() []*AIProviderResolver {
	resolvers := make([]*AIProviderResolver, 0, len(r.M.Results))
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &AIProviderResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *AIProviderSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}
