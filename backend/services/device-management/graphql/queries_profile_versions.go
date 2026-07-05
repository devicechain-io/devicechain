// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
)

// DeviceProfileVersions lists a device profile's published versions, newest first
// (ADR-045 versioning).
func (r *SchemaResolver) DeviceProfileVersions(ctx context.Context, args struct {
	Token string
}) ([]*DeviceProfileVersionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	versions, err := api.DeviceProfileVersions(ctx, args.Token)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceProfileVersionResolver, 0, len(versions))
	for _, v := range versions {
		result = append(result, &DeviceProfileVersionResolver{M: *v, S: r, C: ctx})
	}
	return result, nil
}
