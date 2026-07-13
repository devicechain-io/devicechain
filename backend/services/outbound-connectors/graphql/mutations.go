// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-outbound-connectors/model"
)

// CreateConnector creates a new connector (draft).
func (r *SchemaResolver) CreateConnector(ctx context.Context, args struct {
	Request model.ConnectorCreateRequest
}) (*ConnectorResolver, error) {
	if err := auth.Authorize(ctx, auth.ConnectorWrite); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	created, err := api.CreateConnector(ctx, &args.Request)
	if err != nil {
		return nil, err
	}
	return &ConnectorResolver{M: *created, S: r, C: ctx}, nil
}

// UpdateConnector updates the connector (draft) with the given (current) token.
// expectedUpdatedAt, when supplied, is an optimistic-concurrency precondition.
func (r *SchemaResolver) UpdateConnector(ctx context.Context, args struct {
	Token             string
	Request           model.ConnectorCreateRequest
	ExpectedUpdatedAt *string
}) (*ConnectorResolver, error) {
	if err := auth.Authorize(ctx, auth.ConnectorWrite); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	updated, err := api.UpdateConnector(ctx, args.Token, &args.Request, args.ExpectedUpdatedAt)
	if err != nil {
		return nil, err
	}
	return &ConnectorResolver{M: *updated, S: r, C: ctx}, nil
}

// PublishConnector freezes the current draft into a new immutable version.
// expectedUpdatedAt, when supplied, is an optimistic-concurrency precondition.
func (r *SchemaResolver) PublishConnector(ctx context.Context, args struct {
	Token             string
	Label             *string
	Description       *string
	ExpectedUpdatedAt *string
}) (*ConnectorVersionResolver, error) {
	if err := auth.Authorize(ctx, auth.ConnectorWrite); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	version, err := api.PublishConnector(ctx, args.Token, args.Label, args.Description, publisher(ctx), args.ExpectedUpdatedAt)
	if err != nil {
		return nil, err
	}
	return &ConnectorVersionResolver{M: *version, S: r, C: ctx}, nil
}

// RollbackConnector re-drafts a published version into the connector.
func (r *SchemaResolver) RollbackConnector(ctx context.Context, args struct {
	Token   string
	Version int32
}) (*ConnectorResolver, error) {
	if err := auth.Authorize(ctx, auth.ConnectorWrite); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	updated, err := api.RollbackConnector(ctx, args.Token, args.Version)
	if err != nil {
		return nil, err
	}
	return &ConnectorResolver{M: *updated, S: r, C: ctx}, nil
}

// DeleteConnector deletes the connector with the given token.
func (r *SchemaResolver) DeleteConnector(ctx context.Context, args struct {
	Token string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.ConnectorWrite); err != nil {
		return false, err
	}
	api := r.GetApi(ctx)
	return api.DeleteConnector(ctx, args.Token)
}

// publisher is the identity string recorded on a published version: the caller's
// username, falling back to email. Empty when unauthenticated (the resolver is already
// auth-gated).
func publisher(ctx context.Context) string {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return ""
	}
	if claims.Username != "" {
		return claims.Username
	}
	return claims.Email
}
