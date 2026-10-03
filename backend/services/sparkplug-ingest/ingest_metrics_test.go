// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every ingest counter is built, and the age-limit counter is exported under its documented
// name. The ingester skips a nil counter without a word, so a field left out of
// buildIngestMetrics is a reading dropped uncounted, with every other test still green.
func TestIngestMetricsAreAllBuiltAndTooOldIsExported(t *testing.T) {
	registry := newTestMicroservice(t)

	metrics := buildIngestMetrics()
	v := reflect.ValueOf(metrics)
	for i := 0; i < v.NumField(); i++ {
		require.False(t, v.Field(i).IsNil(), "IngestMetrics.%s is not built", v.Type().Field(i).Name)
	}

	metrics.TooOldDropped.Add(2)
	require.Equal(t, float64(2),
		seriesValue(t, registry, "devicechain_sparkplugingest_samples_too_old_dropped_total"))
}
