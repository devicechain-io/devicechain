// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"strconv"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// maxBatchValidationTokens bounds one validateCommandEnqueueBatch request.
//
// The caller chunks a large fleet, so this is a REQUEST bound rather than a fleet bound:
// it stops one document from carrying an arbitrarily large variable and turning a read
// into a memory event, without capping how many devices a batch may ultimately cover.
const maxBatchValidationTokens = 2000

// maxGroupTargetPageSize bounds one page of a group-target walk, matching the rdb
// page-size clamp so a caller cannot turn the keyset walk into an unbounded scan by
// asking for one enormous page — the same protection ResolveGroupMembers gets by forcing
// Unbounded off.
const maxGroupTargetPageSize = 1000

// BatchEnqueueRefusalResolver exposes one device's refusal within a batch.
type BatchEnqueueRefusalResolver struct {
	M model.BatchEnqueueRefusal
}

func (r *BatchEnqueueRefusalResolver) DeviceToken() string { return r.M.DeviceToken }

func (r *BatchEnqueueRefusalResolver) Code() string { return string(r.M.Code) }

func (r *BatchEnqueueRefusalResolver) Reason() string { return r.M.Reason }

// ValidateCommandEnqueueBatch decides, for many devices at once, whether one command with
// one payload may be enqueued to each — the fan-out counterpart of validateCommandEnqueue,
// gated on the same device:read authority so no new authority is introduced.
//
// 🔑 IT ANSWERS WITH REFUSALS ONLY. A device that may receive the command produces no
// entry, so the normal answer for a healthy fleet is the empty list and the response
// scales with the problems rather than with the fleet. A caller reconstructs "allowed" as
// "asked about, and not named here" — which also means a caller must send tokens it
// actually intends to enqueue to, since silence is consent.
//
// An over-large request is an ERROR rather than a refusal list: it is a fault in how the
// caller chunked its work, not a verdict about any device, and answering it with
// per-device refusals would report a client bug as a fleet condition.
func (r *SchemaResolver) ValidateCommandEnqueueBatch(ctx context.Context, args struct {
	DeviceTokens []string
	CommandKey   string
	Payload      *string
}) ([]*BatchEnqueueRefusalResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	if len(args.DeviceTokens) > maxBatchValidationTokens {
		return nil, fmt.Errorf("cannot validate %d devices in one request; the limit is %d",
			len(args.DeviceTokens), maxBatchValidationTokens)
	}

	var payload []byte
	if args.Payload != nil {
		payload = []byte(*args.Payload)
	}

	refusals, err := r.GetApi(ctx).ValidateCommandEnqueueBatch(ctx, args.DeviceTokens, args.CommandKey, payload)
	if err != nil {
		return nil, err
	}
	resolved := make([]*BatchEnqueueRefusalResolver, 0, len(refusals))
	for _, refusal := range refusals {
		if refusal == nil {
			continue
		}
		resolved = append(resolved, &BatchEnqueueRefusalResolver{M: *refusal})
	}
	return resolved, nil
}

// DeviceGroupTargetPageResolver exposes one keyset page of a group's device members.
type DeviceGroupTargetPageResolver struct {
	M model.DeviceGroupTargetPage
}

func (r *DeviceGroupTargetPageResolver) Rejected() bool { return r.M.Rejected }

// The machine-readable classification of a refusal; null when not rejected.
func (r *DeviceGroupTargetPageResolver) Code() *string {
	if r.M.Code == "" {
		return nil
	}
	code := string(r.M.Code)
	return &code
}

// Why the group cannot serve as a command target; null when not rejected.
func (r *DeviceGroupTargetPageResolver) Reason() *string {
	if r.M.Reason == "" {
		return nil
	}
	reason := r.M.Reason
	return &reason
}

func (r *DeviceGroupTargetPageResolver) DeviceTokens() []string {
	if r.M.DeviceTokens == nil {
		return []string{}
	}
	return r.M.DeviceTokens
}

// The cursor to pass as afterId for the next page; null when the group is exhausted.
//
// It is a String rather than an Int because it is a row id, and GraphQL's Int is a signed
// 32-bit value — an id beyond 2^31 would silently fail to marshal on the one field whose
// job is to not lose members.
func (r *DeviceGroupTargetPageResolver) NextCursor() *string {
	if r.M.NextCursor == 0 {
		return nil
	}
	cursor := fmt.Sprintf("%d", r.M.NextCursor)
	return &cursor
}

// The frozen group version this page resolved against; null for a static group.
func (r *DeviceGroupTargetPageResolver) ResolvedVersion() *int32 {
	return r.M.ResolvedVersion
}

// ResolveDeviceGroupTargets resolves one keyset page of the device tokens an entity group
// collects — the target-set primitive a fleet-wide command write pages through.
//
// It is deliberately NOT the general entityGroupMembers query with a cursor bolted on.
// Three decisions belong at the owner of the group, not at the caller, and all three are
// refusals this returns as decided verdicts rather than errors:
//
//   - the group must collect DEVICES (tokens are unique per table, so an asset group's
//     members would otherwise be commanded as devices wherever a token collides);
//   - a dynamic group resolves its FROZEN published version, never the mutable draft;
//   - a never-published dynamic group is refused rather than falling back to that draft.
//
// Gated on device:read, like every other group read.
func (r *SchemaResolver) ResolveDeviceGroupTargets(ctx context.Context, args struct {
	GroupToken string
	Version    *int32
	AfterId    *string
	Limit      int32
}) (*DeviceGroupTargetPageResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	if args.Limit <= 0 || args.Limit > maxGroupTargetPageSize {
		return nil, fmt.Errorf("group target page size must be between 1 and %d, got %d",
			maxGroupTargetPageSize, args.Limit)
	}

	var afterId uint64
	if args.AfterId != nil && *args.AfterId != "" {
		parsed, err := parseCursor(*args.AfterId)
		if err != nil {
			return nil, err
		}
		afterId = parsed
	}

	page, err := r.GetApi(ctx).ResolveDeviceGroupTargets(ctx, args.GroupToken, args.Version, afterId, int(args.Limit))
	if err != nil {
		return nil, err
	}
	return &DeviceGroupTargetPageResolver{M: *page}, nil
}

// parseCursor reads an opaque page cursor back into a row id.
//
// A malformed cursor is an ERROR, never a silent restart from zero. Zero means "begin",
// so swallowing a parse failure would quietly re-walk the group from the start — a fleet
// write that re-commands every device it has already commanded, reported as success.
//
// 🔴 IT USED fmt.Sscanf AND THAT COMMENT WAS A LIE. Sscanf("%d") stops at the first
// non-digit and reports SUCCESS for the prefix it managed to read: "12abc" and " 12"
// both yielded 12, and "0x10" yielded 0 with no error — landing exactly on the
// re-walk-from-the-beginning this is written to prevent, via a string a caller could
// plausibly produce. ParseUint rejects the whole class because it consumes the ENTIRE
// string or fails.
//
// Zero is rejected outright rather than treated as "begin": the resolver never emits
// "0" as a cursor (a zero NextCursor marshals as null), so a caller presenting one is
// confused, and the safe reading of a cursor that cannot have come from us is refusal.
//
// 🔴 IT RETURNS uint64 AND NARROWS NOTHING, which is the second correction this function
// has needed. The first attempt parsed 64 bits and converted to `uint`, guarded by
// `id > math.MaxUint` — and that guard is a TAUTOLOGY on a 64-bit build, where math.MaxUint
// IS MaxUint64, so it narrowed nothing and constrained nothing. CodeQL was right to keep
// flagging it. Had it ever run on a 32-bit build the conversion would have truncated, and a
// truncated cursor is not a refused one: it is a valid-looking id pointing somewhere else in
// the table, silently resuming the fleet walk at the wrong place.
//
// The cursor is only ever the right-hand side of a SQL `id >` comparison, so there is no
// reason to narrow it at all. Carrying it as uint64 end to end deletes the failure mode
// rather than guarding it — which is the fix that should have been made the first time,
// instead of adding a bound to a conversion that did not need to exist.
func parseCursor(cursor string) (uint64, error) {
	id, err := strconv.ParseUint(cursor, 10, 64)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("malformed page cursor %q", cursor)
	}
	return id, nil
}
