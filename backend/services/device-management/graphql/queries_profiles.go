// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// Find device profiles by unique id.
func (r *SchemaResolver) DeviceProfilesById(ctx context.Context, args struct {
	Ids []string
}) ([]*DeviceProfileResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	ids, err := r.asUintIds(args.Ids)
	if err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DeviceProfilesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceProfileResolver, 0)
	for _, dp := range found {
		result = append(result, &DeviceProfileResolver{M: *dp, S: r, C: ctx})
	}
	return result, nil
}

// Find device profiles by unique token.
func (r *SchemaResolver) DeviceProfilesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*DeviceProfileResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DeviceProfilesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceProfileResolver, 0)
	for _, dp := range found {
		result = append(result, &DeviceProfileResolver{M: *dp, S: r, C: ctx})
	}
	return result, nil
}

// List device profiles that meet criteria.
func (r *SchemaResolver) DeviceProfiles(ctx context.Context, args struct {
	Criteria model.DeviceProfileSearchCriteria
}) (*DeviceProfileSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	results, err := api.DeviceProfiles(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &DeviceProfileSearchResultsResolver{M: *results, S: r, C: ctx}, nil
}
