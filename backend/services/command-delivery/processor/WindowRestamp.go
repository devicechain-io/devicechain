// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-command-delivery/config"
	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
)

// windowRestamper is the slice of the model API the re-stamp pass uses, declared at the
// point of use rather than added to model.CommandDeliveryApi (the choice DeadLetterWriteback
// documents): a test double of the delivery API that knows nothing about windows then keeps
// compiling, and the pass quietly does nothing for it.
type windowRestamper interface {
	WaitingWindows(ctx context.Context, after model.WaitingWindow, limit int) ([]model.WaitingWindow, error)
	RestampWindow(ctx context.Context, windowToken string, opening model.WindowOpening, now time.Time) (int64, error)
}

// windowRepointer is the slice of the model API the re-point step uses. Separate from
// windowRestamper for the reason that one is separate from model.CommandDeliveryApi.
type windowRepointer interface {
	WaitingCommandKeys(ctx context.Context, windowToken string) ([]model.WaitingCommandKey, error)
	RepointWindow(ctx context.Context, from, commandKey string, deviceTokens []string,
		hold *model.DispatchHold) (int64, error)
}

// repointChunk is how many devices one ownership question names: device-management's own bound
// on commandDispatchSchedule.
const repointChunk = 2000

// repointWaiting moves the commands stamped from one window whose device TYPE is no longer on it:
// onto the window the type is on now, or out of any window when the type has none (or no longer
// gates the command). It runs before the window's own re-stamp, so what is left on the window
// afterwards is exactly the commands whose type still uses it.
//
// 🔴 WHY THE PASS RESOLVES THROUGH THE TYPE, AND NOT AN EVENT FROM UpdateDeviceType. A stamp
// records the window the type was on at ENQUEUE, but the operator's intent is the type's window:
// moving a type off a slot because that slot is no longer safe must move what is already waiting,
// or the disruptive commands queued before the move still run in the slot just abandoned. An event
// would carry the change sooner, but it is the trade restampWindows already makes for window
// edits — a new subject, grant, consumer and dead-letter path for a fact that changes a few times
// a year — and a lost event would leave the rows on the old window for good, where this pass
// converges within its interval regardless. The cost is one ownership question per (window,
// command key) and chunk of waiting devices per pass, which the owner answers per device TYPE.
//
// Fail-closed as the rest of the pass: an error leaves every unmoved row as stamped, and a device
// the owner decided nothing about (disabled, gone) is left as stamped too.
func (cproc *CommandDeliveryProcessor) repointWaiting(ctx context.Context, repointer windowRepointer,
	assignments model.WindowAssignmentResolver, window string) (int64, error) {
	keys, err := repointer.WaitingCommandKeys(ctx, window)
	if err != nil {
		return 0, err
	}
	devicesByKey := make(map[string][]string)
	var names []string
	for _, k := range keys {
		if _, seen := devicesByKey[k.Name]; !seen {
			names = append(names, k.Name)
		}
		devicesByKey[k.Name] = append(devicesByKey[k.Name], k.DeviceToken)
	}
	type target struct {
		window       string
		after, until time.Time
	}
	var moved int64
	for _, name := range names {
		devices := devicesByKey[name]
		for start := 0; start < len(devices); start += repointChunk {
			chunk := devices[start:min(start+repointChunk, len(devices))]
			holds, ungated, err := assignments.CurrentWindows(ctx, chunk, name)
			if err != nil {
				return moved, err
			}
			// The devices that left this window, grouped by the opening they now belong to: one
			// write per distinct (window, opening), which is one per device type.
			moving := make(map[target][]string)
			var order []target
			for _, h := range holds {
				if h.WindowToken == window {
					continue // still on this window: the window's own re-stamp handles it
				}
				t := target{h.WindowToken, h.DispatchAfter, h.DispatchUntil}
				if _, seen := moving[t]; !seen {
					order = append(order, t)
				}
				moving[t] = append(moving[t], h.DeviceToken)
			}
			for _, t := range order {
				n, err := repointer.RepointWindow(ctx, window, name, moving[t], &model.DispatchHold{
					WindowToken: t.window, DispatchAfter: t.after, DispatchUntil: t.until})
				moved += n
				if err != nil {
					return moved, err
				}
			}
			if len(ungated) > 0 {
				n, err := repointer.RepointWindow(ctx, window, name, ungated, nil)
				moved += n
				if err != nil {
					return moved, err
				}
			}
		}
	}
	return moved, nil
}

// windowRestampInterval is the minimum spacing of the pass.
func (cproc *CommandDeliveryProcessor) windowRestampInterval() time.Duration {
	if cproc.windowInterval > 0 {
		return cproc.windowInterval
	}
	return config.WindowRestampInterval * time.Second
}

// restampWindows moves commands waiting on a maintenance window onto that window's CURRENT
// schedule, after first moving those whose device type is no longer on the window at all onto the
// type's current window, or out of any (repointWaiting). It runs inside the sweep's leader lock,
// throttled to windowRestampInterval.
//
// 🔴 WHY A POLL, AND WHY HERE. Command delivery learns nothing from device-management by
// event: every cross-service fact it uses (the enqueue verdict, presence, group membership)
// is a synchronous read, and its safety nets (hold reconcile, stranded reconcile) are
// leader-locked passes that re-ask the owner. A window edit is rare, so an event stream —
// a new subject, grant, consumer and dead-letter path to deliver one fact a few times a
// year — would cost more than the minute of latency a poll adds. The poll also repairs what
// an event could not: a command whose window CLOSED again before its device came back
// (see model.Api.RestampWindow), which no edit ever announces.
//
// Fail-closed in the direction that matters. When device-management cannot be reached the
// rows are LEFT as they are, still held, never released: a disruptive command does not
// become dispatchable because the pass could not check on it.
func (cproc *CommandDeliveryProcessor) restampWindows(ctx context.Context) error {
	restamper, ok := cproc.Api.(windowRestamper)
	if !ok || cproc.WindowOpenings == nil {
		return nil
	}
	if !cproc.lastWindowRestamp.IsZero() && time.Since(cproc.lastWindowRestamp) < cproc.windowRestampInterval() {
		return nil
	}
	cproc.lastWindowRestamp = time.Now()

	// 🔴 A SANCTIONED BYPASS OF TENANT ISOLATION: the discovery read crosses tenants to find
	// which (tenant, window) pairs have waiting commands, exactly as the delivery sweep's
	// pending read does. It selects only the pair; every write below is made under that
	// pair's own tenant.
	pairs, err := restamper.WaitingWindows(core.WithSystemContext(ctx), cproc.windowCursor, model.WaitingWindowsPage)
	if err != nil {
		return fmt.Errorf("listing windows with waiting commands: %w", err)
	}
	// A full page means there may be more: the next pass resumes after the last pair. A
	// short page means the end was reached, so the next pass starts from the beginning.
	if len(pairs) >= model.WaitingWindowsPage {
		cproc.windowCursor = pairs[len(pairs)-1]
	} else {
		cproc.windowCursor = model.WaitingWindow{}
	}
	var firstErr error
	for _, pair := range pairs {
		if cproc.tenantDeleted(pair.TenantId) {
			continue
		}
		tenantCtx := core.WithTenant(ctx, pair.TenantId)
		if repointer, ok := cproc.Api.(windowRepointer); ok {
			if assignments, ok := cproc.WindowOpenings.(model.WindowAssignmentResolver); ok {
				moved, err := cproc.repointWaiting(tenantCtx, repointer, assignments, pair.WindowToken)
				if moved > 0 {
					incr(cproc.WindowRestamps, float64(moved))
					log.Info().Str("tenant", pair.TenantId).Str("window", pair.WindowToken).Int64("commands", moved).
						Msg("Moved waiting commands off a maintenance window their device type no longer uses.")
				}
				if err != nil {
					// Not fatal to the window's own re-stamp: the rows not moved are still on this
					// window, and its schedule still applies to them.
					log.Warn().Err(err).Str("tenant", pair.TenantId).Str("window", pair.WindowToken).
						Msg("Could not check which window waiting commands' device types use now; they stay on the window they were stamped with.")
					if firstErr == nil {
						firstErr = err
					}
				}
			}
		}
		now := time.Now()
		opening, err := cproc.WindowOpenings.WindowOpening(tenantCtx, pair.WindowToken, now)
		if err != nil {
			log.Warn().Err(err).Str("tenant", pair.TenantId).Str("window", pair.WindowToken).
				Msg("Could not read a maintenance window; its waiting commands stay held as stamped.")
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		moved, err := restamper.RestampWindow(tenantCtx, pair.WindowToken, opening, now)
		if err != nil {
			log.Error().Err(err).Str("tenant", pair.TenantId).Str("window", pair.WindowToken).
				Msg("Could not re-stamp commands waiting on a maintenance window.")
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if moved > 0 {
			incr(cproc.WindowRestamps, float64(moved))
			log.Info().Str("tenant", pair.TenantId).Str("window", pair.WindowToken).Int64("commands", moved).
				Msg("Moved commands waiting on a maintenance window to its current schedule.")
		}
	}
	if firstErr != nil {
		return fmt.Errorf("re-stamping windows: %v: %w", firstErr, core.ErrPassPartial)
	}
	return nil
}
