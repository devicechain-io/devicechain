// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
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

// AlarmAckRefusalResolver exposes one alarm a bulk acknowledge did not act on.
type AlarmAckRefusalResolver struct {
	M model.AlarmAckRefusal
}

func (r *AlarmAckRefusalResolver) Token() string { return r.M.Token }

func (r *AlarmAckRefusalResolver) Code() string { return string(r.M.Code) }

func (r *AlarmAckRefusalResolver) Reason() string { return r.M.Reason }

// BulkAcknowledgeAlarmsResultResolver exposes the outcome of one bulk acknowledge.
type BulkAcknowledgeAlarmsResultResolver struct {
	M model.BulkAcknowledgeResult
	S *SchemaResolver
	C context.Context
}

func (r *BulkAcknowledgeAlarmsResultResolver) Acknowledged() []*AlarmResolver {
	resolved := make([]*AlarmResolver, 0, len(r.M.Acknowledged))
	for i := range r.M.Acknowledged {
		resolved = append(resolved, &AlarmResolver{M: r.M.Acknowledged[i], S: r.S, C: r.C})
	}
	return resolved
}

func (r *BulkAcknowledgeAlarmsResultResolver) Refusals() []*AlarmAckRefusalResolver {
	resolved := make([]*AlarmAckRefusalResolver, 0, len(r.M.Refusals))
	for i := range r.M.Refusals {
		resolved = append(resolved, &AlarmAckRefusalResolver{M: r.M.Refusals[i]})
	}
	return resolved
}

// Acknowledge many alarms at once — the fan-out counterpart of acknowledgeAlarm, for an operator
// facing an alarm storm. Same authority, same transition, same accountability trail: the
// acknowledging identity is the authenticated subject, taken server-side, and the model layer
// shares ONE acknowledgment transition with the single mutation rather than mirroring it.
//
// It is partial by design (a token naming no alarm is refused, the rest are still acknowledged) and
// bounded (an over-large request is an error, not a per-alarm verdict). An EMPTY list acknowledges
// nothing — see model.AcknowledgeAlarms, which enforces that before any statement is built.
func (r *SchemaResolver) AcknowledgeAlarms(ctx context.Context, args struct {
	Tokens []string
}) (*BulkAcknowledgeAlarmsResultResolver, error) {
	if err := auth.Authorize(ctx, auth.AlarmWrite); err != nil {
		return nil, err
	}

	var by *string
	if claims, ok := auth.ClaimsFromContext(ctx); ok && claims.Username != "" {
		by = &claims.Username
	}

	result, err := r.GetApi(ctx).AcknowledgeAlarms(ctx, args.Tokens, by)
	if err != nil {
		return nil, err
	}
	return &BulkAcknowledgeAlarmsResultResolver{M: *result, S: r, C: ctx}, nil
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
