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

// CancelCommand moves a command the platform still holds — QUEUED, HELD or PARKED — to
// CANCELLED, and answers with the row as it stands.
//
// 🔴 NOT "any non-terminal command", which is what this said before. SENT is non-terminal
// and is deliberately NOT cancellable: the command is at the device and nothing here can
// recall it. Naming a SENT (or already-terminal) command is not an error — it returns that
// command unchanged, so a caller must read the status back rather than treat a successful
// call as proof anything was stopped.
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

// ParkCommand hands back a command whose transport found the device UNREACHABLE, taking it
// SENT -> PARKED so the record says it went nowhere. It reports whether THIS call's park
// landed.
//
// It is the other half of MarkCommandSent for a transport that cannot deliver on demand. A
// queue-mode device is REGISTERED but asleep, and the platform decides to publish from
// presence, so the command goes out and the transport finds no live session. Before this,
// such a row stayed SENT — the state meaning "the device has it" — which made the platform
// blame the device at TTL (TIMEOUT), told an operator cancelling a fleet write that the
// command was beyond recall when it was not, and left the row to be re-dispatched without a
// claim on the next wake.
//
// 🔴 IT TAKES THE DISPATCH NONCE, AND A PARK WITHOUT ONE WOULD RE-ARM AN ACTUATED COMMAND.
// The caller quotes the nonce from the envelope it was handed. A request that is a
// redelivery of an older publish therefore names a dispatch that has since been superseded,
// matches nothing, and moves no row. The full sequence is on model.Command.DispatchNonce.
//
// 🔴 FALSE IS A SETTLED OUTCOME, NOT A FAILURE. It means the row moved on under the park —
// answered, cancelled, expired, or re-claimed by a wake drain. A caller that retried on
// false would burn every redelivery on a row that is already where it should be.
//
// 🔴 IT IS GATED ON command:park, ITS OWN AUTHORITY, RATHER THAN REUSING command:claim.
// The vocabulary already splits on exactly this axis: claiming takes a command OUT of the
// dispatchable set, waking puts one BACK IN, and they are separate authorities for that
// reason alone. Parking is the put-back-in direction FROM SENT, which is strictly stronger
// than waking — it is the only way anything outside this service can move a row backwards
// out of the dispatched state, and a holder could use it to re-arm another transport's
// in-flight command. Folding that into command:claim would also falsify what that authority
// documents itself to mean. Adding an authority is cheap now and breaking once roles exist
// in the wild.
func (r *SchemaResolver) ParkCommand(ctx context.Context, args struct {
	Token         string
	DispatchNonce string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.CommandPark); err != nil {
		return false, err
	}

	api := r.GetApi(ctx)
	return api.ParkClaim(ctx, args.Token, args.DispatchNonce)
}

// MarkCommandSent claims a still-dispatchable command (QUEUED, HELD or PARKED) for
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

// ReleaseHeldCommands returns a device's withheld commands to the delivery queue,
// answering how many it released.
//
// 🔴 THE ADR-077 LIFECYCLE GATE IS DELIBERATELY NOT CALLED HERE, WHICH IS A DEPARTURE
// FROM THE OTHER TWO RELEASE PATHS AND IS WORTH THE PARAGRAPH. The rule this codebase
// follows is that the gate belongs on every path that can put a command in front of a
// dispatcher — and the delivery sweep, which is the only thing that dispatches, applies
// it. Waking moves rows HELD -> QUEUED; for a deleted tenant those rows are then refused
// by the sweep exactly as they were while held, so nothing actuates either way.
//
// What differs from the reconciler, which DOES gate, is cost rather than principle: the
// reconciler's check is an in-process closure over a batch it already grouped by tenant,
// while this one sits on the device-connect path and would add a user-management round
// trip per connecting device — paid on every reconnect storm, to prevent a status change
// on rows that are being erased. The load-bearing gate is the sweep's. If waking ever
// grows a direct dispatch, this decision has to be revisited with it.
//
// command:wake rather than command:claim, and the two are mirror images: claiming takes a
// command out of the dispatchable set without delivering it, while waking puts one back
// in. The worst a spurious wake does is cost one extra evaluation, since the sweep
// re-reads presence and withholds it again — so the split is least privilege, not danger.
// Being system-tier is what keeps it to service tokens.
func (r *SchemaResolver) ReleaseHeldCommands(ctx context.Context, args struct {
	DeviceToken string
}) (int32, error) {
	if err := auth.Authorize(ctx, auth.CommandWake); err != nil {
		return 0, err
	}

	api := r.GetApi(ctx)
	released, err := api.ReleaseHeldForDevice(ctx, args.DeviceToken)
	if err != nil {
		return 0, err
	}
	return int32(released), nil
}
