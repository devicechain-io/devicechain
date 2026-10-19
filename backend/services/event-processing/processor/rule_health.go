// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rulehealth"
	"github.com/rs/zerolog/log"
)

// The rule-health fire counters, per leadership term (internal/rulehealth has the mechanism).
//
// A term owns one accumulator. buildTerm starts it before the replay, because a replay can
// checkpoint and every detection it publishes counts as a fire; the term's end stops it. The
// publisher's recorder is the current term's accumulator and nothing else, so a fire can only
// be admitted into the term that published it, and once a term's accumulator is stopped it
// refuses every admission. A new term starts empty.
//
// What the end of a term does with what is pending depends on why it ended:
//
//   - A GRACEFUL STOP (ExecuteStop) makes one bounded final flush, AFTER the lease is released
//     and the final checkpoint is saved, so neither waits on it. Its budget is the configured
//     write timeout, shortened to leave ruleHealthStopHeadroom of the process's own shutdown
//     deadline when one is set.
//   - LEADERSHIP LOSS discards what is pending at once and cancels a write in flight. The
//     successor counts its own fires; the lost ones are counted as discarded (reason
//     shutdown) and the health count is approximate by contract.

// ruleHealthStopHeadroom is how much of the shutdown deadline the final flush leaves unused.
const ruleHealthStopHeadroom = 3 * time.Second

// ruleHealthConfig is the configured accumulator bounds, or the defaults when none were given.
func (rp *ResolvedEventsProcessor) ruleHealthConfig() rulehealth.Config {
	if rp.cfg.RuleHealth == (rulehealth.Config{}) {
		return rulehealth.DefaultConfig()
	}
	return rp.cfg.RuleHealth
}

// startRuleHealth starts this term's accumulator and makes it the publisher's recorder. With no
// store it does nothing. A previous term's accumulator still present is stopped first, without
// a flush: endTerm normally stops it, and one left behind means its term never got that far.
func (rp *ResolvedEventsProcessor) startRuleHealth() error {
	if rp.ruleStats == nil {
		return nil
	}
	rp.stopRuleHealth(false, context.Background())
	h, err := rulehealth.New(rp.ruleStats, rp.ruleHealthConfig(), rp.metrics)
	if err != nil {
		return fmt.Errorf("event-processing: rule-health counters: %w", err)
	}
	h.Start()
	rp.health = h
	rp.publisher.SetFireRecorder(h)
	return nil
}

// stopRuleHealth ends this term's accumulator: a bounded final flush when graceful, an
// immediate discard otherwise. It joins the accumulator's worker before returning. Nil-safe.
func (rp *ResolvedEventsProcessor) stopRuleHealth(graceful bool, parent context.Context) {
	h := rp.health
	if h == nil {
		return
	}
	rp.health = nil
	rp.publisher.SetFireRecorder(nil)
	ctx, cancel := rp.ruleHealthStopContext(parent)
	defer cancel()
	if dropped := h.Stop(ctx, graceful); dropped > 0 {
		log.Info().Int64("fires", dropped).Bool("graceful", graceful).Str("partition", rp.cfg.PartitionId).
			Msg("Rule-health fire counts still pending when the DETECT term ended were discarded; the " +
				"console's fire counts are short by this many (detection is unaffected).")
	}
}

// ruleHealthStopContext bounds the final flush: the write timeout, and no later than
// ruleHealthStopHeadroom before the process's shutdown deadline or parent's own deadline.
func (rp *ResolvedEventsProcessor) ruleHealthStopContext(parent context.Context) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(rp.ruleHealthConfig().WriteTimeout)
	if n := rp.stopDeadline.Load(); n != 0 {
		if d := time.Unix(0, n).Add(-ruleHealthStopHeadroom); d.Before(deadline) {
			deadline = d
		}
	}
	if d, ok := parent.Deadline(); ok {
		if d = d.Add(-ruleHealthStopHeadroom); d.Before(deadline) {
			deadline = d
		}
	}
	return context.WithDeadline(context.WithoutCancel(parent), deadline)
}

// purgeRuleHealth removes a purged tenant's pending fire counts and settles a write of that
// tenant already in flight, on the loop, before the eviction is answered. It returns how many
// entries it removed (each one is the tenant's rule id held in memory; an in-flight write
// adds the rules of its batch not yet confirmed written, so a write that may have landed makes
// the coordinator sweep again). An error means the in-flight write did not settle in time: the eviction must not be
// reported clean.
func (rp *ResolvedEventsProcessor) purgeRuleHealth(tenant string) (int64, error) {
	if rp.health == nil {
		return 0, nil
	}
	// The write in flight is cancelled, so it ends at once on a driver that honours
	// cancellation; the write timeout is the most it could run in any case.
	ctx, cancel := context.WithTimeout(rp.pctx(), rp.ruleHealthConfig().WriteTimeout+time.Second)
	defer cancel()
	n, err := rp.health.Purge(ctx, tenant)
	if err != nil {
		return n, fmt.Errorf("the rule-health counters could not settle a write in flight for this tenant, "+
			"so it may still land: %w", err)
	}
	return n, nil
}
