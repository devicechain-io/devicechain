// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-management/processor"
	"github.com/devicechain-io/dc-microservice/core"
)

// Use the production constructor; registration must not depend on a running sweep or store.
// The rollup tail is deliberately not instrumented yet: its RunOnce returns nil when the lock is
// held or every window is deferred, which pass metrics would count as a complete pass.
func TestPeriodicPassMetricsWiring(t *testing.T) {
	ms := &core.Microservice{FunctionalArea: "event-management"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	processor.NewUsageRollupSweep(ms, nil, nil, processor.UsageRollupInterval, core.NewNoOpLifecycleCallbacks())
	tasks := []string{"usage_rollup"}
	subsystem := "eventmanagement"

	families, err := reg.Gather()
	require.NoError(t, err)
	for _, task := range tasks {
		name := "devicechain_" + subsystem + "_" + task + "_passes_total"
		found := false
		for _, family := range families {
			if family.GetName() != name {
				continue
			}
			found = true
			outcomes := map[string]float64{}
			for _, metric := range family.GetMetric() {
				require.Len(t, metric.GetLabel(), 1, name)
				label := metric.GetLabel()[0]
				require.Equal(t, "outcome", label.GetName(), name)
				outcomes[label.GetValue()] = metric.GetCounter().GetValue()
			}
			require.Equal(t, map[string]float64{
				"complete": 0, "partial": 0, "failed": 0, "skipped": 0, "cancelled": 0,
			}, outcomes, name)
		}
		require.True(t, found, "%s is not registered", name)
	}
}
