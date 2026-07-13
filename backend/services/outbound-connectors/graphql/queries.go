// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-outbound-connectors/model"
)

// Connector looks up a single connector by its token. Returns nil when not found.
func (r *SchemaResolver) Connector(ctx context.Context, args struct {
	Token string
}) (*ConnectorResolver, error) {
	if err := auth.Authorize(ctx, auth.ConnectorRead); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	matches, err := api.ConnectorsByToken(ctx, []string{args.Token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return &ConnectorResolver{M: *matches[0], S: r, C: ctx}, nil
}

// Connectors searches connectors by criteria.
func (r *SchemaResolver) Connectors(ctx context.Context, args struct {
	Criteria model.ConnectorSearchCriteria
}) (*ConnectorSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.ConnectorRead); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	found, err := api.Connectors(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &ConnectorSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}

// ConnectorVersions lists a connector's published versions, newest first.
func (r *SchemaResolver) ConnectorVersions(ctx context.Context, args struct {
	Token string
}) ([]*ConnectorVersionResolver, error) {
	if err := auth.Authorize(ctx, auth.ConnectorRead); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	versions, err := api.ConnectorVersions(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	resolvers := make([]*ConnectorVersionResolver, 0, len(versions))
	for _, v := range versions {
		resolvers = append(resolvers, &ConnectorVersionResolver{M: *v, S: r, C: ctx})
	}
	return resolvers, nil
}

// ConnectorTypes returns the registered connector-type vocabulary (for a console
// picker). Read-gated like the rest of the surface.
func (r *SchemaResolver) ConnectorTypes(ctx context.Context) ([]string, error) {
	if err := auth.Authorize(ctx, auth.ConnectorRead); err != nil {
		return nil, err
	}
	return model.ConnectorTypes(), nil
}
