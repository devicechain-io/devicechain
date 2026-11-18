// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecheckOnlyDoesNotDeclareDrainInterval(t *testing.T) {
	prev := PresenceDemoteTaskMetrics
	t.Cleanup(func() { PresenceDemoteTaskMetrics = prev })
	for _, drainConfigured := range []bool{false, true} {
		reg := prometheus.NewRegistry()
		ms := &core.Microservice{FunctionalArea: "event-sources"}
		ms.UseMetricsRegistry(reg)
		PresenceDemoteTaskMetrics = ms.NewPeriodicTaskMetrics("presence_demote")
		runner := &recheckBroker{}
		if drainConfigured {
			runner.drain = &recheckBroker{}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rt := &presenceRuntime{stopped: make(chan struct{})}
		launchDemoteLoop(ctx, rt, runner, time.Minute, time.Hour)
		select {
		case <-rt.stopped:
		case <-time.After(5 * time.Second):
			t.Fatal("demote loop did not stop")
		}
		families, err := reg.Gather()
		require.NoError(t, err)
		found := false
		for _, f := range families {
			if f.GetName() == "devicechain_eventsources_presence_demote_pass_interval_seconds" {
				found = true
				value := f.Metric[0].GetGauge().GetValue()
				if drainConfigured {
					assert.Equal(t, 60.0, value)
				} else {
					assert.True(t, math.IsNaN(value), "recheck-only runner declared a drain interval")
				}
			}
		}
		require.True(t, found)
	}
}
