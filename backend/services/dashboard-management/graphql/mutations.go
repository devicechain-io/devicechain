// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-dashboard-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// CreateDashboard creates a new dashboard.
func (r *SchemaResolver) CreateDashboard(ctx context.Context, args struct {
	Request *model.DashboardCreateRequest
}) (*DashboardResolver, error) {
	if err := auth.Authorize(ctx, auth.DashboardWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateDashboard(ctx, args.Request)
	if err != nil {
		return nil, err
	}
	return &DashboardResolver{M: *created, S: r, C: ctx}, nil
}

// UpdateDashboard updates the dashboard (draft) with the given (current) token.
// expectedUpdatedAt, when supplied, is an optimistic-concurrency precondition.
func (r *SchemaResolver) UpdateDashboard(ctx context.Context, args struct {
	Token             string
	Request           *model.DashboardCreateRequest
	ExpectedUpdatedAt *string
}) (*DashboardResolver, error) {
	if err := auth.Authorize(ctx, auth.DashboardWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateDashboard(ctx, args.Token, args.Request, args.ExpectedUpdatedAt)
	if err != nil {
		return nil, err
	}
	return &DashboardResolver{M: *updated, S: r, C: ctx}, nil
}

// PublishDashboard freezes the current draft into a new immutable version.
func (r *SchemaResolver) PublishDashboard(ctx context.Context, args struct {
	Token       string
	Label       *string
	Description *string
}) (*DashboardVersionResolver, error) {
	if err := auth.Authorize(ctx, auth.DashboardWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	version, err := api.PublishDashboard(ctx, args.Token, args.Label, args.Description, publisher(ctx))
	if err != nil {
		return nil, err
	}
	return &DashboardVersionResolver{M: *version, S: r, C: ctx}, nil
}

// RollbackDashboard re-drafts a published version into the dashboard.
func (r *SchemaResolver) RollbackDashboard(ctx context.Context, args struct {
	Token   string
	Version int32
}) (*DashboardResolver, error) {
	if err := auth.Authorize(ctx, auth.DashboardWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.RollbackDashboard(ctx, args.Token, args.Version)
	if err != nil {
		return nil, err
	}
	return &DashboardResolver{M: *updated, S: r, C: ctx}, nil
}

// publisher is the identity string recorded on a published version: the caller's
// username, falling back to email (identity tokens carry email, tenant tokens a
// username). Empty when unauthenticated (the resolver is already auth-gated).
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

// DeleteDashboard deletes the dashboard with the given token.
func (r *SchemaResolver) DeleteDashboard(ctx context.Context, args struct {
	Token string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.DashboardWrite); err != nil {
		return false, err
	}

	api := r.GetApi(ctx)
	return api.DeleteDashboard(ctx, args.Token)
}
