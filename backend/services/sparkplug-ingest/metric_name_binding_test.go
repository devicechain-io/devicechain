// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// seriesValue reads one metric family's single sample out of the registry BY NAME.
//
// 🔴 IT GATHERS RATHER THAN READING THE COLLECTOR, and that is the whole point of the
// test below. testutil.ToFloat64 takes the Go value and reports what it holds, so it
// answers the same number no matter which name that value was registered under — it
// cannot see a swap. Going through the registry is what ties a struct field to the
// identifier an operator will actually type into PromQL.
//
// A missing family is a required-presence failure, not a zero: "the series is absent"
// and "the series is at 0" are different facts and only one of them is a passing state.
func seriesValue(t *testing.T, registry *prometheus.Registry, name string) float64 {
	t.Helper()

	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		metrics := family.GetMetric()
		require.Len(t, metrics, 1, "%s is not a single unlabelled series", name)
		return metrics[0].GetCounter().GetValue()
	}
	require.Failf(t, "metric not registered", "no family named %s in the registry", name)
	return 0
}

// Each rebirth-queue counter is exported under ITS OWN name.
//
// 🔑 THE GAP THIS CLOSES IS THAT NOTHING ELSE BINDS A FIELD TO A NAME. The host tests
// construct their own counters and assert which ARM of the enqueue select moves which
// FIELD; TestStartPhaseRestartDoesNotPanic asserts both NAMES are registered in the
// initialize phase. Neither notices if buildMetrics hands the two constructors to each
// other's field, because a swap registers both names and moves both fields — just the
// wrong way round. What ships then is a healthy fleet whose rebirth_dropped_total climbs
// with every accepted request while rebirth_enqueued_total sits at zero, with the
// operator documentation reading exactly backwards.
//
// Swapping only ONE of them dies on promauto's duplicate-fqName panic. Swapping BOTH is
// silent, which is why this reads the registry rather than trusting construction.
//
// 🔴 THE TWO VALUES MUST DIFFER. Driving both counters to the same number would pass
// under a swap, which is the mutant this exists to kill.
func TestRebirthQueueCountersAreExportedUnderTheirOwnNames(t *testing.T) {
	registry := newTestMicroservice(t)

	metrics := buildMetrics()
	metrics.RebirthEnqueued.Add(3)
	metrics.RebirthDropped.Add(1)

	require.Equal(t, float64(3),
		seriesValue(t, registry, "devicechain_sparkplugingest_rebirth_enqueued_total"),
		"the counter the host increments on the ACCEPTED arm is not the one exported as rebirth_enqueued_total")
	require.Equal(t, float64(1),
		seriesValue(t, registry, "devicechain_sparkplugingest_rebirth_dropped_total"),
		"the counter the host increments on the DROPPED arm is not the one exported as rebirth_dropped_total")
}
