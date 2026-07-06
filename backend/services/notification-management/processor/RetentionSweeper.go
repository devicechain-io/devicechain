// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/rs/zerolog/log"
)

// RetentionSweeper periodically prunes cleared per-alarm NotificationState rows
// (ADR-017 N.C). Once an alarm clears its escalation is settled, so its state row is
// only kept for a grace window; without pruning, one permanent row per alarm-ever-
// raised would asymptotically turn the per-alarm state into an alarm index in the
// wrong service (the ADR-041 history-home concern). This is the maintenance backstop
// that keeps the table bounded.
//
// It is cross-tenant maintenance, so it lists tenants under a system context and then
// prunes each under that tenant's context (so the rdb tenant-scope predicate keeps
// every delete tenant-local) — the same shape as the event-management anchor sweep.
type RetentionSweeper struct {
	Microservice *core.Microservice
	Api          *model.Api
	retention    time.Duration
	interval     time.Duration

	procCtx    context.Context
	procCancel context.CancelFunc
	wg         sync.WaitGroup
	lifecycle  core.LifecycleManager
}

// NewRetentionSweeper builds the sweep. retention is how long a cleared row is kept;
// interval is the tick period.
func NewRetentionSweeper(ms *core.Microservice, api *model.Api, retention, interval time.Duration,
	callbacks core.LifecycleCallbacks) *RetentionSweeper {
	s := &RetentionSweeper{
		Microservice: ms, Api: api, retention: retention, interval: interval,
	}
	s.lifecycle = core.NewLifecycleManager(fmt.Sprintf("%s-retention-sweep", ms.FunctionalArea), s, callbacks)
	return s
}

func (s *RetentionSweeper) Initialize(ctx context.Context) error {
	return s.lifecycle.Initialize(ctx)
}

func (s *RetentionSweeper) ExecuteInitialize(ctx context.Context) error {
	s.procCtx, s.procCancel = context.WithCancel(ctx)
	return nil
}

func (s *RetentionSweeper) Start(ctx context.Context) error {
	return s.lifecycle.Start(ctx)
}

func (s *RetentionSweeper) ExecuteStart(context.Context) error {
	s.wg.Add(1)
	go s.loop()
	return nil
}

func (s *RetentionSweeper) loop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.procCtx.Done():
			return
		case <-ticker.C:
			s.runOnce(s.procCtx)
		}
	}
}

func (s *RetentionSweeper) Stop(ctx context.Context) error {
	return s.lifecycle.Stop(ctx)
}

func (s *RetentionSweeper) ExecuteStop(context.Context) error {
	if s.procCancel != nil {
		s.procCancel()
	}
	s.wg.Wait()
	return nil
}

func (s *RetentionSweeper) Terminate(ctx context.Context) error {
	return s.lifecycle.Terminate(ctx)
}

func (s *RetentionSweeper) ExecuteTerminate(context.Context) error {
	return nil
}

// runOnce prunes every tenant's cleared state rows older than the retention window.
func (s *RetentionSweeper) runOnce(ctx context.Context) {
	before := time.Now().Add(-s.retention)
	tenants, err := s.Api.DistinctStateTenants(core.WithSystemContext(ctx))
	if err != nil {
		log.Error().Err(err).Msg("Retention sweep: failed to list tenants")
		return
	}
	for _, tenant := range tenants {
		select {
		case <-ctx.Done():
			return
		default:
		}
		removed, err := s.Api.PruneClearedStates(core.WithTenant(ctx, tenant), before)
		if err != nil {
			log.Error().Err(err).Str("tenant", tenant).Msg("Retention sweep: prune failed")
			continue
		}
		if removed > 0 {
			log.Info().Str("tenant", tenant).Int64("statesPruned", removed).
				Msg("Retention sweep pruned cleared notification state")
		}
	}
}
