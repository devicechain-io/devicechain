// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// An alert over one result is increase(...) > 0, and increase() over a series that first
// APPEARS at 1 is zero: the first message a pod ever drops would fire nothing. So every
// result must be exported, at zero, before the loop has handled a single message.
func TestEveryProcessorResultIsExportedAtZero(t *testing.T) {
	ms := &Microservice{FunctionalArea: "device-state"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	pm := ms.NewProcessorMetrics("state")

	want := []string{"ok", "invalid", "failed", "dropped", "retry", "erased"}
	if got := testutil.CollectAndCount(pm.processed); got != len(want) {
		t.Fatalf("a fresh loop exports %d result series, want %d (%v)", got, len(want), want)
	}
	for _, result := range want {
		if v := testutil.ToFloat64(pm.processed.WithLabelValues(result)); v != 0 {
			t.Errorf("result %q starts at %v, want 0", result, v)
		}
	}

	// And the seeding is a starting point, not a cap: a result still counts.
	pm.Start()(ResultDropped)
	if v := testutil.ToFloat64(pm.processed.WithLabelValues(ResultDropped)); v != 1 {
		t.Errorf("after one drop the dropped result reads %v, want 1", v)
	}
	if got := testutil.CollectAndCount(pm.processed); got != len(want) {
		t.Errorf("counting a seeded result added a series: %d, want %d", got, len(want))
	}
}
