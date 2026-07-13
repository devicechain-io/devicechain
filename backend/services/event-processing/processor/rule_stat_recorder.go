// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/rs/zerolog/log"
)

// ruleStatRecorder is the processor-side runtime.FireRecorder: it persists each durably-
// published detection into the RuleStat projection for the console's rule-health view
// (ADR-051 slice 7b). It runs on the publish hot path, so it is deliberately best-effort —
// a stat-write failure is logged and dropped, NEVER returned, because it must not turn a
// successfully-published detection into a retryable error or wedge the checkpoint loop. The
// projection is a counting read-model, off the correctness path; a dropped fire only skews an
// approximate health number (and a replay re-records it).
type ruleStatRecorder struct {
	store *model.RuleStatStore
}

// RecordFire upserts the rule's stat row, swallowing (logging) any error.
func (r ruleStatRecorder) RecordFire(ctx context.Context, ruleID, tenant string, at time.Time, edge string) {
	if err := r.store.RecordFire(ctx, ruleID, tenant, at, edge); err != nil {
		log.Warn().Err(err).Str("rule", ruleID).
			Msg("Failed to record a rule-health fire stat; dropping (off the correctness path).")
	}
}
