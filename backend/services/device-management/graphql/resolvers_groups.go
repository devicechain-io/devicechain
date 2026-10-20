// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
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

// Description is the operator's free text about the group, which can say anything the metadata
// can. A scoped caller reads the group's token and name only (ADR-081), so it is refused here
// the way metadata and the selector are.
func (r *EntityGroupResolver) Description(ctx context.Context) (*string, error) {
	if err := refuseScopedDefinition(ctx); err != nil {
		return nil, err
	}
	return util.NullStr(r.M.Description), nil
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

// refuseScopedDefinition refuses a scoped caller (ADR-081) the group fields that carry its
// definition. It checks the scope only, not an authority: these fields ride on whatever
// produced the group, including the DeviceWrite-only create and update mutations, so an
// authority check here would change what an unscoped writer gets back.
func refuseScopedDefinition(ctx context.Context) error {
	if _, scoped := auth.ScopeFromContext(ctx); scoped {
		return auth.ErrScopeUnsupported
	}
	return nil
}

// Metadata is the operator's free-form record on the group, which can name anything — other
// customers included. It is refused to a scoped caller, which reads a group by token to learn
// what it is limited to and needs only the token and name (ADR-081).
func (r *EntityGroupResolver) Metadata(ctx context.Context) (*string, error) {
	if err := refuseScopedDefinition(ctx); err != nil {
		return nil, err
	}
	return util.MetadataStr(r.M.Metadata), nil
}

func (r *EntityGroupResolver) MemberType() string {
	return r.M.MemberType
}

// AccessScope reports whether the group is an access scope (ADR-081).
func (r *EntityGroupResolver) AccessScope() bool {
	return r.M.IsAccessScope()
}

func (r *EntityGroupResolver) MembershipMode() string {
	return r.M.MembershipMode
}

// Selector is the group's membership definition: it names attribute keys and values that can
// refer to other customers' devices. A scoped caller reads the group's token and name only, so
// the definition is refused to it the way members and versions are (ADR-081).
func (r *EntityGroupResolver) Selector(ctx context.Context) (*string, error) {
	if err := refuseScopedDefinition(ctx); err != nil {
		return nil, err
	}
	return util.NullStr(r.M.Selector), nil
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
