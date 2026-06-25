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

// -------------------------
// Entity attribute resolver
// -------------------------

type EntityAttributeResolver struct {
	M model.EntityAttribute
	S *SchemaResolver
	C context.Context
}

func (r *EntityAttributeResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *EntityAttributeResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *EntityAttributeResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *EntityAttributeResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *EntityAttributeResolver) EntityType() string {
	return r.M.EntityType
}

func (r *EntityAttributeResolver) Scope() string {
	return r.M.Scope
}

func (r *EntityAttributeResolver) AttrKey() string {
	return r.M.AttrKey
}

func (r *EntityAttributeResolver) ValueType() string {
	return r.M.ValueType
}

func (r *EntityAttributeResolver) Value() *string {
	return util.NullStr(r.M.Value)
}

func (r *EntityAttributeResolver) LastUpdated() *string {
	return util.FormatTime(r.M.LastUpdated)
}

func (r *EntityAttributeResolver) Entity() (*EntityResolver, error) {
	return r.S.resolveEntity(r.C, r.M.EntityType, r.M.EntityId)
}

type EntityAttributeSearchResultsResolver struct {
	M model.EntityAttributeSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *EntityAttributeSearchResultsResolver) Results() []*EntityAttributeResolver {
	resolvers := make([]*EntityAttributeResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &EntityAttributeResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *EntityAttributeSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}
