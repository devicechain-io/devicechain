// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find detection rules by unique id.
func (r *SchemaResolver) DetectionRulesById(ctx context.Context, args struct {
	Ids []string
}) ([]*DetectionRuleResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.DetectionRulesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*DetectionRuleResolver, 0)
	for _, dr := range found {
		result = append(result, &DetectionRuleResolver{M: *dr, S: r, C: ctx})
	}
	return result, nil
}

// Find detection rules by unique token.
func (r *SchemaResolver) DetectionRulesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*DetectionRuleResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DetectionRulesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*DetectionRuleResolver, 0)
	for _, dr := range found {
		result = append(result, &DetectionRuleResolver{M: *dr, S: r, C: ctx})
	}
	return result, nil
}

// List all detection rules that match the given criteria.
func (r *SchemaResolver) DetectionRules(ctx context.Context, args struct {
	Criteria model.DetectionRuleSearchCriteria
}) (*DetectionRuleSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DetectionRules(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &DetectionRuleSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}
