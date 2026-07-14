// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// SetFacetKey declares (upserts) a facet key by its natural key (memberType, key) —
// ADR-061 G2. Validation (member family, value type, system-facet protection) lives
// in the model API. Gated on device:write, consistent with the entity-group and
// discovery-facet surfaces it sits beside.
func (r *SchemaResolver) SetFacetKey(ctx context.Context, args struct {
	Request *model.FacetKeySetRequest
}) (*FacetKeyResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	set, err := api.SetFacetKey(ctx, args.Request)
	if err != nil {
		return nil, err
	}
	return &FacetKeyResolver{M: *set, S: r, C: ctx}, nil
}

// DeleteFacetKey removes a facet key by its natural key. A system facet is
// non-deletable; the model API refuses it.
func (r *SchemaResolver) DeleteFacetKey(ctx context.Context, args struct {
	MemberType string
	Key        string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	return r.GetApi(ctx).DeleteFacetKey(ctx, args.MemberType, args.Key)
}
