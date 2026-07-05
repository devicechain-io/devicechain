// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-notification-management/model"
)

// NotificationChannelsById finds delivery channels by numeric id.
func (r *SchemaResolver) NotificationChannelsById(ctx context.Context, args struct {
	Ids []string
}) ([]*NotificationChannelResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationRead); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.NotificationChannelsById(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]*NotificationChannelResolver, 0, len(found))
	for _, c := range found {
		result = append(result, &NotificationChannelResolver{M: *c, S: r, C: ctx})
	}
	return result, nil
}

// NotificationChannelsByToken finds delivery channels by token.
func (r *SchemaResolver) NotificationChannelsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*NotificationChannelResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationRead); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	found, err := api.NotificationChannelsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}
	result := make([]*NotificationChannelResolver, 0, len(found))
	for _, c := range found {
		result = append(result, &NotificationChannelResolver{M: *c, S: r, C: ctx})
	}
	return result, nil
}

// NotificationChannels lists delivery channels matching the given criteria.
func (r *SchemaResolver) NotificationChannels(ctx context.Context, args struct {
	Criteria model.NotificationChannelSearchCriteria
}) (*NotificationChannelSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.NotificationRead); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	found, err := api.NotificationChannels(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &NotificationChannelSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}
