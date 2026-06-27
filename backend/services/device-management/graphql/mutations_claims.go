// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"github.com/devicechain-io/dc-microservice/auth"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
)

// Open (or reopen) a possession claim on a device.
func (r *SchemaResolver) InitiateDeviceClaim(ctx context.Context, args struct {
	Request *model.DeviceClaimInitiateRequest
}) (*DeviceClaimResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	claim, err := api.InitiateDeviceClaim(ctx, args.Request)
	if err != nil {
		return nil, err
	}
	return &DeviceClaimResolver{M: *claim, S: r, C: ctx}, nil
}

// Redeem a device's claim, assigning it to a customer on proof of possession.
func (r *SchemaResolver) ClaimDevice(ctx context.Context, args struct {
	Request *model.DeviceClaimRequest
}) (*EntityRelationshipResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	rel, err := api.ClaimDevice(ctx, args.Request, time.Now())
	if err != nil {
		return nil, err
	}
	return &EntityRelationshipResolver{M: *rel, S: r, C: ctx}, nil
}

// Cancel a device's open claim without assigning it.
func (r *SchemaResolver) CancelDeviceClaim(ctx context.Context, args struct {
	DeviceToken string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}

	api := r.GetApi(ctx)
	return api.CancelDeviceClaim(ctx, args.DeviceToken)
}
