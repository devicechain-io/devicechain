// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// CommandBatchRejectionResolver exposes a refused batch: a stable code, a client-safe
// reason, and — when specific devices caused the refusal — those devices.
//
// 🔑 IT IS ONE TYPE OVER TWO INTERNAL ONES. CreateCommandBatch refuses with
// *model.EnqueueRejected for request-shaped problems (ambiguous target, too large,
// malformed JSON, unusable group) and *model.BatchRejected for the two that must name
// devices (partial-refused, ceiling). A caller has no way to know which internal type
// produced its refusal and no reason to care: both answer "why was my batch refused?",
// and forcing the distinction onto the wire would make every client branch on an
// implementation detail of this service.
type CommandBatchRejectionResolver struct {
	code          string
	reason        string
	resolved      *int32
	refusals      []model.BatchDeviceRefusal
	refusalCounts []model.RefusalCount
}

func (r *CommandBatchRejectionResolver) Code() string { return r.code }

func (r *CommandBatchRejectionResolver) Reason() string { return r.reason }

// How many devices the target resolved to before the refusal.
//
// 🔴 IT IS NULLABLE, AND NULL IS NOT ZERO. A group that legally resolves to no devices
// creates a real batch with resolved=0; a refusal that happened before any target was
// established never counted anything. Rendering the second as 0 would state a fleet size
// nobody measured, and "0 devices" is precisely the answer an operator would act on.
//
// The request-shaped refusals therefore report null, and that includes BATCH_TOO_LARGE
// raised from a GROUP walk — which is worth spelling out, because it looks at first like
// a case where a count exists and is being withheld. It is not. That refusal fires
// MID-WALK, the moment the accumulated set crosses the bound, and the walk then aborts:
// the group's actual membership is never established, and the ~10,001 tokens gathered so
// far are a prefix of an unknown total. There is nothing to report. Do not "fix" this by
// wiring the partial count through — it would put a number in a field that means
// "how big is the target set" while answering "where did we stop counting".
func (r *CommandBatchRejectionResolver) Resolved() *int32 { return r.resolved }

func (r *CommandBatchRejectionResolver) Refusals() []*BatchDeviceRefusalResolver {
	return refusalResolvers(r.refusals)
}

func (r *CommandBatchRejectionResolver) RefusalCounts() []*BatchRefusalCountResolver {
	return refusalCountResolvers(r.refusalCounts)
}

// CreateCommandBatchResultResolver carries EITHER the created batch OR the rejection
// that refused it — never both, never neither.
type CreateCommandBatchResultResolver struct {
	batch     *CommandBatchResolver
	rejection *CommandBatchRejectionResolver
}

func (r *CreateCommandBatchResultResolver) Batch() *CommandBatchResolver { return r.batch }

func (r *CreateCommandBatchResultResolver) Rejection() *CommandBatchRejectionResolver {
	return r.rejection
}

// batchRejectionResolver classifies an error from CreateCommandBatch into a rejection
// payload, or reports that it is not a rejection at all.
//
// 🔴 IT MATCHES BOTH REJECTION TYPES, AND THAT IS THE WHOLE POINT OF THE FUNCTION.
// Handling only *EnqueueRejected — the obvious implementation, and the one this
// mutation's single-command sibling gets away with — would sanitize BATCH_PARTIAL_REFUSED
// and HELD_CEILING_EXCEEDED into opaque GraphQL errors. Those two are the most
// operationally common outcomes of a real fleet write: the first is the DEFAULT response
// to any heterogeneous fleet (allowPartial is off unless asked for), and the second is
// what a busy tenant gets. Losing them would tell an operator the platform is broken at
// exactly the moment it is working correctly and has a precise, actionable answer — and
// would discard the device list that says which devices to fix.
//
// The order matters: *BatchRejected is checked FIRST because it is the richer type. Both
// are matched by errors.As against their own concrete types, so the order is not strictly
// required today, but a future wrapper that satisfies both would otherwise silently lose
// the refusal list.
func batchRejectionResolver(err error) (*CommandBatchRejectionResolver, bool) {
	var batchRejection *model.BatchRejected
	if errors.As(err, &batchRejection) {
		resolved := int32(batchRejection.Resolved)
		return &CommandBatchRejectionResolver{
			code:          string(batchRejection.Code),
			reason:        batchRejection.Reason,
			resolved:      &resolved,
			refusals:      batchRejection.Refusals,
			refusalCounts: batchRejection.RefusalCounts,
		}, true
	}
	var enqueueRejection *model.EnqueueRejected
	if errors.As(err, &enqueueRejection) {
		// No resolved count and no devices: these refuse the REQUEST, before or without
		// regard to which devices it names.
		return &CommandBatchRejectionResolver{
			code:   string(enqueueRejection.Code),
			reason: enqueueRejection.Reason,
		}, true
	}
	return nil, false
}

// CreateCommandBatch issues one command to many devices as a single recorded operation.
//
// 🔴 A DEVICE-LIST BATCH NEEDS command:write; A GROUP-TARGETED ONE ALSO NEEDS
// device:read, AND THE SECOND GATE IS NOT SYMMETRY — IT CLOSES A CONFUSED DEPUTY.
// Resolving a group to its members is a READ this service performs under its OWN service
// identity, which is minted with device:read (see wireDeviceManagementGates). The caller
// then learns what that read found: the refusal list names device tokens, and
// resolved/accepted disclose the group's size. So without this gate, a token holding
// command:write and no device:read could enumerate an entity group's membership by
// firing a batch at it — using our authority to answer a question it is not allowed to
// ask. That is not hypothetical: REACT's send-command sink mints command:write ALONE.
//
// Looping createCommand cannot do this, which is the asymmetry that matters. The loop
// requires the caller to already KNOW every device token; the single enqueue gate only
// confirms or denies tokens it was handed. A group target is the one path that hands
// tokens BACK.
//
// 🔑 THE SIZE OF THE FAN-OUT, BY CONTRAST, NEEDS NO NEW AUTHORITY. Anyone holding
// command:write can already command a fleet by looping createCommand. That loop is
// bounded by the same tenant ceiling this is (checkUndeliveredCeiling runs inside every
// single enqueue too) — what it lacks is the RECORD, the per-tenant serialization, the
// frozen group version and a way to cancel what it started. Gating the recorded path
// more heavily than the unrecorded one would push operators toward the loop, withholding
// no capability while losing the audit trail.
//
// The result shape mirrors CreateCommandResult: a decided refusal comes back as
// `rejection` with a machine-readable code, while a failure to DECIDE (device-management
// unreachable, group resolution unavailable, a database error) stays a sanitized GraphQL
// error. A caller that cannot tell those apart retries a permanently-invalid fleet write.
func (r *SchemaResolver) CreateCommandBatch(ctx context.Context, args struct {
	Request model.CommandBatchCreateRequest
}) (*CreateCommandBatchResultResolver, error) {
	if err := auth.Authorize(ctx, auth.CommandWrite); err != nil {
		return nil, err
	}
	// Gated on the request naming a group, not on resolution succeeding, so the check
	// runs BEFORE anything is resolved and an unauthorized caller learns nothing at all
	// — not even whether the group exists.
	if args.Request.GroupToken != nil && *args.Request.GroupToken != "" {
		if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
			return nil, err
		}
	}

	api := r.GetApi(ctx)
	created, err := api.CreateCommandBatch(ctx, &args.Request)
	if err != nil {
		if rejection, isRejection := batchRejectionResolver(err); isRejection {
			return &CreateCommandBatchResultResolver{rejection: rejection}, nil
		}
		return nil, err
	}

	return &CreateCommandBatchResultResolver{
		batch: &CommandBatchResolver{
			M: *created,
			S: r,
			C: ctx,
		},
	}, nil
}
