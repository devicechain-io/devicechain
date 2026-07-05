// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-notification-management/model"
)

// NotificationPoliciesById finds routing policies by numeric id.
func (r *SchemaResolver) NotificationPoliciesById(ctx context.Context, args struct {
	Ids []string
}) ([]*NotificationPolicyResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationRead); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.NotificationPoliciesById(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]*NotificationPolicyResolver, 0, len(found))
	for _, p := range found {
		result = append(result, &NotificationPolicyResolver{M: *p, S: r, C: ctx})
	}
	return result, nil
}

// NotificationPoliciesByToken finds routing policies by token.
func (r *SchemaResolver) NotificationPoliciesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*NotificationPolicyResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationRead); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	found, err := api.NotificationPoliciesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}
	result := make([]*NotificationPolicyResolver, 0, len(found))
	for _, p := range found {
		result = append(result, &NotificationPolicyResolver{M: *p, S: r, C: ctx})
	}
	return result, nil
}

// NotificationPolicies lists routing policies matching the given criteria.
func (r *SchemaResolver) NotificationPolicies(ctx context.Context, args struct {
	Criteria model.NotificationPolicySearchCriteria
}) (*NotificationPolicySearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationRead); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	found, err := api.NotificationPolicies(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &NotificationPolicySearchResultsResolver{M: *found, S: r, C: ctx}, nil
}
