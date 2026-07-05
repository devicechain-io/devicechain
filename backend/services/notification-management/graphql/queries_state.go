// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-notification-management/model"
)

// NotificationStatesByAlarmToken finds per-alarm notification state by alarm token.
func (r *SchemaResolver) NotificationStatesByAlarmToken(ctx context.Context, args struct {
	AlarmTokens []string
}) ([]*NotificationStateResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationRead); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	found, err := api.NotificationStatesByAlarmToken(ctx, args.AlarmTokens)
	if err != nil {
		return nil, err
	}
	result := make([]*NotificationStateResolver, 0, len(found))
	for _, s := range found {
		result = append(result, &NotificationStateResolver{M: *s, S: r, C: ctx})
	}
	return result, nil
}

// NotificationStates lists per-alarm notification state matching the given criteria.
func (r *SchemaResolver) NotificationStates(ctx context.Context, args struct {
	Criteria model.NotificationStateSearchCriteria
}) (*NotificationStateSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationRead); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	found, err := api.NotificationStates(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &NotificationStateSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}
