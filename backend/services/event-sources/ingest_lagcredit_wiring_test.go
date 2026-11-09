// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// freshLagCreditVectors swaps in unregistered copies of the lag-credited gate's vectors.
func freshLagCreditVectors(t *testing.T) {
	t.Helper()
	saved := []any{IngestShedCauseCounter, IngestLagCreditHistogram, AppendSkewClampedCounter}
	IngestShedCauseCounter = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "shed"}, []string{"stream", "route", "cause"})
	IngestLagCreditHistogram = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "credit"}, []string{"stream"})
	AppendSkewClampedCounter = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "clamped"}, []string{"stream"})
	t.Cleanup(func() {
		IngestShedCauseCounter = saved[0].(*prometheus.CounterVec)
		IngestLagCreditHistogram = saved[1].(*prometheus.HistogramVec)
		AppendSkewClampedCounter = saved[2].(*prometheus.CounterVec)
	})
}

// In lagCredit mode the counters the alerts read are exported at 0 from start, so the first shed burst
// after a restart is an increase from 0 and not a series born at its final value (which increase() reads
// as no change, and IngestShedBeyondLagCredit would miss). The labels are spelled out: they are the
// alerts' vocabulary.
func TestLagCreditSeriesAreSeededAtZeroInLagCreditMode(t *testing.T) {
	freshLagCreditVectors(t)
	seedLagCreditMetrics(IngestMeteringLagCredit)
	require.Equal(t, 3*3*3, testutil.CollectAndCount(IngestShedCauseCounter), "every stream x route x cause")
	for _, stream := range []string{"capture", "store", "none"} {
		require.Zero(t, testutil.ToFloat64(IngestShedCauseCounter.WithLabelValues(stream, "fresh", "ceiling")))
	}
	require.Equal(t, 3, testutil.CollectAndCount(IngestLagCreditHistogram), "one histogram per stream label")
	require.Equal(t, 2, testutil.CollectAndCount(AppendSkewClampedCounter), "capture and store")
	require.Zero(t, testutil.ToFloat64(AppendSkewClampedCounter.WithLabelValues("capture")))
}

// In legacy mode (the default) the lag-credited gate's series stay empty: nothing is exported, so its
// alerts cannot fire and a dashboard does not show a gate that is not running.
func TestLagCreditSeriesStayEmptyInLegacyMode(t *testing.T) {
	freshLagCreditVectors(t)
	seedLagCreditMetrics(IngestMeteringLegacy)
	require.Zero(t, testutil.CollectAndCount(IngestShedCauseCounter))
	require.Zero(t, testutil.CollectAndCount(IngestLagCreditHistogram))
	require.Zero(t, testutil.CollectAndCount(AppendSkewClampedCounter))
}
