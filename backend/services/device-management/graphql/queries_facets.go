// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// FacetValues returns the distinct in-use values of a discovery facet (ADR-045
// decision 8) for the authoring UI's suggestion lists. Gated on device:read (facets
// are device-management data) and tenant-scoped at the store.
func (r *SchemaResolver) FacetValues(ctx context.Context, args struct {
	Facet string
}) ([]string, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	return r.GetApi(ctx).FacetValues(ctx, model.FacetKind(args.Facet))
}
