// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// passTotal reads devicechain_eventprocessing_<task>_passes_total{outcome}; 0 when absent.
func passTotal(t *testing.T, reg *prometheus.Registry, task, outcome string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	want := "devicechain_eventprocessing_" + task + "_passes_total"
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

func eventually2(t *testing.T, what string, cond func() bool) {
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

func passMicroservice() (*core.Microservice, *prometheus.Registry) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-processing"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	return ms, reg
}

// TestTheLoopTickFilesItsPasses drives run() -- the caller -- so a tick arm that stopped
// recording fails here. The fact sweep the tick launches is the control that the arm's body
// really ran: the pass is filed at the end of that body, not before it.
func TestTheLoopTickFilesItsPasses(t *testing.T) {
	dm := newDmWorld(t)
	rig := newReconcileRig(t, dm)
	ms, reg := passMicroservice()
	rig.rp.metrics = NewDetectMetrics(ms)
	loopCtx, cancel := context.WithCancel(context.Background())
	rig.rp.procCtx, rig.rp.procCancel = loopCtx, cancel
	rig.rp.ResolvedEventsReader = &fakeReader{}
	rig.rp.cfg.TickInterval = 5 * time.Millisecond

	rig.rp.readerWG.Add(1)
	done := make(chan struct{})
	go func() { rig.rp.run(); close(done) }()

	eventually2(t, "a tick pass to be filed", func() bool {
		return passTotal(t, reg, "detect_tick", core.PassComplete) >= 2
	})
	assert.NotZero(t, rig.src.sweeps.Load(), "a pass was filed without the tick body having run")

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the loop did not stop on cancel")
	}
	assert.Zero(t, passTotal(t, reg, "detect_tick", core.PassFailed), "a stop is never a failed pass")
}

// TestTheShedSummaryRunFilesItsPasses drives run(): the summary letter is the control that the
// flush body ran, the pass counter that the loop filed it.
func TestTheShedSummaryRunFilesItsPasses(t *testing.T) {
	setup := func(dead *deadRecorder) (*shedLetterer, *prometheus.Registry) {
		ms, reg := passMicroservice()
		s := newShedLetterer(deadletter.NewProducer(ms).NewSink(dead), testShedBudget, NewReactMetrics(ms))
		s.window = 5 * time.Millisecond
		s.unlettered = map[string]map[string]int{"acme": {"httpCall": 3}, "globex": {"publish": 1}}
		return s, reg
	}
	runUntil := func(t *testing.T, s *shedLetterer, reg *prometheus.Registry, outcome string) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { s.run(ctx); close(done) }()
		eventually2(t, "a "+outcome+" pass", func() bool { return passTotal(t, reg, "shed_summary", outcome) >= 1 })
		cancel()
		<-done
	}

	t.Run("a window whose summaries were written is a complete pass", func(t *testing.T) {
		dead := &deadRecorder{}
		s, reg := setup(dead)
		runUntil(t, s, reg, core.PassComplete)
		assert.Len(t, dead.msgs, 2, "one summary per tenant: the pass was filed over a flush that ran")
		assert.Zero(t, passTotal(t, reg, "shed_summary", core.PassFailed))
	})

	t.Run("a window whose summaries were all lost is a failed pass", func(t *testing.T) {
		dead := &deadRecorder{err: errors.New("stream unavailable")}
		s, reg := setup(dead)
		runUntil(t, s, reg, core.PassFailed)
	})
}
