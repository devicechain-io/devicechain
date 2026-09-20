// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sample reads one metric family's first value out of the registry, so a test observes what
// the service EXPORTS rather than a collector it holds a pointer to.
func sample(t *testing.T, reg *prometheus.Registry, name, outcome string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			if outcome == "" {
				return m.GetGauge().GetValue()
			}
			for _, l := range m.GetLabel() {
				if l.GetName() == "outcome" && l.GetValue() == outcome {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// failingSweepApi fails the sweep's only query, which is the shape a pass must report rather
// than complete over.
type failingSweepApi struct {
	model.DeviceStateApi
	entered chan struct{}
	err     error
	block   bool
}

func (a *failingSweepApi) SweepInactive(ctx context.Context, _ time.Time) (int64, error) {
	select {
	case a.entered <- struct{}{}:
	default:
	}
	if a.block {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	return 0, a.err
}

// TestTheInactivitySweepFilesWhatItActuallyDid pins the monitor's call site.
//
// 🔴🔴 REMOVING THE RecordPass CALL ENTIRELY SURVIVED THE WHOLE SUITE before this existed.
// The metric was wired in main.go, which nothing here exercises, so the loop could have
// stopped recording and every test would still have passed — a signal that is absent is
// indistinguishable from one that is healthy on a dashboard built to watch it.
//
// The cancelled case is the second half and is the one a single token breaks: passing
// context.Background() to RecordPass turns every shutdown-interrupted sweep into a FAILURE
// on the series operators page on, once per deploy, forever.
func TestTheInactivitySweepFilesWhatItActuallyDid(t *testing.T) {
	const failed = "devicechain_devicestate_inactivity_sweep_passes_total"
	const stamp = "devicechain_devicestate_inactivity_sweep_last_success_timestamp_seconds"

	t.Run("a sweep whose query failed is a failure, not a completed pass", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		ms := &core.Microservice{FunctionalArea: "device-state"}
		ms.UseMetricsRegistry(reg)
		entered := make(chan struct{}, 1)
		sp := &StateProcessor{
			Microservice:       ms,
			Api:                &failingSweepApi{entered: entered, err: errors.New("connection refused")},
			sweepMetrics:       ms.NewPeriodicTaskMetrics("inactivity_sweep"),
			inactivityInterval: 5 * time.Millisecond,
			quit:               make(chan struct{}),
		}

		// Stopped through quit, not through the context: this half is about a sweep that
		// FAILED, and cancelling would classify it as cancelled instead.
		done := make(chan struct{})
		go func() { sp.runInactivityMonitor(context.Background()); close(done) }()
		<-entered
		close(sp.quit)
		<-done

		assert.GreaterOrEqual(t, sample(t, reg, failed, core.PassFailed), float64(1))
		assert.Zero(t, sample(t, reg, failed, core.PassComplete),
			"a sweep that could not read anything must never be filed as complete")
		assert.True(t, math.IsNaN(sample(t, reg, stamp, "")),
			"and it must not refresh the timestamp an operator alerts on")
	})

	t.Run("a sweep cut short by shutdown is cancelled, not failed", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		ms := &core.Microservice{FunctionalArea: "device-state"}
		ms.UseMetricsRegistry(reg)
		entered := make(chan struct{}, 1)
		sp := &StateProcessor{
			Microservice:       ms,
			Api:                &failingSweepApi{entered: entered, block: true},
			sweepMetrics:       ms.NewPeriodicTaskMetrics("inactivity_sweep"),
			inactivityInterval: 5 * time.Millisecond,
			quit:               make(chan struct{}),
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { sp.runInactivityMonitor(ctx); close(done) }()
		<-entered
		cancel()
		<-done

		assert.EqualValues(t, 1, sample(t, reg, failed, core.PassCancelled))
		assert.Zero(t, sample(t, reg, failed, core.PassFailed),
			"the process going away is not a fault of the sweep")
	})
}
