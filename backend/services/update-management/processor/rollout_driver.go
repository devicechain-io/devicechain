// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// RolloutInterval is how often each replica steps the RUNNING rollouts. Fixed, not configured: it
// bounds how long a freed slot stays empty, and a start or resume steps its rollout at once.
const RolloutInterval = 5 * time.Second

// RolloutMetrics are the rollout driver's instruments.
type RolloutMetrics struct {
	dispatches  *prometheus.CounterVec
	transitions *prometheus.CounterVec
	failures    *prometheus.CounterVec
}

// The stage label values of the failures counter.
const (
	rolloutStageStep = "step"
	rolloutStageList = "list"
)

// rolloutTransitionLabels is every value of the transitions counter's label, so each series exists
// at zero from startup.
var rolloutTransitionLabels = []string{"completed", "paused_failure_threshold", "paused_artifact_not_assignable"}

// NewRolloutMetrics registers the rollout instruments, once.
func NewRolloutMetrics(ms *core.Microservice) *RolloutMetrics {
	m := &RolloutMetrics{
		dispatches: ms.NewCounterVec("update_rollout_dispatches_total",
			"Devices a group rollout decided: dispatched (assigned the update, or adopted an assignment of the same "+
				"artifact), or skipped (the assignment plane refused the device: not found, disabled, incompatible, "+
				"busy with another update).",
			[]string{"result"}),
		transitions: ms.NewCounterVec("update_rollout_transitions_total",
			"Group rollouts the driver moved on its own: completed, or paused because the failure threshold was "+
				"reached or the artifact stopped being assignable (retired, or its signer revoked).",
			[]string{"to"}),
		failures: ms.NewCounterVec("update_rollout_step_failures_total",
			"Rollout driver failures left for the next pass, by stage: step (one rollout's step failed: a database "+
				"error, an unreadable compatible-profiles document on the artifact, or every device lookup failed "+
				"because device-management is unreachable or not configured for update-management) or list (the "+
				"query for the running rollouts failed, so no rollout was stepped). A steady rise means rollouts "+
				"are not advancing.",
			[]string{"stage"}),
	}
	for _, st := range []string{rolloutStageStep, rolloutStageList} {
		m.failures.WithLabelValues(st).Add(0)
	}
	for _, r := range []string{"dispatched", "skipped"} {
		m.dispatches.WithLabelValues(r).Add(0)
	}
	for _, t := range rolloutTransitionLabels {
		m.transitions.WithLabelValues(t).Add(0)
	}
	return m
}

// ObserveListFailure records a failed query for the running rollouts. It is the model.Api
// OnRolloutListFailed hook: with no list there is no step to fail, so without it the outage that
// stops every rollout would not move the counter at all.
func (m *RolloutMetrics) ObserveListFailure(error) {
	if m == nil {
		return
	}
	m.failures.WithLabelValues(rolloutStageList).Inc()
}

// Observe records one rollout step. It is the model.Api OnRolloutStep hook.
func (m *RolloutMetrics) Observe(res model.RolloutStepResult, err error) {
	if m == nil {
		return
	}
	if err != nil {
		m.failures.WithLabelValues(rolloutStageStep).Inc()
		return
	}
	m.dispatches.WithLabelValues("dispatched").Add(float64(res.Dispatched))
	m.dispatches.WithLabelValues("skipped").Add(float64(res.Skipped))
	switch {
	case res.Transition == model.RolloutCompleted:
		m.transitions.WithLabelValues("completed").Inc()
	case res.Transition == model.RolloutPaused && res.Reason == model.PauseFailureThreshold:
		m.transitions.WithLabelValues("paused_failure_threshold").Inc()
	case res.Transition == model.RolloutPaused:
		m.transitions.WithLabelValues("paused_artifact_not_assignable").Inc()
	}
}

// RolloutDriver steps every RUNNING group rollout on a fixed cadence. Like the deadline sweep it
// runs on every replica with no leader: each step takes its rollout's row lock with SKIP LOCKED, so
// two replicas never step one rollout at once, and a replica that finds the row held moves on.
type RolloutDriver struct {
	api      *model.Api
	interval time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewRolloutDriver builds the driver. interval must be positive.
func NewRolloutDriver(api *model.Api, interval time.Duration) *RolloutDriver {
	if interval <= 0 {
		panic("processor.NewRolloutDriver: interval must be positive")
	}
	return &RolloutDriver{api: api, interval: interval}
}

// Start launches the loop. It is idempotent.
func (d *RolloutDriver) Start(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancel != nil {
		return
	}
	ctx, d.cancel = context.WithCancel(ctx)
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		timer := time.NewTimer(jitter(d.interval))
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			d.Pass(ctx)
			timer.Reset(jitter(d.interval))
		}
	}()
}

// Stop ends the loop and waits for the pass in flight.
func (d *RolloutDriver) Stop() {
	d.mu.Lock()
	cancel := d.cancel
	d.cancel = nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	d.wg.Wait()
}

// Pass steps every RUNNING rollout once. Exported so a test can drive a pass without the timer.
func (d *RolloutDriver) Pass(ctx context.Context) model.AdvanceResult {
	res, err := d.api.AdvanceRollouts(ctx)
	if err != nil {
		log.Error().Err(err).Msg("The rollout driver could not list running rollouts; it is retried at the next pass.")
	}
	return res
}
