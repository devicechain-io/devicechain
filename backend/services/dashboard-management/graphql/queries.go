// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-dashboard-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// Dashboard looks up a single dashboard by its token. Returns nil when not found.
func (r *SchemaResolver) Dashboard(ctx context.Context, args struct {
	Token string
}) (*DashboardResolver, error) {
	if err := auth.Authorize(ctx, auth.DashboardRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	matches, err := api.DashboardsByToken(ctx, []string{args.Token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return &DashboardResolver{M: *matches[0], S: r, C: ctx}, nil
}

// Dashboards searches dashboards by criteria.
func (r *SchemaResolver) Dashboards(ctx context.Context, args struct {
	Criteria model.DashboardSearchCriteria
}) (*DashboardSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DashboardRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.Dashboards(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &DashboardSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}
