// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
)

// Acknowledge an alarm. An operator asserts they have seen the alarm; this is
// orthogonal to its ACTIVE/CLEARED state. by is an optional operator reference.
func (r *SchemaResolver) AcknowledgeAlarm(ctx context.Context, args struct {
	Token string
	By    *string
}) (*AlarmResolver, error) {
	if err := auth.Authorize(ctx, auth.AlarmWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.AcknowledgeAlarm(ctx, args.Token, args.By)
	if err != nil {
		return nil, err
	}
	return &AlarmResolver{M: *updated, S: r, C: ctx}, nil
}

// Clear an alarm. A manual operator override that moves the alarm to CLEARED; the
// evaluator also auto-clears when a condition resolves (a later slice).
func (r *SchemaResolver) ClearAlarm(ctx context.Context, args struct {
	Token string
}) (*AlarmResolver, error) {
	if err := auth.Authorize(ctx, auth.AlarmWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.ClearAlarm(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return &AlarmResolver{M: *updated, S: r, C: ctx}, nil
}
