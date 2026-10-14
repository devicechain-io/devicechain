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

// windowRestampInterval is the minimum spacing of the pass.
func (cproc *CommandDeliveryProcessor) windowRestampInterval() time.Duration {
	if cproc.windowInterval > 0 {
		return cproc.windowInterval
	}
	return config.WindowRestampInterval * time.Second
}

// restampWindows moves commands waiting on a maintenance window onto that window's CURRENT
// schedule. It runs inside the sweep's leader lock, throttled to windowRestampInterval.
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
