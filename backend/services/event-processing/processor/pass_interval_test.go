// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func intervalMetricsRegistry() (*core.Microservice, *prometheus.Registry) {
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{InstanceId: "interval-test", FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(reg)
	return ms, reg
}

// Read the served gauge from the registry, rather than calling the setter under test.
func passIntervalEquals(reg *prometheus.Registry, task string, period time.Duration) error {
	name := "devicechain_eventprocessing_" + task + "_pass_interval_seconds"
	want := fmt.Sprintf("# HELP %s Declared gap between passes of the %s task in seconds; NaN until started.\n# TYPE %s gauge\n%s %g\n",
		name, task, name, name, period.Seconds())
	return testutil.GatherAndCompare(reg, strings.NewReader(want), name)
}

func TestDetectRunDeclaresPassInterval(t *testing.T) {
	g := newGapRig(t, &fakeReplayOpener{})
	ms, reg := intervalMetricsRegistry()
	g.rp.metrics = NewDetectMetrics(ms)
	g.rp.cfg.TickInterval = 137 * time.Millisecond
	g.rp.ResolvedEventsReader = &fakeReader{}
	g.rp.readerWG.Add(1)
	go g.rp.run() // The same live-loop entry point used by production term startup.
	t.Cleanup(func() { g.rp.pcancel(); g.rp.readerWG.Wait() })

	require.Eventually(t, func() bool {
		return passIntervalEquals(reg, "detect_tick", g.rp.cfg.TickInterval) == nil
	}, time.Second, time.Millisecond)
	require.NoError(t, passIntervalEquals(reg, "detect_tick", g.rp.cfg.TickInterval))
}

func TestStartRuleHealthDeclaresPassInterval(t *testing.T) {
	rp := newTestProcessor(newTestStore(t), nil, 1)
	ms, reg := intervalMetricsRegistry()
	rp.metrics = NewDetectMetrics(ms)
	rp.ruleStats = newBlockingStatStore(false)
	rp.cfg.RuleHealth = healthConfig()

	require.NoError(t, rp.startRuleHealth()) // Production term startup's accumulator entry point.
	t.Cleanup(func() { rp.stopRuleHealth(false, context.Background()) })
	require.NotNil(t, rp.health)
	require.NoError(t, passIntervalEquals(reg, "rule_health_flush", rp.ruleHealthConfig().FlushInterval))
}

func TestShedSummaryRunDeclaresPassInterval(t *testing.T) {
	ms, reg := intervalMetricsRegistry()
	s := newShedLetterer(nil, testShedBudget, NewReactMetrics(ms))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.run(ctx) // The dispatcher's production summary-loop entry point.
	}()
	t.Cleanup(func() { cancel(); <-done })

	require.Eventually(t, func() bool {
		return passIntervalEquals(reg, "shed_summary", s.window) == nil
	}, time.Second, time.Millisecond)
	require.NoError(t, passIntervalEquals(reg, "shed_summary", s.window))
}
