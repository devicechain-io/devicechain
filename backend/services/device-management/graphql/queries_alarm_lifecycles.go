// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

type AlarmLifecycleResolver struct{ M model.AlarmLifecycle }

func (r *AlarmLifecycleResolver) Token() string { return r.M.Token }
func (r *AlarmLifecycleResolver) State() string { return r.M.State }

func (r *SchemaResolver) AlarmLifecycles(ctx context.Context, args struct{ Tokens []string }) ([]*AlarmLifecycleResolver, error) {
	// Service plane only: even a tenant administrator cannot probe withheld alarms here.
	if err := auth.AuthorizeService(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).AlarmLifecycles(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}
	result := make([]*AlarmLifecycleResolver, 0, len(found))
	for _, row := range found {
		result = append(result, &AlarmLifecycleResolver{M: row})
	}
	return result, nil
}
