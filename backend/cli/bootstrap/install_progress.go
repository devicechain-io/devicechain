// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/tofujson"
)

// The measured progress an install reports. Each metric is a snapshot of something
// the install actually observed; none of them is a clock, and none of them decides
// anything. Only Install's return value closes a stage as a success.
const (
	// metricTofuActions counts one OpenTofu apply's resource actions: completed out
	// of planned. A new apply, which is a new stage attempt, starts its own count.
	metricTofuActions = "opentofu.actions"
	sourceTofu        = "opentofu"
	// metricDeployments counts the Deployments a rollout gate has confirmed rolled
	// out, out of the ones it waits for.
	metricDeployments = "kubernetes.deployments"
	sourceKubernetes  = "kubernetes"
	// metricStoreInstances is the relational store's ready instances out of the
	// instances its spec asks for.
	metricStoreInstances = "cloudnativepg.instances"
	sourceCNPG           = "cloudnativepg"
)

// stageProgress reports progress for ONE attempt of one stage. It is bound when the
// work it measures starts, so a snapshot can never land in a later attempt, and it
// may be used from another goroutine: it reads nothing of the run but its reporter.
type stageProgress struct {
	run     *installRun
	step    installflow.StepID
	attempt int
}

// progress binds a reporter to the active stage attempt, or nil outside an engine
// run or between stages. Every method of *stageProgress is safe on nil.
func (r *installRun) progress() *stageProgress {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started || r.step == "" {
		return nil
	}
	return &stageProgress{run: r, step: r.step, attempt: r.attempt}
}

// tofuProgress is progress, for an OpenTofu apply that is to run in -json mode, or
// nil when the presenter did not ask for its tools to be observed — in which case
// the apply runs exactly as it always has.
func (r *installRun) tofuProgress() *stageProgress {
	if r == nil || !r.observeTools {
		return nil
	}
	return r.progress()
}

func (p *stageProgress) report(sp installflow.StepProgress) {
	if p == nil {
		return
	}
	p.run.emit(installflow.Event{Step: p.step, Attempt: p.attempt, Payload: sp})
}

// tofu reports one OpenTofu snapshot. Until the plan's summary is read the total is
// unknown, which is indeterminate progress, never a total of zero.
func (p *stageProgress) tofu(s tofujson.Progress) {
	sp := installflow.StepProgress{MetricID: metricTofuActions, Mode: installflow.ProgressIndeterminate,
		Current: s.Done, Unit: installflow.UnitActions, Source: sourceTofu}
	if s.Known {
		total := s.Planned
		sp.Mode, sp.Total = installflow.ProgressMeasured, &total
		// The observer never reports more done than planned; this holds the line if
		// a provider ever completed an action the plan did not count.
		sp.Current = min(s.Done, total)
	}
	p.report(sp)
}

// gate reports a readiness gate's latest look: ready out of expected. It can go
// down; readiness regresses. A gate that has nothing to count reports nothing.
func (r *installRun) gate(metric string, unit installflow.Unit, source string, ready, expected int64) {
	if expected <= 0 || ready < 0 {
		return
	}
	total := expected
	r.progress().report(installflow.StepProgress{MetricID: metric, Mode: installflow.ProgressMeasured,
		Current: min(ready, expected), Total: &total, Unit: unit, Source: source})
}

// retry closes the active stage's attempt as a transient failure and starts the
// next attempt of it, for work the install retries itself: today, the cluster
// prerequisites' apply after the CloudNativePG admission webhook was unreachable.
//
// 🔴 A RETRY IS A NEW ATTEMPT, SO EVERYTHING MEASURED STARTS AGAIN. Progress reported
// for attempt 1 belongs to attempt 1; the retried apply plans afresh and is counted
// against its own plan, never against the denominator of the apply that failed.
//
// The failed attempt's code is the stage's ID followed by reason.
func (r *installRun) retry(reason string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started || r.step == "" {
		return
	}
	code := installflow.ErrorCode(string(r.step) + "." + reason)
	r.emit(installflow.Event{Step: r.step, Attempt: r.attempt, Payload: installflow.StepFailed{
		Code: code, Retry: installflow.RetryTransient, Effect: r.effect}})
	r.attempt++
	r.emit(installflow.Event{Step: r.step, Attempt: r.attempt, Payload: installflow.StepStarted{
		Message: installflow.MessageCode(r.step), BudgetMS: stageBudget(r.step)}})
	r.beginAttemptLocked()
}
