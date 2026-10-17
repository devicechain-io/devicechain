// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/rs/zerolog/log"
)

// ruleStatRecorder is the BENCHMARK BASELINE's rule-health recorder: the inline write the
// detection loop made before the accumulator (internal/rulehealth) replaced it, one erasure-fence
// read and one upsert per published detection, on the loop. It lives in a test file because it
// is not an operator-facing option: it exists so variant A of the output-path comparison
// (detect_output_fixture_test.go) keeps meaning the path as it was. Like the original it is
// best-effort, logging and dropping a failed write, and it times every write into
// detect_rule_stat_write_seconds so the benchmark can split the loop's waits.
type ruleStatRecorder struct {
	store   fireStatStore
	metrics *DetectMetrics
}

// fireStatStore is the slice of model.RuleStatStore the recorder needs; a seam so a test can fail it.
type fireStatStore interface {
	RecordFire(ctx context.Context, ruleID, tenant string, at time.Time, edge string) error
}

var _ fireStatStore = (*model.RuleStatStore)(nil)

// RecordFire upserts the rule's stat row, swallowing (logging) any error. Its duration is
// observed either way: the loop waited for it whether it succeeded or not.
func (r ruleStatRecorder) RecordFire(ctx context.Context, ruleID, tenant string, at time.Time, edge string) {
	start := time.Now()
	err := r.store.RecordFire(ctx, ruleID, tenant, at, edge)
	r.metrics.recordRuleStatWrite(time.Since(start))
	if err != nil {
		r.metrics.recordRuleStatWriteFailure()
		log.Warn().Err(err).Str("rule", ruleID).
			Msg("Failed to record a rule-health fire stat; dropping (off the correctness path).")
	}
}
