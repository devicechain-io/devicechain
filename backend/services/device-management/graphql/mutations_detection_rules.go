// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new detection rule.
func (r *SchemaResolver) CreateDetectionRule(ctx context.Context, args struct {
	Request model.DetectionRuleCreateRequest
}) (*DetectionRuleResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateDetectionRule(ctx, &args.Request)
	if err != nil {
		return nil, err
	}
	return &DetectionRuleResolver{M: *created, S: r, C: ctx}, nil
}

// Update an existing detection rule.
func (r *SchemaResolver) UpdateDetectionRule(ctx context.Context, args struct {
	Token   string
	Request model.DetectionRuleCreateRequest
}) (*DetectionRuleResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateDetectionRule(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}
	return &DetectionRuleResolver{M: *updated, S: r, C: ctx}, nil
}
