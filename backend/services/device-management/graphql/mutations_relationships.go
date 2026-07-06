// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new entity relationship type.
func (r *SchemaResolver) CreateEntityRelationshipType(ctx context.Context, args struct {
	Request model.EntityRelationshipTypeCreateRequest
}) (*EntityRelationshipTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateEntityRelationshipType(ctx, &args.Request)
	if err != nil {
		return nil, err
	}
	return &EntityRelationshipTypeResolver{M: *created, S: r, C: ctx}, nil
}

// Update an existing entity relationship type by token.
func (r *SchemaResolver) UpdateEntityRelationshipType(ctx context.Context, args struct {
	Token   string
	Request model.EntityRelationshipTypeCreateRequest
}) (*EntityRelationshipTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateEntityRelationshipType(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}
	return &EntityRelationshipTypeResolver{M: *updated, S: r, C: ctx}, nil
}

// Create a new entity relationship.
func (r *SchemaResolver) CreateEntityRelationship(ctx context.Context, args struct {
	Request model.EntityRelationshipCreateRequest
}) (*EntityRelationshipResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateEntityRelationship(ctx, &args.Request)
	if err != nil {
		return nil, err
	}
	return &EntityRelationshipResolver{M: *created, S: r, C: ctx}, nil
}

// Create multiple entity relationships in one transaction (bulk "add members" /
// "assign"). Auto-provisions the reserved "member" relationship type on first use.
func (r *SchemaResolver) CreateEntityRelationships(ctx context.Context, args struct {
	Requests []*model.EntityRelationshipCreateRequest
}) ([]*EntityRelationshipResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateEntityRelationships(ctx, args.Requests)
	if err != nil {
		return nil, err
	}
	resolvers := make([]*EntityRelationshipResolver, 0, len(created))
	for _, c := range created {
		resolvers = append(resolvers, &EntityRelationshipResolver{M: *c, S: r, C: ctx})
	}
	return resolvers, nil
}
