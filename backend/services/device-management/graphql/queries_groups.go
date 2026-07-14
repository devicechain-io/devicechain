// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find entity groups by unique id.
func (r *SchemaResolver) EntityGroupsById(ctx context.Context, args struct {
	Ids []string
}) ([]*EntityGroupResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.EntityGroupsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*EntityGroupResolver, 0)
	for _, rec := range found {
		result = append(result, &EntityGroupResolver{M: *rec, S: r, C: ctx})
	}
	return result, nil
}

// Find entity groups by unique token.
func (r *SchemaResolver) EntityGroupsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*EntityGroupResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.EntityGroupsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*EntityGroupResolver, 0)
	for _, rec := range found {
		result = append(result, &EntityGroupResolver{M: *rec, S: r, C: ctx})
	}
	return result, nil
}

// List all entity groups that match the given criteria.
func (r *SchemaResolver) EntityGroups(ctx context.Context, args struct {
	Criteria model.EntityGroupSearchCriteria
}) (*EntityGroupSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.EntityGroups(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	return &EntityGroupSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}

// Evaluate a candidate dynamic-group selector for a member family WITHOUT saving it —
// the console's live "matches N" authoring preview (ADR-061 G4). A publish-gate rejection
// (non-lowerable / over-budget / malformed) is returned as valid=false + an inline error
// rather than a hard GraphQL error, so the authoring UI surfaces it as the user types; a
// non-author fault (no tenant, DB error) is still a real error.
func (r *SchemaResolver) PreviewSelector(ctx context.Context, args struct {
	MemberType string
	Selector   string
	Pagination PaginationInput
}) (*SelectorPreviewResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.PreviewSelector(ctx, args.MemberType, args.Selector, rdbPagination(args.Pagination))
	if err != nil {
		if isSelectorAuthorError(err) {
			msg := err.Error()
			return &SelectorPreviewResolver{IsValid: false, ErrMsg: &msg, S: r, C: ctx}, nil
		}
		return nil, err
	}
	return &SelectorPreviewResolver{IsValid: true, Matches: found, S: r, C: ctx}, nil
}
