// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-device-management/internal/selector"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	gql "github.com/graph-gophers/graphql-go"
)

// PaginationInput is the Go binding for the GraphQL PaginationInput — the pageNumber/
// pageSize pair a paginated field or query carries. Resolution always clamps to the
// platform ceiling, so a large requested size can never turn into an unbounded scan.
type PaginationInput struct {
	PageNumber int32
	PageSize   int32
}

// rdbPagination maps the GraphQL pagination input to the core pagination request. It never
// sets Unbounded — an external request is always bounded (the model clamps regardless).
func rdbPagination(in PaginationInput) rdb.Pagination {
	return rdb.Pagination{PageNumber: in.PageNumber, PageSize: in.PageSize}
}

// -----------------------------
// Entity group member resolvers
// -----------------------------

// EntityGroupMemberResolver resolves a single resolved group member — the lightweight
// identity (row id + token) of a family entity produced by ResolveGroupMembers (ADR-061 G4).
type EntityGroupMemberResolver struct {
	M model.EntityMember
	S *SchemaResolver
	C context.Context
}

func (r *EntityGroupMemberResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.Id))
}

func (r *EntityGroupMemberResolver) Token() string {
	return r.M.Token
}

// EntityGroupMemberSearchResultsResolver resolves a paginated page of resolved members.
type EntityGroupMemberSearchResultsResolver struct {
	M model.EntityMemberSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *EntityGroupMemberSearchResultsResolver) Results() []*EntityGroupMemberResolver {
	resolvers := make([]*EntityGroupMemberResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &EntityGroupMemberResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *EntityGroupMemberSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}

// Members resolves this group's members transparently over either mode — a static group's
// "member" edges or a dynamic group's eval-on-read selector matches. The page is always
// bounded (the model forces Unbounded off and applies the platform page-size clamp).
func (r *EntityGroupResolver) Members(ctx context.Context, args struct {
	Pagination PaginationInput
}) (*EntityGroupMemberSearchResultsResolver, error) {
	// Gate the nested field on its own: an EntityGroup can be produced by the
	// DeviceWrite-only create/update mutations, so reading members through it must
	// still require DeviceRead (authorities are flat — write does not imply read).
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	api := r.S.GetApi(ctx)
	found, err := api.ResolveGroupMembers(ctx, &r.M, rdbPagination(args.Pagination))
	if err != nil {
		return nil, err
	}
	return &EntityGroupMemberSearchResultsResolver{M: *found, S: r.S, C: ctx}, nil
}

// -------------------------
// Selector preview resolver
// -------------------------

// SelectorPreviewResolver resolves the result of evaluating a candidate selector without
// saving it (ADR-061 G4). A publish-gate rejection is carried as valid=false + a
// console-surfaceable error rather than a hard GraphQL error, so the authoring UI shows the
// problem inline; a valid selector carries the eval-on-read matches.
type SelectorPreviewResolver struct {
	IsValid bool
	ErrMsg  *string
	Matches *model.EntityMemberSearchResults
	S       *SchemaResolver
	C       context.Context
}

func (r *SelectorPreviewResolver) Valid() bool {
	return r.IsValid
}

func (r *SelectorPreviewResolver) Error() *string {
	return r.ErrMsg
}

func (r *SelectorPreviewResolver) Members() *EntityGroupMemberSearchResultsResolver {
	if r.Matches == nil {
		return nil
	}
	return &EntityGroupMemberSearchResultsResolver{M: *r.Matches, S: r.S, C: r.C}
}

// isSelectorAuthorError reports whether an error from the selector engine is an author-facing
// publish-gate rejection (surfaceable inline) rather than a server/infra fault. These are the
// three typed errors the compile + lowering path returns for a bad candidate selector.
func isSelectorAuthorError(err error) bool {
	var compile *selector.CompileError
	var cost *selector.CostError
	var notLowerable *selector.NotLowerableError
	return errors.As(err, &compile) || errors.As(err, &cost) || errors.As(err, &notLowerable)
}
