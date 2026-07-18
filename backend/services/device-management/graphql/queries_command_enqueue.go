// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// CommandEnqueueVerdictResolver exposes the enqueue gate's verdict.
type CommandEnqueueVerdictResolver struct {
	M model.CommandEnqueueVerdict
}

// Whether the command may be enqueued.
func (r *CommandEnqueueVerdictResolver) Allowed() bool {
	return r.M.Allowed
}

// Why the command was rejected; null when allowed.
func (r *CommandEnqueueVerdictResolver) Reason() *string {
	if r.M.Reason == "" {
		return nil
	}
	reason := r.M.Reason
	return &reason
}

// ValidateCommandEnqueue decides whether a command may be enqueued to a device
// (ADR-043 decision 3). It is gated on device:read — the same authority the
// existing command/profile/device reads use, and the one command-delivery's
// service client already requests, so no new authority is introduced.
//
// A rejection returns allowed=false with a reason rather than a GraphQL error:
// the caller (command-delivery's enqueue path) must distinguish "this command is
// invalid" — which it relays to its API client — from "the check failed" — on
// which it fails closed with a sanitized message. Collapsing both into an error
// would make an outage indistinguishable from a bad command, and an outage would
// then read to the client as "your command is invalid".
func (r *SchemaResolver) ValidateCommandEnqueue(ctx context.Context, args struct {
	DeviceToken string
	CommandKey  string
	Payload     *string
}) (*CommandEnqueueVerdictResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	var payload []byte
	if args.Payload != nil {
		payload = []byte(*args.Payload)
	}

	verdict, err := r.GetApi(ctx).ValidateCommandEnqueue(ctx, args.DeviceToken, args.CommandKey, payload)
	if err != nil {
		return nil, err
	}
	return &CommandEnqueueVerdictResolver{M: *verdict}, nil
}
