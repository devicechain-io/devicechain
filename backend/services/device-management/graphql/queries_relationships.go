// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find entity relationship types by unique id.
func (r *SchemaResolver) EntityRelationshipTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*EntityRelationshipTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.EntityRelationshipTypesById(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]*EntityRelationshipTypeResolver, 0)
	for _, rec := range found {
		result = append(result, &EntityRelationshipTypeResolver{M: *rec, S: r, C: ctx})
	}
	return result, nil
}

// Find entity relationship types by unique token.
func (r *SchemaResolver) EntityRelationshipTypesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*EntityRelationshipTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.EntityRelationshipTypesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}
	result := make([]*EntityRelationshipTypeResolver, 0)
	for _, rec := range found {
		result = append(result, &EntityRelationshipTypeResolver{M: *rec, S: r, C: ctx})
	}
	return result, nil
}

// List all entity relationship types that match the given criteria.
func (r *SchemaResolver) EntityRelationshipTypes(ctx context.Context, args struct {
	Criteria model.EntityRelationshipTypeSearchCriteria
}) (*EntityRelationshipTypeSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.EntityRelationshipTypes(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &EntityRelationshipTypeSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}

// Find entity relationships by unique id.
func (r *SchemaResolver) EntityRelationshipsById(ctx context.Context, args struct {
	Ids []string
}) ([]*EntityRelationshipResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.EntityRelationshipsById(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]*EntityRelationshipResolver, 0)
	for _, rec := range found {
		result = append(result, &EntityRelationshipResolver{M: *rec, S: r, C: ctx})
	}
	return result, nil
}

// Find entity relationships by unique token.
func (r *SchemaResolver) EntityRelationshipsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*EntityRelationshipResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.EntityRelationshipsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}
	result := make([]*EntityRelationshipResolver, 0)
	for _, rec := range found {
		result = append(result, &EntityRelationshipResolver{M: *rec, S: r, C: ctx})
	}
	return result, nil
}

// List all entity relationships that match the given criteria. A source filter is
// supplied as (sourceType, source-token); it is resolved to the internal id here.
func (r *SchemaResolver) EntityRelationships(ctx context.Context, args struct {
	Criteria model.EntityRelationshipSearchCriteria
}) (*EntityRelationshipSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	criteria := args.Criteria
	if criteria.Source != nil && criteria.SourceType != nil {
		id, err := api.ResolveEntityToken(ctx, *criteria.SourceType, *criteria.Source)
		if err != nil {
			return nil, err
		}
		criteria.SourceId = &id
	}
	found, err := api.EntityRelationships(ctx, criteria)
	if err != nil {
		return nil, err
	}
	return &EntityRelationshipSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}
