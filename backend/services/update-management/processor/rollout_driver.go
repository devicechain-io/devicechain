// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
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
var rolloutTransitionLabels = []string{"completed", "paused_failure_threshold", "paused_artifact_not_assignable", "paused_failure_rate", "paused_cohort_ready"}

// NewRolloutMetrics registers the rollout instruments, once.
func NewRolloutMetrics(ms *core.Microservice) *RolloutMetrics {
	m := &RolloutMetrics{
		dispatches: ms.NewCounterVec("update_rollout_dispatches_total",
			"Devices a group rollout decided: dispatched (assigned the update, or adopted an assignment of the same "+
				"artifact), or skipped (the assignment plane refused the device: not found, disabled, incompatible, "+
				"busy with another update).",
			[]string{"result"}),
		transitions: ms.NewCounterVec("update_rollout_transitions_total",
			"Group rollouts the driver moved on its own: completed, or paused at a failure-count limit, "+
				"failure-rate limit, manual cohort gate, or because the artifact stopped being assignable.",
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
	// A rate halt can commit even when all pre-lock device lookups failed. StepRollout
	// returns that committed transition alongside the lookup error; expose both facts.
	if res.Transition == model.RolloutPaused && res.Reason == model.PauseFailureRate {
		m.transitions.WithLabelValues("paused_failure_rate").Inc()
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
	case res.Transition == model.RolloutPaused && res.Reason == model.PauseCohortReady:
		m.transitions.WithLabelValues("paused_cohort_ready").Inc()
	case res.Transition == model.RolloutPaused && res.Reason != model.PauseFailureRate:
		m.transitions.WithLabelValues("paused_artifact_not_assignable").Inc()
	}
}

// RolloutDriver steps every RUNNING group rollout on a jittered cadence. Like the deadline sweep it
// runs on every replica with no leader: each step takes its rollout's row lock with SKIP LOCKED, so
// two replicas never step one rollout at once, and a replica that finds the row held moves on.
type RolloutDriver struct {
	api *model.Api

	mu       sync.Mutex
	task     *core.PeriodicTask
	makeTask func() *core.PeriodicTask
}

// NewRolloutDriver builds the driver. interval must be positive. passMetrics are required and
// registered once by the caller on the initialize path: the driver is built on every start.
func NewRolloutDriver(api *model.Api, interval time.Duration, passMetrics *core.PeriodicTaskMetrics) *RolloutDriver {
	if interval <= 0 {
		panic("processor.NewRolloutDriver: interval must be positive")
	}
	// Refused here rather than at the first Start: the task is built per start, and a nil here
	// would otherwise surface only when the service starts, not when it is wired.
	if passMetrics == nil {
		panic("processor.NewRolloutDriver: pass metrics are required")
	}
	d := &RolloutDriver{api: api}
	// A fresh lifecycle per start preserves the wrapper's ability to restart after Stop.
	d.makeTask = func() *core.PeriodicTask {
		return core.NewPeriodicTask("update-management", "rollout-driver", interval,
			d.runOnce, core.NewNoOpLifecycleCallbacks(), passMetrics)
	}
	return d
}

// Start launches the loop. It is idempotent. It has the lifecycle-verb signature so the service's
// start and stop callbacks wire it as a component of their own (the start-stop symmetry guard
// pairs a verb called on a package-level variable, and stops at it rather than reading inside).
func (d *RolloutDriver) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.task != nil {
		return nil
	}
	task := d.makeTask()
	if err := task.Initialize(ctx); err != nil {
		return err
	}
	if err := task.Start(ctx); err != nil {
		return err
	}
	d.task = task
	return nil
}

// Stop ends the loop and waits for the pass in flight.
func (d *RolloutDriver) Stop(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.task == nil {
		return nil
	}
	err := d.task.Stop(ctx)
	d.task = nil
	return err
}

// runOnce is the scheduled pass. A failure to list running rollouts fails the pass; individual
// step failures (already logged per rollout by AdvanceRollouts) make it partial or failed through
// core.PassResult, so a driver whose every step fails does not read as healthy.
func (d *RolloutDriver) runOnce(ctx context.Context) error {
	res, err := d.api.AdvanceRollouts(ctx)
	if err != nil {
		return fmt.Errorf("the rollout driver could not list running rollouts: %w", err)
	}
	return rolloutPassResult(res)
}

// rolloutPassResult maps one pass's step tally onto the periodic-task outcome.
func rolloutPassResult(res model.AdvanceResult) error {
	var lastErr error
	if res.Failed > 0 {
		lastErr = fmt.Errorf("%d of %d rollout steps failed", res.Failed, res.Stepped)
	}
	return core.PassResult(res.Stepped, res.Failed, lastErr)
}

// Pass steps every RUNNING rollout once. Exported so a test can drive a pass without the timer.
func (d *RolloutDriver) Pass(ctx context.Context) model.AdvanceResult {
	res, err := d.api.AdvanceRollouts(ctx)
	if err != nil {
		log.Error().Err(err).Msg("The rollout driver could not list running rollouts; it is retried at the next pass.")
	}
	return res
}
