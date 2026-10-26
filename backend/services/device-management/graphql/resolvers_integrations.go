// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"math"

	"github.com/devicechain-io/dc-device-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// -------------------------------------------------
// Integration with its freshly issued bearer token
// -------------------------------------------------

// IntegrationSecretResultResolver answers the two writes that mint a bearer token. The token
// rides on the model row's transient IssuedToken and is read by nothing else.
type IntegrationSecretResultResolver struct {
	M model.Integration
	S *SchemaResolver
	C context.Context
}

func (r *IntegrationSecretResultResolver) Integration() *IntegrationResolver {
	return &IntegrationResolver{M: r.M, S: r.S, C: r.C}
}

func (r *IntegrationSecretResultResolver) IssuedToken() string {
	return r.M.IssuedToken
}

// --------------------------
// Integration resolver
// --------------------------

type IntegrationResolver struct {
	M model.Integration
	S *SchemaResolver
	C context.Context
}

func (r *IntegrationResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *IntegrationResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *IntegrationResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *IntegrationResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *IntegrationResolver) Token() string {
	return r.M.Token
}

func (r *IntegrationResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *IntegrationResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *IntegrationResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *IntegrationResolver) Type() string {
	return r.M.Type
}

func (r *IntegrationResolver) Enabled() bool {
	return r.M.Enabled
}

func (r *IntegrationResolver) Identity() string {
	return r.M.Identity
}

func (r *IntegrationResolver) Generic() *string {
	return util.MetadataStr(r.M.Generic)
}

func (r *IntegrationResolver) LinkMetrics() bool {
	return r.M.LinkMetrics
}

// The bearer token is deliberately not exposed: the schema's Integration type omits it, and
// only its digest is stored. It is returned once, by IntegrationSecretResultResolver.

func (r *IntegrationResolver) DeviceTypes() []*DeviceTypeResolver {
	out := make([]*DeviceTypeResolver, 0, len(r.M.DeviceTypes))
	for _, dt := range r.M.DeviceTypes {
		out = append(out, &DeviceTypeResolver{M: *dt, S: r.S, C: r.C})
	}
	return out
}

func (r *IntegrationResolver) TokenIssuedAt() string {
	return *util.FormatTime(r.M.TokenIssuedAt)
}

// UndecodedCount saturates at the largest GraphQL Int rather than wrapping.
func (r *IntegrationResolver) UndecodedCount() int32 {
	if r.M.UndecodedCount > math.MaxInt32 {
		return math.MaxInt32
	}
	if r.M.UndecodedCount < 0 {
		return 0
	}
	return int32(r.M.UndecodedCount)
}

func (r *IntegrationResolver) LastUndecodedAt() *string {
	if r.M.LastUndecodedAt.Valid {
		return util.FormatTime(r.M.LastUndecodedAt.Time)
	}
	return nil
}

func (r *IntegrationResolver) LastUndecodedSample() *string {
	return util.NullStr(r.M.LastUndecodedSample)
}

func (r *IntegrationResolver) LastAuthFailureAt() *string {
	if r.M.LastAuthFailureAt.Valid {
		return util.FormatTime(r.M.LastAuthFailureAt.Time)
	}
	return nil
}

// -------------------------------------------
// Integration search results resolver
// -------------------------------------------

type IntegrationSearchResultsResolver struct {
	M model.IntegrationSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *IntegrationSearchResultsResolver) Results() []*IntegrationResolver {
	resolvers := make([]*IntegrationResolver, 0, len(r.M.Results))
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &IntegrationResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *IntegrationSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}
