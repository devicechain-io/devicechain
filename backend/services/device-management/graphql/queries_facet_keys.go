// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// FacetKeys lists the tenant's declared facet keys (ADR-061 G2), optionally for one
// member family. Gated on device:read (facets are device-management classification
// data) and tenant-scoped at the store.
func (r *SchemaResolver) FacetKeys(ctx context.Context, args struct {
	Criteria model.FacetKeySearchCriteria
}) (*FacetKeySearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.FacetKeys(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &FacetKeySearchResultsResolver{M: *found, S: r, C: ctx}, nil
}
