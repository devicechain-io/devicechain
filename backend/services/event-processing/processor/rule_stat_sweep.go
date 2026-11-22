// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"time"

	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

const (
	// RuleStatSweepInterval is how often the stranded rule-stat sweep runs. The rows it removes
	// are not read while their version is superseded, so there is no hurry: hourly keeps the
	// table bounded without load.
	RuleStatSweepInterval = time.Hour
	// RuleStatSweepQuiet is how long a superseded version's row must have gone without a fire
	// before it is removed. A version that is still firing (events resolved against it while a
	// publish propagates, or a replay) keeps its row for at least this long after its last fire.
	RuleStatSweepQuiet = 7 * 24 * time.Hour
	// RuleStatSweepBatch bounds one delete statement; ruleStatSweepMaxBatches bounds one
	// tenant's share of a pass, so one tenant's backlog cannot hold the pass. What is left
	// over is taken by the next pass.
	RuleStatSweepBatch      = 500
	ruleStatSweepMaxBatches = 20
)

// RuleStatSweeper is the store surface the sweep needs: model.RuleStatStore.
type RuleStatSweeper interface {
	SweepTenants(ctx context.Context) ([]string, error)
	SweepStranded(ctx context.Context, tenant string, before time.Time, limit int) (int64, error)
}

var _ RuleStatSweeper = (*model.RuleStatStore)(nil)

// RuleStatSweep removes the rule-health stat rows that a re-publish strands: rows for a profile
// version that is no longer active and has not fired for RuleStatSweepQuiet. The health view
// reads only the active version's rows, so these are not read while the version is
// superseded. A rollback can make the version active again; the sweep records each version it
// removed rows of (model.RuleStatSweepRecord), and the health view then reports that the
// history was removed instead of counting the rules as never fired.
type RuleStatSweep struct {
	Store   RuleStatSweeper
	Deleted prometheus.Counter
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// NewRuleStatSweep builds the hourly sweep task.
func NewRuleStatSweep(ms *core.Microservice, store RuleStatSweeper) *core.PeriodicTask {
	s := &RuleStatSweep{Store: store, Deleted: ms.NewCounter("rule_stat_sweep_deleted_total",
		"Rule-health stat rows of superseded profile versions removed by the stranded-row sweep.")}
	return core.NewPeriodicTask(ms.FunctionalArea, "rule-stat-sweep", RuleStatSweepInterval, s.Run,
		core.NewNoOpLifecycleCallbacks(), ms.NewPeriodicTaskMetrics("rule_stat_sweep"))
}

// Run is one pass: every tenant with stat rows, up to ruleStatSweepMaxBatches batches each.
// When another replica holds the sweep lock the pass stops. It reports itself skipped only when
// it had swept nothing yet; a pass that ran batches before the lock was taken reports the
// tenants it visited, so the pass metrics do not file real work as skipped.
func (s *RuleStatSweep) Run(ctx context.Context) error {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	before := now().UTC().Add(-RuleStatSweepQuiet)
	tenants, err := s.Store.SweepTenants(ctx)
	if err != nil {
		return err
	}
	failed, visited, ran := 0, 0, false
	var last error
	for _, tenant := range tenants {
		for i := 0; i < ruleStatSweepMaxBatches; i++ {
			n, err := s.Store.SweepStranded(ctx, tenant, before, RuleStatSweepBatch)
			if errors.Is(err, core.ErrPassSkipped) {
				if !ran {
					return err
				}
				return core.PassResult(visited, failed, last)
			}
			ran = true
			if err != nil {
				failed++
				last = err
				log.Warn().Err(err).Str("tenant", tenant).Msg("Rule stat sweep skipped tenant")
				break
			}
			if s.Deleted != nil {
				s.Deleted.Add(float64(n))
			}
			if n < RuleStatSweepBatch {
				break
			}
		}
		visited++
	}
	return core.PassResult(visited, failed, last)
}
