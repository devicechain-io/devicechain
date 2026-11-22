// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

type fakeSweepStore struct {
	tenants []string
	// left is each tenant's stranded backlog; fail is a tenant whose sweep errs; busy skips.
	left   map[string]int64
	fail   string
	busy   bool
	calls  map[string]int
	before time.Time
}

func (f *fakeSweepStore) SweepTenants(context.Context) ([]string, error) { return f.tenants, nil }

func (f *fakeSweepStore) SweepStranded(_ context.Context, tenant string, before time.Time, limit int) (int64, error) {
	f.calls[tenant]++
	f.before = before
	if f.busy {
		return 0, core.ErrPassSkipped
	}
	if tenant == f.fail {
		return 0, errors.New("boom")
	}
	n := min(f.left[tenant], int64(limit))
	f.left[tenant] -= n
	return n, nil
}

func TestRuleStatSweepPassBatchesEachTenantWithinItsBound(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	f := &fakeSweepStore{tenants: []string{"acme", "beta", "huge"}, calls: map[string]int{},
		left: map[string]int64{"acme": RuleStatSweepBatch + 1, "beta": 0, "huge": RuleStatSweepBatch * (ruleStatSweepMaxBatches + 3)}}
	s := &RuleStatSweep{Store: f, Now: func() time.Time { return now }}
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !f.before.Equal(now.Add(-RuleStatSweepQuiet)) {
		t.Errorf("cut-off %s; want now less the quiet period", f.before)
	}
	// acme: a full batch then a partial one; beta: one empty call; huge: capped.
	if f.calls["acme"] != 2 || f.left["acme"] != 0 {
		t.Errorf("acme: %d calls, %d left; want 2 calls and nothing left", f.calls["acme"], f.left["acme"])
	}
	if f.calls["beta"] != 1 {
		t.Errorf("beta: %d calls; want 1", f.calls["beta"])
	}
	if f.calls["huge"] != ruleStatSweepMaxBatches || f.left["huge"] != RuleStatSweepBatch*3 {
		t.Errorf("huge: %d calls, %d left; want the %d-batch cap", f.calls["huge"], f.left["huge"], ruleStatSweepMaxBatches)
	}
}

func TestRuleStatSweepPassStopsWhenAnotherReplicaHoldsTheLock(t *testing.T) {
	f := &fakeSweepStore{tenants: []string{"acme", "beta"}, calls: map[string]int{}, busy: true}
	err := (&RuleStatSweep{Store: f}).Run(context.Background())
	if !errors.Is(err, core.ErrPassSkipped) {
		t.Fatalf("Run = %v; want ErrPassSkipped", err)
	}
	if f.calls["beta"] != 0 {
		t.Errorf("the pass went on to beta after the lock was refused")
	}
}

func TestRuleStatSweepPassReportsAFailedTenantAndGoesOn(t *testing.T) {
	f := &fakeSweepStore{tenants: []string{"acme", "beta"}, calls: map[string]int{}, fail: "acme",
		left: map[string]int64{"beta": 3}}
	err := (&RuleStatSweep{Store: f}).Run(context.Background())
	if err == nil {
		t.Fatal("a failed tenant was not reported")
	}
	if f.calls["acme"] != 1 || f.left["beta"] != 0 {
		t.Errorf("acme %d calls, beta %d left; want one try at acme and beta swept", f.calls["acme"], f.left["beta"])
	}
}
