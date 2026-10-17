// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

type failingFireStore struct{ err error }

func (f failingFireStore) RecordFire(context.Context, string, string, time.Time, string) error {
	return f.err
}

// The recorder swallows a failed write by design, so the counter is the only aggregate trace of
// a broken projection. A success must not move it.
func TestRuleStatRecorderCountsFailedWrites(t *testing.T) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	m := NewDetectMetrics(ms)
	bad := ruleStatRecorder{store: failingFireStore{err: errors.New("boom")}, metrics: m}
	bad.RecordFire(context.Background(), "r1", "t1", time.Now(), "raise")
	bad.RecordFire(context.Background(), "r1", "t2", time.Now(), "clear")
	if got := testutil.ToFloat64(m.ruleStatWriteFailures); got != 2 {
		t.Errorf("counter = %v, want 2", got)
	}
	good := ruleStatRecorder{store: failingFireStore{}, metrics: m}
	good.RecordFire(context.Background(), "r1", "t1", time.Now(), "raise")
	if got := testutil.ToFloat64(m.ruleStatWriteFailures); got != 2 {
		t.Errorf("after a successful write: counter = %v, want 2", got)
	}
	// Every write is timed, failed or not: the loop waited for each of them.
	if got := histogramCount(t, m.ruleStatWrite); got != 3 {
		t.Errorf("rule-stat write time observed %d times over 3 writes, want 3", got)
	}
	// Nil metrics (scaffold paths) must not panic.
	ruleStatRecorder{store: failingFireStore{err: errors.New("x")}}.RecordFire(context.Background(), "r", "t", time.Now(), "raise")
}

// histogramStats is a histogram's observation count and sum (seconds), for a benchmark or test.
func histogramStats(tb testing.TB, o prometheus.Observer) (count uint64, sum float64) {
	tb.Helper()
	ch := make(chan prometheus.Metric, 1)
	o.(prometheus.Collector).Collect(ch)
	var d dto.Metric
	if err := (<-ch).Write(&d); err != nil {
		tb.Fatalf("write histogram: %v", err)
	}
	return d.GetHistogram().GetSampleCount(), d.GetHistogram().GetSampleSum()
}
