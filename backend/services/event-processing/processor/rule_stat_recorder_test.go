// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type failingFireStore struct{ err error }

func (f failingFireStore) RecordFire(context.Context, string, string, time.Time, string) error {
	return f.err
}

// The recorder swallows a failed write by design, so the counter is the only aggregate trace of
// a broken projection. A success must not move it.
func TestRuleStatRecorderCountsFailedWrites(t *testing.T) {
	m := &DetectMetrics{ruleStatWriteFailures: prometheus.NewCounter(prometheus.CounterOpts{
		Name: "detect_rule_stat_write_failures_total_test", Help: "test",
	})}
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
	// Nil metrics (scaffold paths) must not panic.
	ruleStatRecorder{store: failingFireStore{err: errors.New("x")}}.RecordFire(context.Background(), "r", "t", time.Now(), "raise")
}
