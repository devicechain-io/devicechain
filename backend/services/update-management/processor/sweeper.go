// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
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

// NewDeadlineSweeper builds the sweep. interval must be positive.
func NewDeadlineSweeper(api *model.Api, interval time.Duration, metrics *UpdateReportsMetrics, ms *core.Microservice) *DeadlineSweeper {
	if interval <= 0 {
		panic("processor.NewDeadlineSweeper: interval must be positive")
	}
	s := &DeadlineSweeper{api: api, page: sweepPage, metrics: metrics}
	opts := []core.PeriodicTaskOption{core.WithDetachedContext()}
	if ms != nil {
		opts = append(opts, core.WithPassMetrics(ms.NewPeriodicTaskMetrics("deadline_sweep")))
	}
	// A fresh lifecycle per start preserves the wrapper's ability to restart after Stop.
	s.makeTask = func() *core.PeriodicTask {
		return core.NewPeriodicTask("update-management", "deadline-sweep", interval,
			s.runOnce, core.NewNoOpLifecycleCallbacks(), opts...)
	}
	return s
}

// Start launches the loop. It is idempotent.
func (s *DeadlineSweeper) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task != nil {
		return
	}
	s.task = s.makeTask()
	_ = s.task.Initialize(ctx)
	_ = s.task.Start(ctx)
}

// Stop ends the loop and waits for the pass in flight.
func (s *DeadlineSweeper) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task != nil {
		_ = s.task.Stop(context.Background())
		s.task = nil
	}
}

func (s *DeadlineSweeper) runOnce(ctx context.Context) error {
	_, err := s.sweep(ctx)
	return err
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
