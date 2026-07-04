// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find alarms by unique id.
func (r *SchemaResolver) AlarmsById(ctx context.Context, args struct {
	Ids []string
}) ([]*AlarmResolver, error) {
	if err := auth.Authorize(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.AlarmsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*AlarmResolver, 0)
	for _, a := range found {
		result = append(result, &AlarmResolver{M: *a, S: r, C: ctx})
	}
	return result, nil
}

// Find alarms by unique token.
func (r *SchemaResolver) AlarmsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*AlarmResolver, error) {
	if err := auth.Authorize(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.AlarmsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*AlarmResolver, 0)
	for _, a := range found {
		result = append(result, &AlarmResolver{M: *a, S: r, C: ctx})
	}
	return result, nil
}

// List all alarms that match the given criteria.
func (r *SchemaResolver) Alarms(ctx context.Context, args struct {
	Criteria model.AlarmSearchCriteria
}) (*AlarmSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.Alarms(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &AlarmSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}
