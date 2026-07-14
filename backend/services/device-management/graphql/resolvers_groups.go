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

// ---------------------
// Entity group resolver
// ---------------------

// EntityGroupResolver resolves the uniform EntityGroup (ADR-061) — the group that
// folds the four former per-family groups. It exposes the common branding fields
// plus the member family and membership mode.
type EntityGroupResolver struct {
	M model.EntityGroup
	S *SchemaResolver
	C context.Context
}

func (r *EntityGroupResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *EntityGroupResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *EntityGroupResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *EntityGroupResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *EntityGroupResolver) Token() string {
	return r.M.Token
}

func (r *EntityGroupResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *EntityGroupResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *EntityGroupResolver) ImageUrl() *string {
	return util.NullStr(r.M.ImageUrl)
}

func (r *EntityGroupResolver) Icon() *string {
	return util.NullStr(r.M.Icon)
}

func (r *EntityGroupResolver) BackgroundColor() *string {
	return util.NullStr(r.M.BackgroundColor)
}

func (r *EntityGroupResolver) ForegroundColor() *string {
	return util.NullStr(r.M.ForegroundColor)
}

func (r *EntityGroupResolver) BorderColor() *string {
	return util.NullStr(r.M.BorderColor)
}

func (r *EntityGroupResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *EntityGroupResolver) MemberType() string {
	return r.M.MemberType
}

func (r *EntityGroupResolver) MembershipMode() string {
	return r.M.MembershipMode
}

func (r *EntityGroupResolver) Selector() *string {
	return util.NullStr(r.M.Selector)
}

// ------------------------------------
// Entity group search results resolver
// ------------------------------------

type EntityGroupSearchResultsResolver struct {
	M model.EntityGroupSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *EntityGroupSearchResultsResolver) Results() []*EntityGroupResolver {
	resolvers := make([]*EntityGroupResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&EntityGroupResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *EntityGroupSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}
