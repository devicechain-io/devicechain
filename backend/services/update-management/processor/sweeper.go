// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/rs/zerolog/log"
)

// DeadlineSweeper moves update attempts whose device went quiet: TIMED_OUT before installation was
// reported started, UNKNOWN after. The decisions are the core state machine's (ota.Tick); this loop
// only decides when to ask.
//
// It runs on every replica with no leader. That is safe because each change is a compare-and-set on
// the attempt's version (model.Api.SweepDeadlines): two replicas that pick up the same attempt race
// to one outcome, and a device report that lands between the read and the write wins or loses
// cleanly. Replicas are staggered with jitter so they do not all read at once.
type DeadlineSweeper struct {
	api     *model.Api
	page    int
	metrics *UpdateReportsMetrics

	mu       sync.Mutex
	task     *core.PeriodicTask
	makeTask func() *core.PeriodicTask
}

// sweepPage bounds how many attempts one pass examines. A backlog larger than a page is worked off
// across passes, oldest first.
const sweepPage = 200

// NewDeadlineSweeper builds the sweep. interval must be positive. passMetrics are required and
// registered once by the caller on the initialize path: the sweep is built on every start.
func NewDeadlineSweeper(api *model.Api, interval time.Duration, metrics *UpdateReportsMetrics, passMetrics *core.PeriodicTaskMetrics) *DeadlineSweeper {
	if interval <= 0 {
		panic("processor.NewDeadlineSweeper: interval must be positive")
	}
	// Refused here rather than at the first Start: the task is built per start, and a nil here
	// would otherwise surface only when the service starts, not when it is wired.
	if passMetrics == nil {
		panic("processor.NewDeadlineSweeper: pass metrics are required")
	}
	s := &DeadlineSweeper{api: api, page: sweepPage, metrics: metrics}
	// A fresh lifecycle per start preserves the wrapper's ability to restart after Stop.
	s.makeTask = func() *core.PeriodicTask {
		return core.NewPeriodicTask("update-management", "deadline-sweep", interval,
			s.runOnce, core.NewNoOpLifecycleCallbacks(), passMetrics)
	}
	return s
}

// Start launches the loop. It is idempotent. It has the lifecycle-verb signature so the service's
// start and stop callbacks wire it as a component of their own (the start-stop symmetry guard
// pairs a verb called on a package-level variable, and stops at it rather than reading inside).
func (s *DeadlineSweeper) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task != nil {
		return nil
	}
	task := s.makeTask()
	if err := task.Initialize(ctx); err != nil {
		return err
	}
	if err := task.Start(ctx); err != nil {
		return err
	}
	s.task = task
	return nil
}

// Stop ends the loop and waits for the pass in flight.
func (s *DeadlineSweeper) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task == nil {
		return nil
	}
	err := s.task.Stop(ctx)
	s.task = nil
	return err
}

// runOnce is the scheduled pass. It returns the error Pass only logs, so the pass outcome is
// classified (and logged, rate-limited, by the periodic task) instead of counted as complete.
func (s *DeadlineSweeper) runOnce(ctx context.Context) error {
	if _, err := s.sweep(ctx); err != nil {
		return fmt.Errorf("the update deadline sweep failed: %w", err)
	}
	return nil
}

// Pass runs one sweep and records what it did. Exported so a test (and an operator tool) can drive
// a pass without waiting for the timer.
func (s *DeadlineSweeper) Pass(ctx context.Context) model.SweepResult {
	res, err := s.sweep(ctx)
	if err != nil {
		log.Error().Err(err).Msg("The update deadline sweep failed; it is retried at the next pass.")
	}
	return res
}

func (s *DeadlineSweeper) sweep(ctx context.Context) (model.SweepResult, error) {
	res, err := s.api.SweepDeadlines(ctx, s.page)
	if err != nil {
		return res, err
	}
	if s.metrics != nil && s.metrics.expired != nil {
		s.metrics.expired.WithLabelValues(string(ota.StateTimedOut)).Add(float64(res.TimedOut))
		s.metrics.expired.WithLabelValues(string(ota.StateUnknown)).Add(float64(res.Unknown))
	}
	if res.TimedOut+res.Unknown > 0 {
		log.Info().Int("timedOut", res.TimedOut).Int("unknown", res.Unknown).Msg("Update attempts passed their deadline.")
	}
	return res, nil
}
