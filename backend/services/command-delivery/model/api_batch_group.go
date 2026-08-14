// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
)

// groupTargetPageSize is how many members one resolution call asks for. It matches the
// bound the owner enforces on its own page size.
const groupTargetPageSize = 1000

// maxBarrenGroupPages bounds how many CONSECUTIVE pages may add no new device before the
// walk gives up.
//
// 🔴 IT COUNTS BARREN PAGES, NOT TOTAL PAGES, AND THE DIFFERENCE IS A FALSE FAILURE ON
// LEGAL INPUT. The first version fused on total pages at (MaxBatchDeviceTokens /
// groupTargetPageSize) + 10, reasoning that a legal group could not need more. That holds
// only if every page comes back FULL — and short pages are legal and expected: the owner
// post-filters, and membership churns mid-walk, which this very file documents as normal.
// A group legally returning ~half-full pages needs twice the page count, so a perfectly
// good ten-thousand-device fan-out would have died with "resolution did not terminate".
//
// Progress is the property actually being defended. A walk that keeps finding devices is
// working however many pages it takes; one that keeps being handed a cursor while adding
// nothing is a broken pagination contract — a cursor that does not advance, or a page that
// always claims a successor. Reaching this bound is therefore an ERROR rather than a
// truncation: silently returning a short target set would report a fleet write as complete
// when it commanded a fraction of the fleet.
const maxBarrenGroupPages = 5

// GroupTargetPage is one keyset page of a group's device members, as the owner answers it.
type GroupTargetPage struct {
	// Rejected reports that the group cannot serve as a command target at all — it does
	// not exist, collects something other than devices, was never published, or the named
	// version does not exist. Code and Reason explain it; every other field is meaningless.
	Rejected bool
	Code     string
	Reason   string

	DeviceTokens []string
	// NextCursor is empty when the group is exhausted.
	NextCursor string
	// ResolvedVersion is the frozen group version this page resolved against, nil for a
	// static group.
	ResolvedVersion *int32
}

// GroupTargetResolver resolves an entity group to the devices it collects, one keyset page
// at a time, at the service that owns the group.
//
// Every decision about whether the group may be commanded at all lives at that owner rather
// than here — it collects devices or it does not, it is published or it is not — and comes
// back as a decided rejection on the page. An error means the resolution could not be
// PERFORMED, and this service fails closed on it.
type GroupTargetResolver interface {
	ResolveGroupTargets(ctx context.Context, groupToken string, version *int32,
		afterCursor string, limit int) (*GroupTargetPage, error)
}

// resolveGroupTargets walks a group to its complete device set.
//
// 🔑 THE WALK IS THE SNAPSHOT. A dynamic group is evaluated as it is read, so membership can
// change between pages; the keyset cursor guarantees no device that STAYS in the set is
// skipped, and what remains — a device that joins below the cursor after the walk has passed
// it — is the documented semantic: a batch targets the group's membership as of the moment
// it fired. `Resolved` on the record is what the walk actually saw, so the record never
// claims a fleet size it did not enumerate.
//
// The walk stops at MaxBatchDeviceTokens rather than resolving an unbounded group. That
// bound is a refusal, not a truncation: quietly commanding the first ten thousand members of
// a fifty-thousand-device group and reporting success is the worst available answer, because
// nothing in the record would distinguish it from a complete fan-out.
func (api *Api) resolveGroupTargets(ctx context.Context, request *CommandBatchCreateRequest) (*batchTargets, error) {
	targets := &batchTargets{
		kind:       BatchTargetGroup,
		groupToken: sql.NullString{String: *request.GroupToken, Valid: true},
	}
	seen := make(map[string]struct{})
	cursor := ""

	barren := 0
	for barren < maxBarrenGroupPages {
		resolved, err := api.GroupTargetResolver.ResolveGroupTargets(ctx, *request.GroupToken,
			request.GroupVersion, cursor, groupTargetPageSize)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve the group target: group resolution is unavailable")
		}
		if resolved.Rejected {
			// The owner OWNS why a group is unusable, so its code is relayed alongside the
			// reason rather than re-derived here — a second classification built from the
			// prose would be a parse of prose, and a caller that can only read the sentence
			// cannot tell "not a device group" from "never published" without doing exactly
			// that. The reason stays the human half.
			return nil, rejected(RejectGroupUnusable, "cannot command group %q [%s]: %s",
				*request.GroupToken, resolved.Code, resolved.Reason)
		}
		if resolved.ResolvedVersion != nil {
			targets.groupVersion = sql.NullInt32{Int32: *resolved.ResolvedVersion, Valid: true}
		}
		before := len(targets.deviceTokens)
		for _, token := range resolved.DeviceTokens {
			// A keyset walk cannot legitimately return a device twice, but a group whose
			// members change mid-walk is exactly when a contract slip would show up — and
			// a duplicate here would be counted twice against the headroom and collide on
			// the intra-batch unique index.
			if _, already := seen[token]; already {
				continue
			}
			seen[token] = struct{}{}
			targets.deviceTokens = append(targets.deviceTokens, token)
		}
		if len(targets.deviceTokens) > MaxBatchDeviceTokens {
			return nil, rejected(RejectBatchTooLarge,
				"group %q resolves to more than %d devices, which is more than one batch may "+
					"command; narrow the group or split the operation",
				*request.GroupToken, MaxBatchDeviceTokens)
		}
		if resolved.NextCursor == "" {
			return targets, nil
		}
		if len(targets.deviceTokens) == before {
			barren++
		} else {
			barren = 0
		}
		cursor = resolved.NextCursor
	}
	// Consecutive pages that each promised a successor and added nobody. That is a broken
	// pagination contract — a cursor that does not advance, or a page that always claims
	// another — not a large fleet, so it is an error rather than a short target set.
	return nil, fmt.Errorf("cannot resolve the group target: resolution did not terminate")
}
