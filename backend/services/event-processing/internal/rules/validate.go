// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

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
