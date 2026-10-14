// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

// DispatchHold says one device's command must not be claimed before DispatchAfter, because
// the device type's maintenance window WindowToken is closed until then.
type DispatchHold struct {
	DeviceToken   string
	WindowToken   string
	DispatchAfter time.Time
}

// DispatchScheduler asks the owner of the device types and their maintenance windows
// (device-management) when a command may START, for one device or many.
//
// It answers with HOLDS (wait until this instant) and REFUSALS (the window opens only after
// the command expires, so accepting it could only end in expiry). A device in neither list
// may be dispatched immediately. An error means the schedule could not be determined and the
// caller must fail closed: reading it as "no hold" would send a disruptive command outside
// its window exactly when the dependency is down.
//
// Like CommandEnqueueValidator it is dependency-inverted, so this package depends on the
// question and never on the sync-call machinery that answers it. A nil scheduler skips the
// window gate, the way a nil validator skips the vocabulary gate; the service logs that mode
// at startup.
type DispatchScheduler interface {
	ScheduleDispatch(ctx context.Context, deviceTokens []string, commandKey string,
		expiresAt sql.NullTime) ([]DispatchHold, []BatchDeviceRefusal, error)
}

// WindowOpening is the owner's current answer for one maintenance window.
type WindowOpening struct {
	// Found is false when the window no longer exists.
	Found bool
	// OpenNow reports whether the window is open at the instant asked about.
	OpenNow bool
	// DispatchAfter is the instant asked about when open, else the next opening.
	DispatchAfter time.Time
}

// WindowOpeningResolver reads a maintenance window's current opening, for the pass that
// re-stamps waiting commands after the window was edited.
type WindowOpeningResolver interface {
	WindowOpening(ctx context.Context, windowToken string, at time.Time) (WindowOpening, error)
}

// dispatchableNowWhere is the ONE predicate for "this row's hold, if any, has lapsed", shared
// by every query that selects work to dispatch and every conditional UPDATE that claims it.
//
// 🔴 IT IS A FUNCTION RATHER THAN A STRING LITERAL IN EACH QUERY because the residual risk of
// this design is a future query that forgets it. The three claims (MarkSent, MarkSentByToken,
// ConfirmDispatch's rotation) and the three reads that feed them (PendingCommands,
// QueuedCommandsForDevice, DrainableCommands) all call this, and the wiring test drives
// every dispatch road against a held row. NULL is "no hold": the OR spells that out instead
// of leaning on three-valued logic, which would drop the row from both sides.
func dispatchableNowWhere(now time.Time) (string, []any) {
	return "(dispatch_after IS NULL OR dispatch_after <= ?)", []any{now}
}

// WaitingWindow is a (tenant, maintenance window) pair that still has undelivered commands
// stamped from it.
type WaitingWindow struct {
	TenantId    string
	WindowToken string
}

// WaitingWindows lists the distinct (tenant, window) pairs with undelivered stamped
// commands, for the re-stamp pass. It crosses tenants, so the caller passes a system
// context; each pair is then handled under its own tenant.
//
// Bounded by limit: a pass that finds more than it can handle takes the rest next time,
// ordered so the same pairs do not starve the others.
func (api *Api) WaitingWindows(ctx context.Context, limit int) ([]WaitingWindow, error) {
	if limit <= 0 {
		limit = 500
	}
	found := make([]WaitingWindow, 0)
	err := api.RDB.DB(ctx).Model(&Command{}).
		Select("tenant_id, window_token").
		Where("window_token IS NOT NULL AND status IN ?", claimableStatusStrings()).
		Group("tenant_id, window_token").
		Order("tenant_id, window_token").
		Limit(limit).
		Scan(&found).Error
	if err != nil {
		return nil, err
	}
	return found, nil
}

// RestampWindow moves the undelivered commands stamped from one maintenance window onto the
// window's CURRENT schedule, and reports how many rows it changed. Called under the window's
// tenant.
//
//   - window gone: the hold is cleared (the stamp and the token), and the commands dispatch
//     as ordinary ones. Deleting a window that device types still use is refused upstream,
//     so this is a type that was unassigned or a window removed after.
//   - window open now: a stamp in the future is brought forward to now.
//   - window closed: every stamp is set to the next opening, INCLUDING a stamp that is
//     already in the past. That is the roll-over: a command whose window opened while its
//     device was absent was never claimed, and without this the device's return a week
//     later would find an old, open-looking stamp and be sent the command outside any
//     window. The window gates the START, so a command already claimed is not touched (the
//     predicate names the three undelivered statuses; SENT is not among them).
//
// Each write is one conditional UPDATE, predicated on status, so it composes with a claim
// racing it: whichever lands first decides, and neither can clobber the other.
func (api *Api) RestampWindow(ctx context.Context, windowToken string, opening WindowOpening,
	now time.Time) (int64, error) {
	update := func(where string, args []any, set map[string]any) (int64, error) {
		res := api.RDB.DB(ctx).Model(&Command{}).
			Where("window_token = ? AND status IN ?", windowToken, claimableStatusStrings()).
			Where(where, args...).
			Updates(set)
		return res.RowsAffected, res.Error
	}
	switch {
	case !opening.Found:
		return update("dispatch_after IS NOT NULL", nil,
			map[string]any{"dispatch_after": nil, "window_token": nil})
	case opening.OpenNow:
		return update("dispatch_after > ?", []any{now}, map[string]any{"dispatch_after": now})
	default:
		return update("dispatch_after IS NOT NULL AND dispatch_after <> ?", []any{opening.DispatchAfter},
			map[string]any{"dispatch_after": opening.DispatchAfter})
	}
}

// windowHoldFor asks the scheduler whether a command to one device must wait for a
// maintenance window. It returns the instant to hold until and the window it came from, or
// the empty window token when the command may dispatch at once (or no scheduler is wired).
//
// A refusal comes back as *EnqueueRejected carrying the owner's code; a failure to decide
// is a plain, sanitized error so the caller fails closed without learning topology.
func (api *Api) windowHoldFor(ctx context.Context, deviceToken, commandKey string,
	expiresAt sql.NullTime) (time.Time, string, error) {
	if api.DispatchScheduler == nil {
		return time.Time{}, "", nil
	}
	holds, refusals, err := api.DispatchScheduler.ScheduleDispatch(ctx, []string{deviceToken}, commandKey, expiresAt)
	if err != nil {
		log.Error().Err(err).Str("deviceToken", deviceToken).Str("command", commandKey).
			Msg("Maintenance window check failed; refusing enqueue.")
		return time.Time{}, "", fmt.Errorf("cannot enqueue command: validation is unavailable")
	}
	for _, refusal := range refusals {
		if refusal.DeviceToken == deviceToken {
			return time.Time{}, "", rejected(refusal.Code, "cannot enqueue command: %s", refusal.Reason)
		}
	}
	for _, hold := range holds {
		if hold.DeviceToken == deviceToken {
			return hold.DispatchAfter, hold.WindowToken, nil
		}
	}
	return time.Time{}, "", nil
}

// scheduleBatchHolds is windowHoldFor for a batch: one scheduler call per chunk of devices,
// returning the holds keyed by device and the window refusals as batch refusals. With no
// scheduler wired it returns nothing, so a batch is exactly as it was.
//
// An error means the schedule could not be determined for at least one chunk; the whole
// batch fails closed (token unspent), the same stance validateBatchTargets takes.
func (api *Api) scheduleBatchHolds(ctx context.Context, tokens []string, commandKey string,
	expiresAt sql.NullTime) (map[string]DispatchHold, []BatchDeviceRefusal, error) {
	holds := make(map[string]DispatchHold)
	refusals := make([]BatchDeviceRefusal, 0)
	if api.DispatchScheduler == nil || len(tokens) == 0 {
		return holds, refusals, nil
	}
	for start := 0; start < len(tokens); start += batchValidationChunk {
		end := start + batchValidationChunk
		if end > len(tokens) {
			end = len(tokens)
		}
		chunkHolds, chunkRefusals, err := api.DispatchScheduler.ScheduleDispatch(ctx, tokens[start:end],
			commandKey, expiresAt)
		if err != nil {
			log.Error().Err(err).Str("command", commandKey).
				Msg("Maintenance window check failed; refusing batch.")
			return nil, nil, fmt.Errorf("cannot enqueue command batch: validation is unavailable")
		}
		for _, hold := range chunkHolds {
			holds[hold.DeviceToken] = hold
		}
		refusals = append(refusals, chunkRefusals...)
	}
	return holds, refusals, nil
}
