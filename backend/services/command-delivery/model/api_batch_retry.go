// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"
)

// maxRetriesListed bounds CommandBatch.retries: a batch can be retried by many scripts, and the
// list is a navigation aid rather than an inventory.
const maxRetriesListed = 100

// retryLookupChunk is how many device tokens go into one IN list when checking eligibility.
// Smaller than the 65,535-bind ceiling by a wide margin, so a full 10,000-device retry is ten
// cheap indexed reads instead of one that depends on the driver's limit.
const retryLookupChunk = 1000

// CommandBatchRetryRequest asks for a chosen subset of a batch's unsuccessful targets to be
// sent the SAME command again, as a new batch linked to the original.
//
// 🔴 THE COMMAND IS NOT IN THE REQUEST, AND THAT IS THE DESIGN. A retry means "the same
// operation to a subset of the same devices"; letting the caller restate name or payload would
// make "retry" a way to send a different command to a fleet while the record says it was a
// retry. The server reads both from the original.
type CommandBatchRetryRequest struct {
	// Token is the retry's OWN idempotency key. Replaying it returns the retry; it must not be
	// the original's.
	Token string
	// Of is the token of the batch being retried (which may itself be a retry).
	Of string
	// DeviceTokens are the devices to retry, in the order they are admitted if the retry is
	// only partially admitted.
	DeviceTokens []string
	// IncludeUnknown acknowledges that UNKNOWN-outcome targets may already have acted.
	IncludeUnknown bool
	AllowPartial   bool
	ExpiresAt      *string
	Metadata       *string
}

// retryPlan is the context decideCommandBatch needs to create a retry rather than a fresh batch.
type retryPlan struct {
	original       *CommandBatch
	includeUnknown bool
}

// RetryCommandBatch re-issues the original batch's command to a subset of its devices that did
// not succeed, as a new batch whose retry_of names the original.
//
// It is createCommandBatch with two differences, and everything else is shared on purpose: the
// command comes from the original, and the targets must be proven eligible against the original's
// stored per-command outcomes. The same enqueue gate, ceiling, reserve, lock and transaction
// apply, so a retry is refused for a disabled device or a destructive command exactly as a new
// batch is — a retry is not a back door around any of them.
//
// 🔑 IDEMPOTENCY RUNS BOTH WAYS. The retry has its own token: replaying it returns the retry and
// executes nothing. The original's token is untouched: createCommandBatch with it still returns
// the original and never re-executes, because the retry is a different batch row.
func (api *Api) RetryCommandBatch(ctx context.Context, request *CommandBatchRetryRequest) (*CommandBatch, error) {
	acct := batchAccounting{targetKind: targetKindLabel(BatchTargetDeviceList)}
	created, err := api.decideRetry(ctx, request, &acct)
	api.recordBatchOutcome(&acct, err)
	return created, err
}

func (api *Api) decideRetry(ctx context.Context, request *CommandBatchRetryRequest,
	acct *batchAccounting) (*CommandBatch, error) {
	// The retry's own token first, before the original is even loaded: a client whose request
	// timed out and retried must get its batch back even if the original has since been removed.
	if existing, replay, err := api.liveBatchByToken(ctx, request.Token); err != nil {
		return nil, err
	} else if replay {
		if existing.RetryOf.String != request.Of || !existing.RetryOf.Valid {
			return nil, rejected(RejectRetryTokenInUse,
				"token %q already names a batch that is not a retry of %q; a retry needs its own token",
				request.Token, request.Of)
		}
		acct.replay = true
		return existing, nil
	}

	if len(request.DeviceTokens) == 0 {
		return nil, rejected(RejectRetryNoTargets, "a retry must name at least one device")
	}
	originals, err := api.CommandBatchesByToken(ctx, []string{request.Of})
	if err != nil {
		return nil, err
	}
	if len(originals) == 0 {
		return nil, rejected(RejectRetryOriginalNotFound, "there is no batch %q to retry", request.Of)
	}
	original := originals[0]
	if request.Token == original.Token {
		return nil, rejected(RejectRetryTokenInUse,
			"the retry's token must differ from the batch it retries (%q)", original.Token)
	}

	var payload *string
	if original.Payload != nil {
		text := string(*original.Payload)
		payload = &text
	}
	devices := dedupeBatchTokens(request.DeviceTokens)
	plan := &retryPlan{original: original, includeUnknown: request.IncludeUnknown}
	// Cheap, indexed, and ahead of the remote gate: a request naming the wrong devices is
	// refused before anything is asked of device-management.
	if err := api.checkRetryEligibility(api.RDB.DB(ctx), plan, devices); err != nil {
		return nil, err
	}
	create := &CommandBatchCreateRequest{
		Token:        request.Token,
		Name:         original.Name,
		Payload:      payload,
		ExpiresAt:    request.ExpiresAt,
		Metadata:     request.Metadata,
		DeviceTokens: &devices,
		AllowPartial: request.AllowPartial,
	}
	return api.decideCommandBatch(ctx, create, acct, plan)
}

// retryTargetState is one device's standing in the original batch.
type retryTargetState struct {
	status  string
	outcome string
}

// checkRetryEligibility proves every named device may be retried, or refuses with the devices
// that may not be and why. It reads through db so the SAME check runs once cheaply before the
// remote gate and again inside the creating transaction (where a late response may have moved a
// row since the first read): the race costs a rejection, never a resend.
//
// A device is retryable when it is a member of the original — it has a command row there, or it
// is a refused device whose individual refusal the record holds in full — and
//
//   - its command is NOT_DELIVERED or REJECTED; or UNKNOWN with includeUnknown;
//   - it is not in flight, and did not SUCCEED;
//   - no retry of this same original already holds a live or successful command for it (retrying
//     twice from one parent would send twice; retry the retry instead).
//
// A refused device with no command row is retryable only when its refusal code's stored sample is
// complete (sample size equals the exact count): the record keeps at most 100 per code, so for a
// larger refusal set it cannot say whether a device outside the sample was a member.
func (api *Api) checkRetryEligibility(db *gorm.DB, plan *retryPlan, devices []string) error {
	states := make(map[string]retryTargetState, len(devices))
	for start := 0; start < len(devices); start += retryLookupChunk {
		end := min(start+retryLookupChunk, len(devices))
		var rows []Command
		if err := db.Model(&Command{}).
			Select("device_token", "status", "outcome").
			Where("batch_id = ? AND device_token IN ?", plan.original.ID, devices[start:end]).
			Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			states[row.DeviceToken] = retryTargetState{status: row.Status, outcome: row.Outcome.String}
		}
	}

	refusedOK, err := completeRefusals(plan.original)
	if err != nil {
		return err
	}

	// Devices a sibling retry already covers: live, or successful.
	covered := make(map[string]struct{})
	for start := 0; start < len(devices); start += retryLookupChunk {
		end := min(start+retryLookupChunk, len(devices))
		var rows []Command
		if err := db.Model(&Command{}).
			Select("device_token").
			Where("device_token IN ?", devices[start:end]).
			Where("(outcome IS NULL OR outcome = ?)", OutcomeSucceeded.String()).
			Where("batch_id IN (?)", db.Session(&gorm.Session{NewDB: true}).Model(&CommandBatch{}).
				Select("id").Where("retry_of = ?", plan.original.Token)).
			Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			covered[row.DeviceToken] = struct{}{}
		}
	}

	var bad []BatchDeviceRefusal
	first := RejectionCode("")
	flag := func(device string, code RejectionCode, reason string) {
		if first == "" || retryPrecedence(code) < retryPrecedence(first) {
			first = code
		}
		bad = append(bad, BatchDeviceRefusal{DeviceToken: device, Code: code, Reason: reason})
	}
	for _, device := range devices {
		state, hasRow := states[device]
		if !hasRow {
			if !refusedOK[device] {
				flag(device, RejectRetryNotAMember,
					"this device is not a member of the batch being retried, or its refusal was not "+
						"individually recorded")
			} else if _, dup := covered[device]; dup {
				flag(device, RejectRetryNotEligible, "an earlier retry of this batch already covers this device")
			}
			continue
		}
		if _, dup := covered[device]; dup {
			flag(device, RejectRetryNotEligible, "an earlier retry of this batch already covers this device")
			continue
		}
		if !CommandStatus(state.status).Terminal() {
			flag(device, RejectRetryNotEligible, "this device's command is still in flight")
			continue
		}
		switch CommandOutcome(state.outcome) {
		case OutcomeNotDelivered, OutcomeRejected:
		case OutcomeSucceeded:
			flag(device, RejectRetryNotEligible, "this device's command already succeeded")
		default:
			// UNKNOWN, and also a finished row that somehow carries no valid class: the
			// conservative reading, which asks for the acknowledgement rather than assuming safe.
			if !plan.includeUnknown {
				flag(device, RejectRetryUnknownNotAcknowledged,
					"this device's command may already have acted; set includeUnknown to retry it anyway")
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return batchRejection(first, bad, len(devices),
		"%d of %d named devices cannot be retried", len(bad), len(devices))
}

// retryPrecedence orders the retry refusals so the headline code is the most fundamental
// problem: a wrong device before an ineligible one before a missing acknowledgement.
func retryPrecedence(code RejectionCode) int {
	switch code {
	case RejectRetryNotAMember:
		return 0
	case RejectRetryNotEligible:
		return 1
	default:
		return 2
	}
}

// completeRefusals returns the devices the original refused and whose refusal the record holds
// in full: the stored sample for a code covers every device the exact count says was refused for
// it. Anything from a truncated code is absent, because the record cannot vouch for membership.
func completeRefusals(original *CommandBatch) (map[string]bool, error) {
	out := make(map[string]bool)
	if original.Refusals == nil || original.RefusalCounts == nil {
		return out, nil
	}
	var sample []BatchDeviceRefusal
	if err := json.Unmarshal(*original.Refusals, &sample); err != nil {
		return nil, fmt.Errorf("batch %q has an unreadable refusal record", original.Token)
	}
	var counts []RefusalCount
	if err := json.Unmarshal(*original.RefusalCounts, &counts); err != nil {
		return nil, fmt.Errorf("batch %q has an unreadable refusal record", original.Token)
	}
	total := make(map[RejectionCode]int, len(counts))
	for _, c := range counts {
		total[c.Code] = c.Count
	}
	held := make(map[RejectionCode]int, len(counts))
	for _, r := range sample {
		held[r.Code]++
	}
	for _, r := range sample {
		if held[r.Code] == total[r.Code] {
			out[r.DeviceToken] = true
		}
	}
	return out, nil
}

// RetriesOfBatch lists the tokens of the batches that retried the one named, oldest first.
func (api *Api) RetriesOfBatch(ctx context.Context, token string) ([]string, error) {
	var rows []CommandBatch
	if err := api.RDB.DB(ctx).Select("token").
		Where("retry_of = ?", token).
		Order("created_at ASC, token ASC").Limit(maxRetriesListed).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Token)
	}
	return out, nil
}

// retryOfValue is the nullable column value a retry batch stores.
func retryOfValue(plan *retryPlan) sql.NullString {
	if plan == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: plan.original.Token, Valid: true}
}
