// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
)

// List all entity attributes that match the given criteria. An owner filter is
// supplied as (entityType, entity-token); it is resolved to the internal id by
// the API layer.
func (r *SchemaResolver) EntityAttributes(ctx context.Context, args struct {
	Criteria model.EntityAttributeSearchCriteria
}) (*EntityAttributeSearchResultsResolver, error) {
	api := r.GetApi(ctx)
	found, err := api.EntityAttributes(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &EntityAttributeSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}
