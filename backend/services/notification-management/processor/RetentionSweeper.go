// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/rs/zerolog/log"
)

// RetentionSweeper periodically prunes RESOLVED per-alarm NotificationState rows
// (ADR-017 N.C). Once an alarm is acknowledged or cleared its escalation is settled, so
// its state row is only kept for a grace window; without pruning, one permanent row per
// alarm-ever-raised would asymptotically turn the per-alarm state into an alarm index in
// the wrong service (the ADR-041 history-home concern). This is the maintenance backstop
// that keeps the table bounded.
//
// Resolved means EITHER terminal stamp, not just cleared. A tombstone row is written for
// every acknowledged or cleared alarm in every tenant — including tenants with no
// notification policies, since the row closes an ordering race rather than recording a
// page — so a sweep that only recognized cleared_at left every acknowledged-but-never-
// cleared alarm in the table for the life of the instance. See Api.PruneResolvedStates.
//
// It is cross-tenant maintenance, so it lists tenants under a system context and then
// prunes each under that tenant's context (so the rdb tenant-scope predicate keeps
// every delete tenant-local) — the same shape as the event-management anchor sweep.
type RetentionSweeper struct {
	// The embedded task supplies the lifecycle octet, the schedule and the pass metrics.
	*core.PeriodicTask

	Microservice *core.Microservice
	Api          *model.Api
	retention    time.Duration
}

// NewRetentionSweeper builds the sweep. retention is how long a cleared row is kept;
// interval is the tick period.
func NewRetentionSweeper(ms *core.Microservice, api *model.Api, retention, interval time.Duration,
	callbacks core.LifecycleCallbacks) *RetentionSweeper {
	s := &RetentionSweeper{Microservice: ms, Api: api, retention: retention}
	s.PeriodicTask = core.NewPeriodicTask(ms.FunctionalArea, "retention-sweep", interval,
		s.runOnce, callbacks, core.WithPassMetrics(ms.NewPeriodicTaskMetrics("retention_sweep")))
	return s
}

// runOnce prunes every tenant's cleared state rows older than the retention window.
func (s *RetentionSweeper) runOnce(ctx context.Context) error {
	before := time.Now().Add(-s.retention)
	tenants, err := s.Api.DistinctStateTenants(core.WithSystemContext(ctx))
	if err != nil {
		return fmt.Errorf("listing tenants with notification state: %w", err)
	}
	var visited, failed int
	var lastErr error
	for _, tenant := range tenants {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		visited++
		removed, err := s.Api.PruneResolvedStates(core.WithTenant(ctx, tenant), before)
		if err != nil {
			log.Error().Err(err).Str("tenant", tenant).Msg("Retention sweep: prune failed")
			failed++
			lastErr = err
			continue
		}
		if removed > 0 {
			log.Info().Str("tenant", tenant).Int64("statesPruned", removed).
				Msg("Retention sweep pruned cleared notification state")
		}
	}
	return core.PassResult(visited, failed, lastErr)
}
