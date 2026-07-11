// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"

	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/rs/zerolog/log"
)

// storeRuleResolver resolves a rule's REACT layer for the dispatcher (ADR-051 slice 5b) by reading
// the authoritative rule Definition from the durable DetectRule projection and decoding it — the
// SAME projection the engine's rule set is rebuilt from, so the actions REACT dispatches always
// match the rule the engine ran, and an action-chain edit takes effect without re-publishing events.
// A per-detection point read (by primary key) is cheap: detections are rare relative to events, and
// reading fresh avoids a cache-staleness window on a reused-id definition change. A cache is a
// deferred perf optimization, not a correctness need.
type storeRuleResolver struct {
	store *model.DetectRuleStore
}

// NewStoreRuleResolver builds a resolver over the durable rule projection (the REACT dispatcher's
// rule-resolution seam, wired in main). It returns the dispatcher's resolver interface so the
// concrete adapter stays package-private.
func NewStoreRuleResolver(store *model.DetectRuleStore) react.RuleResolver {
	return &storeRuleResolver{store: store}
}

// Resolve loads and decodes the rule for a composed runtime id. found=false means the rule is no
// longer persisted (removed after its detection fired — an orphan the dispatcher drops). A store
// error is returned so the dispatcher retries rather than treating a transient read failure as
// "rule gone". A row whose stored Definition no longer decodes (a hand-edited/forged projection
// row) is treated as not-found: there is nothing dispatchable, and a retry cannot fix a permanent
// decode failure — the same fail-closed drop the compile path takes for an undecodable rule.
func (r *storeRuleResolver) Resolve(ctx context.Context, ruleID string) (rules.Rule, bool, error) {
	row, found, err := r.store.LoadByID(ctx, ruleID)
	if err != nil {
		return rules.Rule{}, false, err
	}
	if !found {
		return rules.Rule{}, false, nil
	}
	rule, err := rules.Decode([]byte(row.Definition))
	if err != nil {
		// A permanently-undecodable stored rule (a forged/hand-edited projection row, or a rolling-
		// deploy skew where an older REACT binary's strict decoder rejects a field a newer gate
		// accepted) is dropped — a retry cannot fix it — but logged LOUDLY so a lost action chain is
		// visible, not silently indistinguishable from a benign rule deletion (which also drops).
		log.Error().Err(err).Str("rule", ruleID).
			Msg("REACT dropping a derived event: its rule's stored definition no longer decodes (actions lost; likely a schema/deploy skew).")
		return rules.Rule{}, false, nil
	}
	return rule, true, nil
}

// compile-time assertion that the adapter satisfies the dispatcher's resolver contract.
var _ react.RuleResolver = (*storeRuleResolver)(nil)
