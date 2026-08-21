// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// recordLateSamples has to actually move the counter. The whole value of instrumenting the refusal
// is that an operator can see it, so a record that folds to nothing publishes a permanent zero —
// which reads as "no samples lost" rather than as "not measured". Constructing the struct directly
// avoids needing a microservice: the field is a plain prometheus.Counter either way.
func TestRecordLateSamplesMovesTheCounter(t *testing.T) {
	m := &detectMetrics{lateSamplesTotal: prometheus.NewCounter(prometheus.CounterOpts{
		Name: "detect_late_samples_total_test",
		Help: "test",
	})}
	m.recordLateSamples(7)
	m.recordLateSamples(3)
	if got := testutil.ToFloat64(m.lateSamplesTotal); got != 10 {
		t.Errorf("counter = %v, want 10", got)
	}
	// Zero is the overwhelmingly common case (every in-order message), and must stay a no-op
	// rather than a counter touch.
	m.recordLateSamples(0)
	if got := testutil.ToFloat64(m.lateSamplesTotal); got != 10 {
		t.Errorf("after a zero record: counter = %v, want 10", got)
	}
	// Nil-safe: unit-test loops run unmeasured, like every other recorder here.
	var nilMetrics *detectMetrics
	nilMetrics.recordLateSamples(5)
}
