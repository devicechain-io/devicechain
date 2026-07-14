// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
)

// EntityGroupVersions lists a dynamic entity group's published versions, newest
// first (ADR-062 S1).
func (r *SchemaResolver) EntityGroupVersions(ctx context.Context, args struct {
	Token string
}) ([]*EntityGroupVersionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	versions, err := api.EntityGroupVersions(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	result := make([]*EntityGroupVersionResolver, 0, len(versions))
	for _, v := range versions {
		result = append(result, &EntityGroupVersionResolver{M: *v, S: r, C: ctx})
	}
	return result, nil
}
