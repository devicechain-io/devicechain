// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import "time"

// checkDurationCeiling rejects a rule whose temporal extent exceeds the compile budget's
// MaxRuleDuration. It checks the AUTHORED fields rather than the lowered core rule, and Compile
// calls it ONCE for every rule type rather than from each per-type lowering — so a new rule kind
// inherits the ceiling instead of having to remember it. That is precisely the failure mode that
// left this unbounded: `forbid` below only ever asserts a duration is UNSET on the kinds that do
// not use it, and no per-type lowering ever asserted an upper bound on the kinds that do.
//
// Field names match the authored JSON keys (and `forbid`'s), so the console anchors the error to
// the input the author actually typed.
func checkDurationCeiling(r Rule, max time.Duration) error {
	for _, c := range []struct {
		d     time.Duration
		field string
	}{
		{r.Window.D(), "window"},
		{r.Hold.D(), "hold"},
		{r.Ttl.D(), "timeout"},
		{r.Gap.D(), "gap"},
	} {
		if c.d > max {
			return invalid(r.ID, c.field, "%s exceeds the maximum rule duration of %s", c.d, max)
		}
	}
	return nil
}

// forbidden marks which schema fields a rule type does NOT use. Compile rejects a rule
// that sets any forbidden field (fail-closed on an ill-formed rule, matching the project's
// "reject unknown/invalid keys" convention) — this is the guard that catches a form or a
// canvas node emitting a parameter onto the wrong rule type. A zero-valued field is
// indistinguishable from unset and so passes; the check catches the meaningful case of a
// stray non-zero parameter.
type forbidden struct {
	leaf      bool // the When condition
	value     bool // the Metric value selector
	window    bool
	hold      bool
	timeout   bool
	gap       bool
	count     bool
	rate      bool
	mode      bool
	agg       bool
	op        bool
	threshold bool
	anchor    bool
	memberCap bool
}

// forbid returns a ValidationError for the first forbidden field the rule sets.
func forbid(r Rule, ruleType string, f forbidden) error {
	checks := []struct {
		on    bool
		set   bool
		field string
	}{
		{f.leaf, !r.When.isZero(), "when"},
		{f.value, r.Metric != "", "metric"},
		{f.window, r.Window.D() != 0, "window"},
		{f.hold, r.Hold.D() != 0, "hold"},
		{f.timeout, r.Ttl.D() != 0, "timeout"},
		{f.gap, r.Gap.D() != 0, "gap"},
		{f.count, r.Count != 0, "count"},
		{f.rate, r.Rate, "rate"},
		{f.mode, r.Mode != "", "windowMode"},
		{f.agg, r.Agg != "", "agg"},
		{f.op, r.Op != "", "op"},
		{f.threshold, r.Threshold != nil, "threshold"},
		{f.anchor, r.AnchorType != "", "anchorType"},
		{f.memberCap, r.MemberCap != 0, "memberCap"},
	}
	for _, c := range checks {
		if c.on && c.set {
			return invalid(r.ID, c.field, "%s does not use this field", ruleType)
		}
	}
	return nil
}
