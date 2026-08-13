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
// 🔴 SERVICE TOKENS ONLY, and command:write is NOT sufficient on its own.
// command:write is a TENANT-tier authority (see auth.tiersByAuthority), so every
// tenant user who can issue or cancel a command would otherwise be able to claim
// one — and a claim is not a harmless status edit. It removes a command from the
// dispatchable set WITHOUT publishing it, so the command is never delivered, sits
// in SENT, and dies as TIMEOUT: a terminal state that blames the DEVICE for a
// delivery the platform silently suppressed. Cancelling records the truth
// (CANCELLED, a named actor); this would manufacture a lie with nothing in the
// audit trail to contradict it.
//
// No human surface offers this mutation and none should: it exists for one
// machine caller, the LwM2M wake drain, which claims a command it is about to put
// on a live CoAP session. The tier vocabulary alone cannot express "service
// tokens only" — that takes an explicit TokenType check here, which is exactly
// what the authority table's own note says is required when it must be true.
func (r *SchemaResolver) MarkCommandSent(ctx context.Context, args struct {
	Token string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.CommandWrite); err != nil {
		return false, err
	}
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok || claims.TokenType != auth.TokenTypeService {
		return false, auth.ErrForbidden
	}

	api := r.GetApi(ctx)
	return api.MarkSentByToken(ctx, args.Token)
}
