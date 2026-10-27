// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// DispatchHold binds one device's command to an opening of its device type's maintenance
// window WindowToken: it may be claimed only in [DispatchAfter, DispatchUntil). DispatchAfter
// is already past when the window was open at enqueue.
type DispatchHold struct {
	DeviceToken   string
	WindowToken   string
	DispatchAfter time.Time
	DispatchUntil time.Time
}

// DispatchScheduler asks the owner of the device types and their maintenance windows
// (device-management) when a command may START, for one device or many.
//
// It answers with HOLDS (start only inside this opening, which may be the one open now) and
// REFUSALS (the window opens only after the command expires, so accepting it could only end
// in expiry). A device in neither list is not window-gated. An error means the schedule could not be determined and the
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

// CommandImpactResolver asks the same owner, through the same question the window gate asks, what
// impact (NORMAL, DISRUPTIVE or DESTRUCTIVE) the profile gives a command on a device's type.
//
// The impact is NOT stored on the command row: enqueue resolves it only to decide the window, and
// keeps the hold. A transport adapter that gates on impact itself therefore gets it stamped at
// DISPATCH, from here. An error means the impact could not be decided, including a device the
// owner decided nothing about; the caller must leave the impact off rather than guess NORMAL.
type CommandImpactResolver interface {
	CommandImpact(ctx context.Context, deviceToken, commandKey string) (string, error)
}

// WindowOpening is the owner's current answer for one maintenance window.
type WindowOpening struct {
	// Found is false when the window no longer exists.
	Found bool
	// OpenNow reports whether the window is open at the instant asked about.
	OpenNow bool
	// DispatchAfter is the instant asked about when open, else the next opening.
	DispatchAfter time.Time
	// Closes is when the opening DispatchAfter belongs to ends.
	Closes time.Time
}

// WindowOpeningResolver reads a maintenance window's current opening, for the pass that
// re-stamps waiting commands after the window was edited.
type WindowOpeningResolver interface {
	WindowOpening(ctx context.Context, windowToken string, at time.Time) (WindowOpening, error)
}

// WindowAssignmentResolver asks the owner which maintenance window each device's TYPE is on NOW,
// for a command already waiting — the question the re-stamp pass asks so that a command follows
// its device type when the type is re-pointed to another window or unassigned.
//
// holds names the devices the command must still wait for, each with the window and opening it
// should be bound to (which may be a different window from the one it was stamped with). ungated
// names the devices whose type has no maintenance window any more. undefined names the devices
// whose type's current profile version no longer defines the command at all. A device in NONE of
// the lists is one the owner decided nothing about (it is disabled, or gone), and the caller must
// leave its rows exactly as stamped: reading that silence as "ungated" would release a disabled
// device's disruptive commands to go out outside any window once it is enabled. An error means
// nothing was decided, and the rows likewise stay as stamped.
//
// 🔴 THE QUESTION IS ASKED ABOUT A COMMAND THAT IS ALREADY HELD, AND THE ANSWER FOLLOWS THE
// TYPE'S WINDOW, NOT THE COMMAND'S CURRENT IMPACT. A profile version that now marks the command
// NORMAL does not make it ungated: what was queued as disruptive keeps waiting for the window.
// Only an operator changing the window (re-pointing the type, or unassigning its window)
// releases it. A command the profile no longer defines is never ungated either: it is
// undefined, and the pass fails it rather than send a command the device's profile no longer
// describes.
type WindowAssignmentResolver interface {
	CurrentWindows(ctx context.Context, deviceTokens []string, commandKey string) (holds []DispatchHold,
		ungated []string, undefined []string, err error)
}

// CommandUndefinedReason is the fixed sentence written into a command's Error column when the
// re-stamp pass fails a command that was waiting for a maintenance window and that its device's
// profile no longer defines. Like UndispatchableReason it is fixed rather than interpolated, so
// it identifies this failure exactly; the row's outcome is NOT_DELIVERED, because it never left
// the platform.
const CommandUndefinedReason = "Command no longer defined by this device's profile. It was waiting " +
	"for a maintenance window and was never sent."

// FailUndefinedWaiting drives the undelivered commands of the named devices and command key that
// are stamped from window `from` to FAILED (outcome NOT_DELIVERED, error CommandUndefinedReason),
// because the profile version their device type is on now no longer defines the command. It
// reports how many rows it failed. Called under the window's tenant.
//
// 🔑 FAILED, NOT RELEASED AND NOT LEFT WAITING. Releasing would send a command the device's
// profile no longer describes, outside the window it was queued to wait for; leaving it would
// send it at the next opening all the same. Waiting cannot make the command valid again, so the
// honest terminal is reached now, with a reason the operator can read: the stance
// MarkUndeliverable takes for a transport with no command path.
//
// Predicated on the undelivered statuses like RepointWindow, so a row claimed meanwhile is not
// touched and the conditional UPDATE composes with a racing claim.
func (api *Api) FailUndefinedWaiting(ctx context.Context, from, commandKey string,
	deviceTokens []string) (int64, error) {
	var failed int64
	for start := 0; start < len(deviceTokens); start += gateChunk {
		chunk := deviceTokens[start:min(start+gateChunk, len(deviceTokens))]
		res := api.RDB.DB(ctx).Model(&Command{}).
			Where("window_token = ? AND name = ? AND status IN ?", from, commandKey, claimableStatusStrings()).
			Where("device_token IN ?", chunk).
			Updates(terminalUpdate(CommandFailed, OutcomeNotDelivered, map[string]any{
				"error": sql.NullString{String: CommandUndefinedReason, Valid: true},
			}))
		if res.Error != nil {
			return failed, res.Error
		}
		failed += res.RowsAffected
	}
	return failed, nil
}

// WaitingCommandKey is one (device, command key) with undelivered commands stamped from a window.
type WaitingCommandKey struct {
	DeviceToken string
	Name        string
}

// WaitingCommandKeys lists the distinct (device, command key) pairs with undelivered commands
// stamped from one window, in the context's tenant. The owner's answer is a function of the device
// (through its type) and the command key, so this is the whole question the re-point step asks.
func (api *Api) WaitingCommandKeys(ctx context.Context, windowToken string) ([]WaitingCommandKey, error) {
	found := make([]WaitingCommandKey, 0)
	err := api.RDB.DB(ctx).Model(&Command{}).
		Select("device_token, name").
		Where("window_token = ? AND status IN ?", windowToken, claimableStatusStrings()).
		Group("device_token, name").
		Order("device_token, name").
		Scan(&found).Error
	if err != nil {
		return nil, err
	}
	return found, nil
}

// RepointWindow moves the undelivered commands of the named devices and command key that are
// stamped from window `from` onto `hold` — another window and its current opening — or, with a nil
// hold, clears the hold (both bounds and the token) because the device's type no longer gates the
// command. It reports how many rows it changed. Called under the window's tenant.
//
// Like RestampWindow it gates only the START: the predicate names the undelivered statuses, so a
// row claimed meanwhile is not touched, and the conditional UPDATE composes with a racing claim.
func (api *Api) RepointWindow(ctx context.Context, from, commandKey string, deviceTokens []string,
	hold *DispatchHold) (int64, error) {
	set := map[string]any{"dispatch_after": nil, "dispatch_until": nil, "window_token": nil}
	if hold != nil {
		set = map[string]any{"dispatch_after": hold.DispatchAfter, "dispatch_until": hold.DispatchUntil,
			"window_token": hold.WindowToken}
	}
	var moved int64
	for start := 0; start < len(deviceTokens); start += gateChunk {
		chunk := deviceTokens[start:min(start+gateChunk, len(deviceTokens))]
		res := api.RDB.DB(ctx).Model(&Command{}).
			Where("window_token = ? AND name = ? AND status IN ?", from, commandKey, claimableStatusStrings()).
			Where("device_token IN ?", chunk).
			Updates(set)
		if res.Error != nil {
			return moved, res.Error
		}
		moved += res.RowsAffected
	}
	return moved, nil
}

// dispatchableNowWhere is the ONE predicate for "this row's hold, if any, admits a start now",
// shared by every query that selects work to dispatch and every conditional UPDATE that claims
// it: the opening has begun AND has not yet closed.
//
// 🔴 IT IS A FUNCTION RATHER THAN A STRING LITERAL IN EACH QUERY because the residual risk of
// this design is a future query that forgets it. The three claims (MarkSent, MarkSentByToken,
// ConfirmDispatch's rotation) and the three reads that feed them (PendingCommands,
// QueuedCommandsForDevice, DrainableCommands) all call this, and the wiring test drives
// every dispatch road against a held row. NULL is "no hold": the ORs spell that out instead
// of leaning on three-valued logic, which would drop the row from both sides.
//
// 🔴 THE UPPER BOUND IS WHAT MAKES THE GATE FAIL CLOSED. Without it a row whose opening passed
// unclaimed stays claimable until the re-stamp pass rolls it on — a minute in the healthy
// case, and for as long as device-management is unreachable otherwise — so the pass would be
// load-bearing for safety. With it the pass only decides WHEN a row may next go, never
// WHETHER a row may go between windows.
func dispatchableNowWhere(now time.Time) (string, []any) {
	return "(dispatch_after IS NULL OR dispatch_after <= ?) AND (dispatch_until IS NULL OR dispatch_until > ?)",
		[]any{now, now}
}

// WaitingWindowsPage is how many (tenant, window) pairs one re-stamp pass handles.
const WaitingWindowsPage = 500

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
// Bounded by limit and paged by a keyset cursor: it returns the pairs strictly after
// `after` in (tenant, window) order, so a caller that finds a full page resumes from its
// last pair next time instead of re-reading the same first page forever. With dispatch_until
// a pair that is never reached is not a safety problem (its rows cannot go out between
// windows), but its commands would never go out at all.
func (api *Api) WaitingWindows(ctx context.Context, after WaitingWindow, limit int) ([]WaitingWindow, error) {
	if limit <= 0 {
		limit = WaitingWindowsPage
	}
	found := make([]WaitingWindow, 0)
	db := api.RDB.DB(ctx).Model(&Command{}).
		Select("tenant_id, window_token").
		Where("window_token IS NOT NULL AND status IN ?", claimableStatusStrings())
	if after != (WaitingWindow{}) {
		db = db.Where("(tenant_id > ? OR (tenant_id = ? AND window_token > ?))",
			after.TenantId, after.TenantId, after.WindowToken)
	}
	err := db.Group("tenant_id, window_token").
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
//   - window gone: the hold is cleared (both bounds and the token), and the commands dispatch
//     as ordinary ones. Deleting a window that device types still use is refused upstream,
//     so this is a window removed after its last type let go of it. (A type that was
//     UNASSIGNED, or re-pointed to another window, does not make its old window gone: the
//     window still exists for every other type using it. That case is RepointWindow's, which
//     the pass runs first, through the device type's current assignment.)
//   - window open now: every row is bound to the current opening — a future start is brought
//     forward to now, and the close is set to this opening's. That releases a row that was
//     waiting for it and re-admits one whose earlier opening passed unclaimed.
//   - window closed: every row is bound to the next opening. A row whose opening passed
//     unclaimed (its device was absent) was already unclaimable, because its close is past;
//     this is what moves it on, so it goes out in the next window rather than never.
//
// The window gates the START, so a command already claimed is not touched (the predicate
// names the three undelivered statuses; SENT is not among them). Rows already on the target
// opening are left alone, so a pass over an unchanged window writes nothing.
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
		return update("1 = 1", nil,
			map[string]any{"dispatch_after": nil, "dispatch_until": nil, "window_token": nil})
	case opening.OpenNow:
		// A start already past is kept (any past start admits, and it records when the row
		// became sendable); only a future one is pulled forward. The close must match.
		return update("(dispatch_after IS NULL OR dispatch_after > ? OR dispatch_until IS NULL OR dispatch_until <> ?)",
			[]any{now, opening.Closes},
			map[string]any{
				"dispatch_after": gorm.Expr(
					"CASE WHEN dispatch_after IS NULL OR dispatch_after > ? THEN ? ELSE dispatch_after END", now, now),
				"dispatch_until": opening.Closes,
			})
	default:
		return update("(dispatch_after IS NULL OR dispatch_after <> ? OR dispatch_until IS NULL OR dispatch_until <> ?)",
			[]any{opening.DispatchAfter, opening.Closes},
			map[string]any{"dispatch_after": opening.DispatchAfter, "dispatch_until": opening.Closes})
	}
}

// windowHoldFor asks the scheduler whether a command to one device is gated by a maintenance
// window. It returns the hold, or nil when the command is not gated (or no scheduler is wired).
//
// A refusal comes back as *EnqueueRejected carrying the owner's code; a failure to decide
// is a plain, sanitized error so the caller fails closed without learning topology.
func (api *Api) windowHoldFor(ctx context.Context, deviceToken, commandKey string,
	expiresAt sql.NullTime) (*DispatchHold, error) {
	if api.DispatchScheduler == nil {
		return nil, nil
	}
	holds, refusals, err := api.DispatchScheduler.ScheduleDispatch(ctx, []string{deviceToken}, commandKey, expiresAt)
	if err != nil {
		log.Error().Err(err).Str("deviceToken", deviceToken).Str("command", commandKey).
			Msg("Maintenance window check failed; refusing enqueue.")
		return nil, fmt.Errorf("cannot enqueue command: validation is unavailable")
	}
	for _, refusal := range refusals {
		if refusal.DeviceToken == deviceToken {
			return nil, rejected(refusal.Code, "cannot enqueue command: %s", refusal.Reason)
		}
	}
	for _, hold := range holds {
		if hold.DeviceToken == deviceToken {
			h := hold
			return &h, nil
		}
	}
	return nil, nil
}

// stampHold writes a hold's bounds and window onto a command about to be inserted.
func stampHold(cmd *Command, hold DispatchHold) {
	cmd.DispatchAfter = sql.NullTime{Time: hold.DispatchAfter, Valid: true}
	cmd.DispatchUntil = sql.NullTime{Time: hold.DispatchUntil, Valid: true}
	cmd.WindowToken = sql.NullString{String: hold.WindowToken, Valid: true}
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
