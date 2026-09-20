// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// passMetrics builds one task's signals on a registry the test can read back.
func passMetrics(t *testing.T, name string) (*core.PeriodicTaskMetrics, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{FunctionalArea: "command-delivery"}
	ms.UseMetricsRegistry(reg)
	return ms.NewPeriodicTaskMetrics(name), reg
}

// passCount reads devicechain_commanddelivery_<task>_passes_total{outcome=...}.
//
// It goes through the registry rather than the collector because the counter is unexported
// in core — which is the right place for it, and means a test outside that package has to
// read what the service actually EXPORTS. That is the more useful reading anyway.
func passCount(t *testing.T, reg *prometheus.Registry, task, outcome string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	want := "devicechain_commanddelivery_" + task + "_passes_total"
	for _, f := range families {
		if f.GetName() != want {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "outcome" && l.GetValue() == outcome {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// lastSuccess reads the task's last-success gauge, which is NaN until a pass has done work.
func lastSuccess(t *testing.T, reg *prometheus.Registry, task string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	want := "devicechain_commanddelivery_" + task + "_last_success_timestamp_seconds"
	for _, f := range families {
		if f.GetName() != want {
			continue
		}
		for _, m := range f.GetMetric() {
			return m.GetGauge().GetValue()
		}
	}
	return 0
}

// TestASweepThatCouldNotReadIsNotACompletedPass is the regression guard for the defect that
// made these signals worth less than nothing.
//
// 🔴🔴 THE PASS USED TO REPORT `complete` WHEN ITS FIRST QUERY FAILED. The lock body logged
// the read error and returned nil, so with the database unreachable the sweep incremented
// passes_total{outcome="complete"} and refreshed its last-success gauge every thirty seconds.
// That is worse than having no metric: an operator watching the series built to tell them the
// sweep had stopped would have been told, on a fixed cadence, that it was healthy. Both halves
// are asserted, because the counter alone would be satisfied by a pass that recorded nothing.
func TestASweepThatCouldNotReadIsNotACompletedPass(t *testing.T) {
	m, reg := passMetrics(t, "command_sweep")
	api := &fakeApi{lockAvailable: true, pendingErr: errors.New("connection refused")}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: &recordingWriter{},
		DeliveryMetrics: DeliveryMetrics{CommandSweep: m}}

	started := time.Now()
	err := proc.sweepLocked(context.Background())
	require.Error(t, err, "a pass whose first read failed did none of its work")
	assert.NotErrorIs(t, err, core.ErrPassSkipped, "the lock was acquired; this is not a decline")
	assert.NotErrorIs(t, err, core.ErrPassPartial, "nothing was delivered, so no part of it completed")

	m.RecordPass(context.Background(), err, started, "command-sweep")
	assert.EqualValues(t, 1, passCount(t, reg, "command_sweep", core.PassFailed))
	assert.Zero(t, passCount(t, reg, "command_sweep", core.PassComplete),
		"a sweep that read nothing must never be filed as complete")
	assert.True(t, math.IsNaN(lastSuccess(t, reg, "command_sweep")),
		"and it must not refresh the timestamp an operator alerts on")
}

// TestAReconcilePassWithNoPresenceReaderIsSkippedNotComplete covers the other false-complete:
// a pass that correctly declines to act is not a pass that did its work. With no presence
// reader, holds are never released and lapse to EXPIRED — the one case where "keep holding" is
// permanent — so a fresh last-success timestamp would be actively misleading.
func TestAReconcilePassWithNoPresenceReaderIsSkippedNotComplete(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*CommandDeliveryProcessor) error
	}{
		{"hold", func(p *CommandDeliveryProcessor) error { return p.reconcileOnePage(context.Background()) }},
		{"stranded", func(p *CommandDeliveryProcessor) error { return p.reconcileStrandedPage(context.Background()) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := &CommandDeliveryProcessor{Api: &fakeApi{}, DeviceCommandsWriter: &recordingWriter{}}
			assert.ErrorIs(t, tc.run(proc), core.ErrPassSkipped)
		})
	}
}

// TestAPassInterruptedByShutdownIsFiledAsCancelled pins the call sites, not the classifier.
//
// 🔴🔴 THE CLASSIFIER IS TESTED IN core; ITS FIVE NEW CALLERS WERE NOT. Replacing the context
// passed at any one of them with context.Background() survived the entire suite — a single
// token that turns every shutdown-interrupted pass into a FAILURE on the series operators page
// on, once per deploy, for the life of the service. This drives the real ticker so the context
// under test is the one the loop actually hands over.
func TestAPassInterruptedByShutdownIsFiledAsCancelled(t *testing.T) {
	m, reg := passMetrics(t, "command_sweep")
	entered := make(chan struct{}, 1)
	api := &cancellingLockApi{entered: entered}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: &recordingWriter{},
		DeliveryMetrics: DeliveryMetrics{CommandSweep: m}, SweepInterval: 5 * time.Millisecond}
	proc.quit = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { proc.runSweepTicker(ctx); close(done) }()

	<-entered // a pass is now in flight
	cancel()  // ...and the process is going away underneath it
	<-done

	assert.EqualValues(t, 1, passCount(t, reg, "command_sweep", core.PassCancelled),
		"a pass cut short by shutdown is the operator's own deploy, not a fault")
	assert.Zero(t, passCount(t, reg, "command_sweep", core.PassFailed),
		"and it must not also land on the failure series")
	assert.True(t, math.IsNaN(lastSuccess(t, reg, "command_sweep")))
}

// cancellingLockApi parks inside the lock body until the caller's context is cancelled, then
// reports the failure a real pass reports when its reads are cut short. That pairing — an
// error AND a cancelled context — is the only shape that tells the two classifications apart.
type cancellingLockApi struct {
	model.CommandDeliveryApi
	entered chan struct{}
}

func (a *cancellingLockApi) TrySweepLock(ctx context.Context, fn func() error) (bool, error) {
	select {
	case a.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return true, ctx.Err()
}
