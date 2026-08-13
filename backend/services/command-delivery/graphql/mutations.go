// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// CommandEnqueueRejectionResolver exposes a refused enqueue: a stable code plus a
// client-safe reason.
type CommandEnqueueRejectionResolver struct {
	M model.EnqueueRejected
}

func (r *CommandEnqueueRejectionResolver) Code() string { return string(r.M.Code) }

func (r *CommandEnqueueRejectionResolver) Reason() string { return r.M.Reason }

// CreateCommandResultResolver carries EITHER the created command OR the rejection
// that refused it — never both, never neither.
type CreateCommandResultResolver struct {
	command   *CommandResolver
	rejection *CommandEnqueueRejectionResolver
}

func (r *CreateCommandResultResolver) Command() *CommandResolver { return r.command }

func (r *CreateCommandResultResolver) Rejection() *CommandEnqueueRejectionResolver {
	return r.rejection
}

// CreateCommand issues (persists) a new command to a device.
//
// 🔴 THE RETURN SHAPE SPLITS "NO" FROM "I COULD NOT ANSWER", and the split is the
// point of this mutation's payload:
//
//   - A REJECTION — the device does not exist, the command is not in the profile's
//     published vocabulary, the payload is malformed or violates its schema, the
//     tenant is at its held-command ceiling — is a DECIDED verdict about the request.
//     It comes back as `rejection` with a machine-readable code, so a caller can tell
//     a permanently-invalid command from a temporary refusal without parsing prose.
//   - A FAILURE TO ANSWER — the enqueue gate unreachable, a database error — stays a
//     GraphQL error, sanitized. It must NOT become a rejection: a rejection asserts
//     something about the caller's command that nobody actually checked, and the
//     detail behind an availability failure names in-cluster topology this tenant
//     client has no business learning.
//
// Collapsing them is what the previous shape did, and it is live-visible today: every
// rejection arrived as an error string, so REACT's dispatcher retried a
// device-deleted command until its poison cap gave up — indistinguishable, from the
// outside, from command-delivery being down.
func (r *SchemaResolver) CreateCommand(ctx context.Context, args struct {
	Request model.CommandCreateRequest
}) (*CreateCommandResultResolver, error) {
	if err := auth.Authorize(ctx, auth.CommandWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateCommand(ctx, &args.Request)
	if err != nil {
		var rejection *model.EnqueueRejected
		if errors.As(err, &rejection) {
			return &CreateCommandResultResolver{
				rejection: &CommandEnqueueRejectionResolver{M: *rejection},
			}, nil
		}
		return nil, err
	}

	return &CreateCommandResultResolver{
		command: &CommandResolver{
			M: *created,
			S: r,
			C: ctx,
		},
	}, nil
}

// CancelCommand cancels a non-terminal command by token (moves it to CANCELLED).
func (r *SchemaResolver) CancelCommand(ctx context.Context, args struct {
	Token string
}) (*CommandResolver, error) {
	if err := auth.Authorize(ctx, auth.CommandWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	cancelled, err := api.CancelCommand(ctx, args.Token)
	if err != nil {
		return nil, err
	}

	return &CommandResolver{
		M: *cancelled,
		S: r,
		C: ctx,
	}, nil
}

// MarkCommandSent claims a still-dispatchable command (QUEUED or HELD) for
// immediate delivery, moving it to SENT. It reports whether THIS call won the
// claim.
//
// It exists for a transport that dispatches a device's backlog itself instead of
// waiting for the delivery sweep — the LwM2M wake drain, which issues a sleeping
// device's held commands over the CoAP session it opens on Register. Claiming is
// what keeps that from double-actuating hardware: the sweep publishes anything
// still dispatchable, so a drain that dispatched without claiming would leave the
// row HELD for the next tick to publish again.
//
// 🔴 It returns a BOOLEAN, not the command. A caller uses this to decide whether
// it may actuate a physical device, and that decision must rest on whether the
// conditional UPDATE matched — not on a status field read back afterwards, which
// cannot distinguish "I claimed it" from "someone else did a millisecond ago".
// Returning the row would invite exactly that misread.
//
// 🔴 IT IS GATED ON command:claim, NOT command:write, AND THAT IS THE WHOLE
// SECURITY ARGUMENT. A claim is not a harmless status edit: it removes a command
// from the dispatchable set WITHOUT publishing it, so the command is never
// delivered, sits in SENT, and dies as TIMEOUT — a terminal that blames the
// DEVICE for a delivery the platform suppressed. Cancelling records the truth
// (CANCELLED, a named actor); this would manufacture a lie with nothing in the
// audit trail to contradict it.
//
// command:claim is SYSTEM-tier, and the existing vocabulary is what turns that
// into "service tokens only": auth.satisfies bounds a tenant access token —
// including one holding "*" — to tenant-tier authorities, and the data-plane
// handler already refuses identity tokens. So no explicit token-type check is
// needed here, and one would be strictly weaker than this: command:write plus a
// token-type check still admits EVERY service holding command:write, and
// event-processing's REACT sink mints exactly that, so a mutation for one caller
// would answer to two. A separate authority is granted to the one service that
// needs it, and being system-tier also stops an operator granting it on a
// tenant-scoped role — which a resolver-level check cannot police at all.
func (r *SchemaResolver) MarkCommandSent(ctx context.Context, args struct {
	Token string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.CommandClaim); err != nil {
		return false, err
	}

	api := r.GetApi(ctx)
	return api.MarkSentByToken(ctx, args.Token)
}
