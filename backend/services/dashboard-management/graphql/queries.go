// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"

	"github.com/devicechain-io/dc-dashboard-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"gorm.io/gorm"
)

// PublishedDashboard returns the snapshot viewers are served. Null when the dashboard
// does not exist (as Dashboard); the typed NOT_PUBLISHED error when it exists but was
// never published, so a viewer never gets a plausible blank board.
func (r *SchemaResolver) PublishedDashboard(ctx context.Context, args struct {
	Token string
}) (*PublishedDashboardResolver, error) {
	if err := auth.Authorize(ctx, auth.DashboardRead); err != nil {
		return nil, err
	}

	dash, version, err := r.GetApi(ctx).PublishedDashboard(ctx, args.Token)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &PublishedDashboardResolver{D: *dash, V: *version}, nil
}

// DashboardVersion reads one version including its definition. Author-only: a published
// version body is a frozen copy of a draft, and the draft is not a viewer's to read.
func (r *SchemaResolver) DashboardVersion(ctx context.Context, args struct {
	Token   string
	Version int32
}) (*DashboardVersionDetailResolver, error) {
	if err := auth.Authorize(ctx, auth.DashboardWrite); err != nil {
		return nil, err
	}

	version, err := r.GetApi(ctx).DashboardVersion(ctx, args.Token, args.Version)
	if err != nil {
		return nil, err
	}
	return &DashboardVersionDetailResolver{M: *version}, nil
}

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

// DashboardVersions lists a dashboard's published versions, newest first.
func (r *SchemaResolver) DashboardVersions(ctx context.Context, args struct {
	Token  string
	Limit  *int32
	Offset *int32
}) ([]*DashboardVersionResolver, error) {
	if err := auth.Authorize(ctx, auth.DashboardRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	versions, err := api.DashboardVersions(ctx, args.Token, args.Limit, args.Offset)
	if err != nil {
		return nil, err
	}
	resolvers := make([]*DashboardVersionResolver, 0, len(versions))
	for _, v := range versions {
		resolvers = append(resolvers, &DashboardVersionResolver{M: *v, S: r, C: ctx})
	}
	return resolvers, nil
}
