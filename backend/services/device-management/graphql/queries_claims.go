// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// Get a device's current possession claim, or null if it has none.
func (r *SchemaResolver) DeviceClaim(ctx context.Context, args struct {
	DeviceToken string
}) (*DeviceClaimResolver, error) {
	api := r.GetApi(ctx)
	claim, err := api.DeviceClaimByDeviceToken(ctx, args.DeviceToken)
	if err != nil {
		// A device with no claim is a null result, not an error.
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &DeviceClaimResolver{M: *claim, S: r, C: ctx}, nil
}
