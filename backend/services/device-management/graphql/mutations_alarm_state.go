// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
)

// Acknowledge an alarm. An operator asserts they have seen the alarm; this is
// orthogonal to its ACTIVE/CLEARED state. The acknowledging identity is taken from
// the authenticated subject (not a caller-supplied value) so the accountability
// trail can't be forged.
func (r *SchemaResolver) AcknowledgeAlarm(ctx context.Context, args struct {
	Token string
}) (*AlarmResolver, error) {
	if err := auth.Authorize(ctx, auth.AlarmWrite); err != nil {
		return nil, err
	}

	var by *string
	if claims, ok := auth.ClaimsFromContext(ctx); ok && claims.Username != "" {
		by = &claims.Username
	}

	api := r.GetApi(ctx)
	updated, err := api.AcknowledgeAlarm(ctx, args.Token, by)
	if err != nil {
		return nil, err
	}
	return &AlarmResolver{M: *updated, S: r, C: ctx}, nil
}

// Clear an alarm. A manual operator override that moves the alarm to CLEARED; the DETECT
// edge integrator also clears when a rule's condition resolves (ADR-057).
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
