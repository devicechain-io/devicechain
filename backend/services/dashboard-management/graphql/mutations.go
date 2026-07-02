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

// UpdateDashboard updates the dashboard with the given (current) token.
func (r *SchemaResolver) UpdateDashboard(ctx context.Context, args struct {
	Token   string
	Request *model.DashboardCreateRequest
}) (*DashboardResolver, error) {
	if err := auth.Authorize(ctx, auth.DashboardWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateDashboard(ctx, args.Token, args.Request)
	if err != nil {
		return nil, err
	}
	return &DashboardResolver{M: *updated, S: r, C: ctx}, nil
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
