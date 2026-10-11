// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

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
	api      *model.Api
	interval time.Duration
	page     int
	metrics  *UpdateReportsMetrics

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// sweepPage bounds how many attempts one pass examines. A backlog larger than a page is worked off
// across passes, oldest first.
const sweepPage = 200

// NewDeadlineSweeper builds the sweep. interval must be positive.
func NewDeadlineSweeper(api *model.Api, interval time.Duration, metrics *UpdateReportsMetrics) *DeadlineSweeper {
	if interval <= 0 {
		panic("processor.NewDeadlineSweeper: interval must be positive")
	}
	return &DeadlineSweeper{api: api, interval: interval, page: sweepPage, metrics: metrics}
}

// Start launches the loop. It is idempotent.
func (s *DeadlineSweeper) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// The first pass after a start is delayed by a jittered fraction of the interval.
		timer := time.NewTimer(jitter(s.interval))
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			s.Pass(ctx)
			timer.Reset(s.interval)
		}
	}()
}

// Stop ends the loop and waits for the pass in flight.
func (s *DeadlineSweeper) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// Pass runs one sweep and records what it did. Exported so a test (and an operator tool) can drive
// a pass without waiting for the timer.
func (s *DeadlineSweeper) Pass(ctx context.Context) model.SweepResult {
	res, err := s.api.SweepDeadlines(ctx, s.page)
	if err != nil {
		log.Error().Err(err).Msg("The update deadline sweep failed; it is retried at the next pass.")
		return res
	}
	if s.metrics != nil && s.metrics.expired != nil {
		s.metrics.expired.WithLabelValues(string(ota.StateTimedOut)).Add(float64(res.TimedOut))
		s.metrics.expired.WithLabelValues(string(ota.StateUnknown)).Add(float64(res.Unknown))
	}
	if res.TimedOut+res.Unknown > 0 {
		log.Info().Int("timedOut", res.TimedOut).Int("unknown", res.Unknown).Msg("Update attempts passed their deadline.")
	}
	return res
}

func jitter(d time.Duration) time.Duration {
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}
