// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// passSample reads one sample of a task series; label is the outcome ("" for an unlabelled one).
func passSample(t *testing.T, reg *prometheus.Registry, name, outcome string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			if outcome == "" {
				return gaugeOrCounter(m)
			}
			for _, l := range m.GetLabel() {
				if l.GetName() == "outcome" && l.GetValue() == outcome {
					return gaugeOrCounter(m)
				}
			}
		}
	}
	return 0
}

func gaugeOrCounter(m *dto.Metric) float64 {
	if m.GetCounter() != nil {
		return m.GetCounter().GetValue()
	}
	return m.GetGauge().GetValue()
}

type errRunner struct {
	runs atomic.Int32
	err  error
}

func (e *errRunner) Run(context.Context) error { e.runs.Add(1); return e.err }

type errProber struct {
	probes atomic.Int32
	err    error
}

func (e *errProber) Probe(context.Context) error { e.probes.Add(1); return e.err }

func presenceTestMetrics(reg *prometheus.Registry) presenceLoopMetrics {
	ms := &core.Microservice{FunctionalArea: "event-sources"}
	ms.UseMetricsRegistry(reg)
	// Built through the service's own constructor, so the series names below are the ones
	// the service exports, not ones this test chose.
	return newPresenceLoopMetrics(ms)
}

const (
	reconcilePasses = "devicechain_eventsources_presence_reconcile_passes_total"
	canaryPasses    = "devicechain_eventsources_presence_canary_passes_total"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestThePresenceLoopsFileTheirPasses drives runPresenceLoops, the caller, so a loop that
// stopped calling RecordPass (or stopped running its body) fails here.
func TestThePresenceLoopsFileTheirPasses(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := presenceTestMetrics(reg)
	ctx, cancel := context.WithCancel(context.Background())
	runner := &errRunner{}
	prober := &errProber{err: errors.New("broker unreachable")}
	rt := &presenceRuntime{stopped: make(chan struct{})}

	runPresenceLoops(ctx, rt, runner, prober, time.Hour, 5*time.Millisecond, metrics)

	// The reconcile's first pass is immediate; the canary's first is one interval in.
	waitFor(t, "the immediate reconcile pass to be filed", func() bool {
		return passSample(t, reg, reconcilePasses, core.PassComplete) >= 1
	})
	waitFor(t, "a canary probe to be filed", func() bool {
		return passSample(t, reg, canaryPasses, core.PassFailed) >= 1
	})
	assert.Zero(t, passSample(t, reg, canaryPasses, core.PassComplete),
		"a probe that returned an error is a failure, never a completed pass")
	assert.Greater(t, passSample(t, reg, "devicechain_eventsources_presence_reconcile_last_success_timestamp_seconds", ""), float64(0))

	// Shutdown: both loops end, and a pass in flight at cancel is not filed as a fault.
	cancel()
	select {
	case <-rt.stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("the presence loops did not stop on cancel")
	}
}

// TestACancelledReconcileIsNotAFailure pins the context passed to RecordPass: handing it a
// fresh context would turn every deploy that catches a pass mid-flight into a failure.
func TestACancelledReconcileIsNotAFailure(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := presenceTestMetrics(reg)
	ctx, cancel := context.WithCancel(context.Background())
	runner := &blockingRunner{entered: make(chan struct{}, 1), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { runReconcileLoop(ctx, runner, time.Hour, metrics.reconcile); close(done) }()
	<-runner.entered
	cancel()
	<-done
	assert.EqualValues(t, 1, passSample(t, reg, reconcilePasses, core.PassCancelled))
	assert.Zero(t, passSample(t, reg, reconcilePasses, core.PassFailed))
}

// A reconcile pass that returned an error is a failure, never a completed pass. The canary
// half of TestThePresenceLoopsFileTheirPasses pins the same for the probe; this is the
// reconcile half, and the outcomes differ from that test's so a loop filed on the other's
// series fails both.
func TestAFailedReconcileIsAFailedPass(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := presenceTestMetrics(reg)
	ctx, cancel := context.WithCancel(context.Background())
	runner := &errRunner{err: errors.New("no tenant's presence state could be read")}
	rt := &presenceRuntime{stopped: make(chan struct{})}

	runPresenceLoops(ctx, rt, runner, nil, 5*time.Millisecond, time.Hour, metrics)

	waitFor(t, "a failed reconcile pass to be filed", func() bool {
		return passSample(t, reg, reconcilePasses, core.PassFailed) >= 2
	})
	assert.Zero(t, passSample(t, reg, reconcilePasses, core.PassComplete),
		"a reconcile that returned an error is a failure, never a completed pass")

	cancel()
	select {
	case <-rt.stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("the presence loops did not stop on cancel")
	}
}

// initializePresenceMetrics is where the service binds its three presence task names. The loop
// tests build their own metrics through newPresenceLoopMetrics, so nothing else proves the
// STARTUP path registers them: a service that stopped calling the constructor (or left a field
// nil, which still classifies and logs) would export no pass series at all.
func TestThePresencePassSeriesAreRegisteredAtStartup(t *testing.T) {
	reg := prometheus.NewRegistry()
	prevMS, prevDemote, prevLoops := Microservice, PresenceDemoteTaskMetrics, PresenceLoopTaskMetrics
	t.Cleanup(func() {
		Microservice, PresenceDemoteTaskMetrics, PresenceLoopTaskMetrics = prevMS, prevDemote, prevLoops
	})
	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}
	Microservice.UseMetricsRegistry(reg)
	PresenceDemoteTaskMetrics, PresenceLoopTaskMetrics = nil, presenceLoopMetrics{}

	initializePresenceMetrics()

	require.NotNil(t, PresenceDemoteTaskMetrics, "the demote drain files no passes")
	require.NotNil(t, PresenceLoopTaskMetrics.reconcile, "the reconcile loop files no passes")
	require.NotNil(t, PresenceLoopTaskMetrics.canary, "the canary loop files no passes")

	// Each task's last-success gauge exists from registration (NaN until a pass completes), so
	// it is visible before any pass has run; the counter appears with its first pass.
	families, err := reg.Gather()
	require.NoError(t, err)
	have := map[string]bool{}
	for _, f := range families {
		have[f.GetName()] = true
	}
	for _, task := range []string{"presence_demote", "presence_reconcile", "presence_canary"} {
		assert.True(t, have["devicechain_eventsources_"+task+"_last_success_timestamp_seconds"],
			"%s is not registered by the startup path", task)
	}

	// And each field is bound to its own name: filing through one moves only that series.
	PresenceLoopTaskMetrics.canary.RecordPass(context.Background(), nil, time.Now(), "presence-canary")
	PresenceLoopTaskMetrics.reconcile.RecordPass(context.Background(), errors.New("x"), time.Now(), "presence-reconcile")
	PresenceDemoteTaskMetrics.RecordPass(context.Background(), nil, time.Now(), "presence-demote")
	assert.Equal(t, float64(1), passSample(t, reg, canaryPasses, core.PassComplete))
	assert.Equal(t, float64(1), passSample(t, reg, reconcilePasses, core.PassFailed))
	assert.Zero(t, passSample(t, reg, canaryPasses, core.PassFailed))
	assert.Equal(t, float64(1), passSample(t, reg, "devicechain_eventsources_presence_demote_passes_total", core.PassComplete))
}
