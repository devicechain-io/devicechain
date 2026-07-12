// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"time"

	"gorm.io/datatypes"
)

// This file is the PURE, DB-free half of the ADR-057 alarm-object integrator: the level-state
// reduction that turns per-rule edge signals (raise/resolve) into the alarm's aggregate state. The
// DETECT engine emits edge-triggered signals per rule; the alarm object reference-counts the rules
// currently raising it (its CONTRIBUTOR SET), resolves severity as the max over that active set, and
// clears the alarm when the set empties. All of that logic lives here as pure functions over an
// in-memory map so it is exhaustively unit-testable (order-independence, resolve-wins-at-equal-ts,
// max-tier) without a database; the DB-facing half (api_alarm_contributor.go) loads/derives/persists.

// AlarmContributor is one rule's contribution to an alarm's level (ADR-057), serialized in the alarm
// row's contributor-set JSON column. A contributor is either ACTIVE (the rule is currently raising,
// carrying the tier it raised at) or a resolved TOMBSTONE (Active=false): the tombstone is retained,
// with the decision time of the resolve, so a later-arriving edge at the SAME event time cannot
// re-add the contributor (resolve-wins-at-equal-ts). Tombstones are bounded by the number of distinct
// rules that ever shared this alarm key (small); a lateness-horizon GC is a future refinement.
type AlarmContributor struct {
	// Tier is the AlarmSeverity this rule raised at (empty for a tombstone whose raise tier is no
	// longer needed). Only ACTIVE contributors' tiers participate in the alarm's presented severity.
	Tier string `json:"tier"`
	// DecisionTs is the OccurredTime of the LAST edge applied to this contributor — the monotonic
	// ordering key. An incoming edge older than this is ignored (a stale redelivery/replay).
	DecisionTs time.Time `json:"decisionTs"`
	// Active is true while the rule is raising, false once it has resolved (a tombstone).
	Active bool `json:"active"`
}

// contributorSet is the in-memory contributor map keyed by composed rule id, reduced from and
// serialized to the alarm row's JSON column.
type contributorSet map[string]AlarmContributor

// decodeContributors reads an alarm row's contributor JSON into a set. A NULL/empty column (a legacy
// measurement-evaluator alarm, or a never-populated row) yields a fresh empty set, not an error.
func decodeContributors(raw datatypes.JSON) (contributorSet, error) {
	cs := contributorSet{}
	if len(raw) == 0 {
		return cs, nil
	}
	if err := json.Unmarshal(raw, &cs); err != nil {
		return nil, err
	}
	return cs, nil
}

// encode serializes the set for the alarm row's JSON column. Go marshals a map with sorted string
// keys, so the bytes are deterministic for a given set — an order-independent set (canonical
// tombstones) serializes identically regardless of the edge order that produced it.
func (cs contributorSet) encode() (datatypes.JSON, error) {
	b, err := json.Marshal(cs)
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(b), nil
}

// apply folds one rule's edge into the set and reports whether the set changed. It is idempotent and
// ORDER-INDEPENDENT: for each rule the final state is a pure function of the edge with the maximum
// DecisionTs, with a RESOLVE winning a RAISE at an equal ts and, among equal-ts RAISES, the HIGHER tier
// winning — so processing any permutation (or a redelivered duplicate) of a rule's edges yields the
// same result regardless of arrival order (ADR-057 / RaiseAlarmRequest ordering contract). raised
// selects the edge kind; tier is the raise tier (ignored for a resolve).
//
//   - A stale edge (ts strictly before the stored decision time) is ignored.
//   - A RAISE at a ts EQUAL to a stored resolved tombstone is ignored — resolve wins the tie.
//   - Among equal-ts RAISES the higher tier (lower Rank) wins — deterministic even if a buggy producer
//     emits two tiers for one rule at one instant (a well-formed versioned rule has a single tier).
//   - Otherwise the contributor is set: a raise makes it active at its tier; a resolve tombstones it.
func (cs contributorSet) apply(ruleID string, raised bool, tier string, ts time.Time) (changed bool) {
	cur, exists := cs[ruleID]
	if exists && cur.DecisionTs.After(ts) {
		return false // stale: a newer edge already decided this contributor
	}
	if raised {
		if exists && cur.DecisionTs.Equal(ts) && !cur.Active {
			return false // equal-ts tombstone: resolve wins, the raise does not re-add
		}
		if exists && cur.Active && cur.DecisionTs.Equal(ts) {
			// Equal-ts re-raise of an active contributor: keep the HIGHER tier (lower Rank) so the
			// outcome is order-independent. An equal tier (an idempotent redelivery, dedup'd by
			// .Equal so a non-UTC-offset duplicate still matches), a lower tier, or an unknown-rank
			// tier is a no-op. Only a strictly-higher tier updates.
			ir, cr := AlarmSeverity(tier).Rank(), AlarmSeverity(cur.Tier).Rank()
			if ir < 0 || cr <= ir {
				return false
			}
		}
		cs[ruleID] = AlarmContributor{Tier: tier, DecisionTs: ts, Active: true}
		return true
	}
	// Resolve → tombstone. Retain the (now inactive) contributor with the resolve's ts so an equal-ts
	// raise can't re-add it. The tier is CLEARED (a tombstone's tier is never read — activeSeverity
	// skips inactive contributors, and a later reactivation overwrites it with a fresh raise tier), so
	// dropping it makes the serialized set canonical: two orderings that reach the same active set also
	// serialize to the same bytes. A re-resolve at the same ts of an already-inactive contributor is a
	// no-op.
	if exists && !cur.Active && cur.DecisionTs.Equal(ts) {
		return false
	}
	cs[ruleID] = AlarmContributor{DecisionTs: ts, Active: false}
	return true
}

// activeSeverity returns the presented severity of the set — the MAX tier (lowest Rank, ADR-041) over
// the ACTIVE contributors — and whether any contributor is active. An empty/all-tombstone set has no
// active contributor: the alarm clears. An active contributor whose tier is unknown (Rank < 0) is
// skipped for severity but still counts as active (it keeps the alarm raised) — a forged tier cannot
// silently clear a live alarm.
func (cs contributorSet) activeSeverity() (severity string, anyActive bool) {
	bestRank := -1
	for _, c := range cs {
		if !c.Active {
			continue
		}
		anyActive = true
		r := AlarmSeverity(c.Tier).Rank()
		if r < 0 {
			continue
		}
		if bestRank < 0 || r < bestRank {
			bestRank, severity = r, c.Tier
		}
	}
	return severity, anyActive
}
