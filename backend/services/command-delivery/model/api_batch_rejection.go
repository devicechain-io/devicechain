// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"sort"
)

// MaxBatchDeviceTokens bounds how many devices one request may name explicitly.
//
// 🔴 IT IS INPUT VALIDATION, NOT GOVERNANCE, and deliberately independent of the tenant's
// ceiling. Its job is to stop a request from being enormous before anything has looked at
// it — a million-token document would exhaust memory long before the ceiling check could
// refuse it. Deriving it from the platform ceiling default would ALSO be wrong in the other
// direction: a tenant whose resolved ceiling exceeds that default would be capped below its
// own governed limit by a number that is not a governance knob.
const MaxBatchDeviceTokens = 10000

// batchValidationChunk is how many device tokens go to the owner's fleet gate per call. It
// matches the bound that gate enforces on its own request size.
const batchValidationChunk = 2000

const (
	// RejectBatchTargetAmbiguous: the request named both a device list and a group, or
	// neither, or a version without a group. Permanent for this request.
	RejectBatchTargetAmbiguous RejectionCode = "BATCH_TARGET_AMBIGUOUS"
	// RejectBatchTooLarge: the request named more devices than one request may carry.
	// Permanent — the caller must split the batch.
	RejectBatchTooLarge RejectionCode = "BATCH_TOO_LARGE"
	// RejectBatchPartialRefused: at least one device cannot receive the command and the
	// caller did not opt into a partial fan-out. NOTHING was created.
	//
	// It is temporary or permanent depending on WHY each device was refused, which is
	// exactly why the refusals travel with it: a device missing from the vocabulary needs
	// a profile change, while one refused for headroom will succeed once the backlog
	// drains. A single code cannot say both, so it says neither and defers to the list.
	RejectBatchPartialRefused RejectionCode = "BATCH_PARTIAL_REFUSED"
	// RejectTokenInUse: the command token names a row this caller does not own — in
	// practice, one the platform minted for a batch. Permanent for this token; pick
	// another.
	//
	// It exists because the alternative outcomes are both worse: returning the occupying
	// command would hand the caller a different device's actuation as an idempotent replay,
	// and succeeding silently would report a command that was never created.
	RejectTokenInUse RejectionCode = "TOKEN_IN_USE"
	// RejectGroupUnusable: the named group cannot serve as a command target — it does not
	// exist, collects something other than devices, was never published, or the named
	// version does not exist. The owner's own code travels in the reason.
	RejectGroupUnusable RejectionCode = "BATCH_GROUP_UNUSABLE"
)

// BatchRejected is a refused batch: a classification, a client-safe reason, and — when the
// refusal was caused by specific devices — those devices.
//
// 🔴 THE REFUSAL LIST IS WHY THIS TYPE EXISTS RATHER THAN REUSING EnqueueRejected. The
// default (allowPartial false) refuses the whole batch when ANY device cannot receive the
// command, so on a heterogeneous fleet that is the ordinary outcome — and it creates no
// batch record, because nothing was created. Without the list riding on the rejection
// itself, the platform's most-travelled batch failure would answer "some devices were
// refused" and offer no way to learn WHICH, leaving the operator to bisect a fleet by hand.
//
// Resolved travels too, so the caller can say "3 of 5,000" rather than "3".
type BatchRejected struct {
	Code     RejectionCode
	Reason   string
	Resolved int
	// Refusals is a bounded sample of the devices responsible, and RefusalCounts the
	// complete per-code totals — the same split the persisted record uses, for the same
	// reason: a dynamic group can resolve to tens of thousands of devices, and an
	// unbounded list on an error path is a response nobody can read anyway.
	Refusals      []BatchDeviceRefusal
	RefusalCounts []RefusalCount
}

func (e *BatchRejected) Error() string { return e.Reason }

// batchRejection builds a batch rejection, bounding the refusal sample the same way the
// persisted record does so the two views of the same event agree.
func batchRejection(code RejectionCode, refusals []BatchDeviceRefusal, resolved int,
	format string, args ...any) *BatchRejected {
	kept, counts := boundRefusals(refusals)
	return &BatchRejected{
		Code:          code,
		Reason:        fmt.Sprintf(format, args...),
		Resolved:      resolved,
		Refusals:      kept,
		RefusalCounts: counts,
	}
}

// boundRefusals truncates the per-code sample while keeping the per-code totals complete.
//
// It de-duplicates by DEVICE first, defensively. `resolved = accepted + sum(counts)` is the
// invariant that makes a batch record self-auditing, and it is only true if each device is
// counted once — but half the refusals here come from ANOTHER SERVICE's fleet gate, and
// nothing in this process guarantees that service names a device once. A remote answer that
// repeated a token would quietly push the totals past `resolved`, which reads as a corrupt
// record rather than as the upstream slip it is.
//
// The output is sorted by code so the stored JSON is deterministic: unsorted, it came out in
// Go map-iteration order, so two identical batches produced different bytes and any diff of
// the record was noise.
func boundRefusals(refusals []BatchDeviceRefusal) ([]BatchDeviceRefusal, []RefusalCount) {
	counts := make(map[RejectionCode]int)
	seen := make(map[string]struct{}, len(refusals))
	kept := make([]BatchDeviceRefusal, 0, len(refusals))
	for _, r := range refusals {
		if _, already := seen[r.DeviceToken]; already {
			continue
		}
		seen[r.DeviceToken] = struct{}{}
		counts[r.Code]++
		if counts[r.Code] <= maxStoredRefusalsPerCode {
			kept = append(kept, r)
		}
	}
	ordered := make([]RefusalCount, 0, len(counts))
	for code, count := range counts {
		ordered = append(ordered, RefusalCount{Code: code, Count: count})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Code < ordered[j].Code })
	return kept, ordered
}
