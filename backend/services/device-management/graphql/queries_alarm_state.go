// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find alarms by unique id.
func (r *SchemaResolver) AlarmsById(ctx context.Context, args struct {
	Ids []string
}) ([]*AlarmResolver, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := util.AsUintIds(args.Ids)
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
	if _, err := auth.AuthorizeScoped(ctx, auth.AlarmRead); err != nil {
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

// Count the alarms matching the criteria per originating device type and severity.
func (r *SchemaResolver) AlarmCountsByDeviceType(ctx context.Context, args struct {
	Criteria model.AlarmCountCriteria
}) ([]*AlarmDeviceTypeCountResolver, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}

	found, err := r.GetApi(ctx).AlarmCountsByDeviceType(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	result := make([]*AlarmDeviceTypeCountResolver, 0, len(found))
	for _, c := range found {
		result = append(result, &AlarmDeviceTypeCountResolver{M: c, S: r, C: ctx})
	}
	return result, nil
}

// AlarmDeviceTypeCountResolver resolves one (device type, severity) alarm count.
type AlarmDeviceTypeCountResolver struct {
	M model.AlarmDeviceTypeCount
	S *SchemaResolver
	C context.Context
}

func (r *AlarmDeviceTypeCountResolver) DeviceType() *DeviceTypeResolver {
	return &DeviceTypeResolver{M: *r.M.DeviceType, S: r.S, C: r.C}
}

func (r *AlarmDeviceTypeCountResolver) Severity() string { return r.M.Severity }

func (r *AlarmDeviceTypeCountResolver) Count() int32 { return int32(r.M.Count) }

// List all alarms that match the given criteria.
func (r *SchemaResolver) Alarms(ctx context.Context, args struct {
	Criteria model.AlarmSearchCriteria
}) (*AlarmSearchResultsResolver, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.Alarms(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}
	return &AlarmSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}
