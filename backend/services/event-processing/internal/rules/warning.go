// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"fmt"

	"github.com/devicechain-io/dc-event-processing/internal/detect/predicate"
)

// Warning is one non-fatal authoring finding on a rule that compiled. Unlike a ValidationError it
// never rejects: the rule is accepted and evaluates exactly as it would without the warning. The
// pair (Code, Params) is the stable contract — the console localises its own text from the code and
// interpolates the params, so Message is only the English fallback for a client that does not know
// the code (dcctl, an SDK, a log line).
type Warning struct {
	// Code is a stable, lowerCamel identifier. Adding a code is additive; renaming one breaks
	// every client's localised text.
	Code string
	// Params are the positional values the message interpolates, in a fixed order per code.
	Params []string
	// Message is the English fallback text.
	Message string
}

// WarnNegatedAttributeGuard is the code of a leaf that carries an absence test for a device
// attribute joined by && (`!("k" in attr) && m["t"] > 80.0`). Params: [attribute].
const WarnNegatedAttributeGuard = "negatedAttributeGuard"

// compileWarnings returns the advisory findings for a compiled leaf.
//
// The negated-guard warning is raised for threshold and duration only — the kinds whose leaf IS the
// alarm condition, the same two kinds ErrTrueWithoutAttributes refuses for. On every other kind the
// leaf is an optional per-event gate, where `!("maint" in attr)` is the ordinary "not in
// maintenance" filter and a warning would be noise on the idiom the gate exists to express.
func compileWarnings(t RuleType, pred *predicate.Predicate) []Warning {
	if t != TypeThreshold && t != TypeDuration {
		return nil
	}
	var out []Warning
	for _, k := range pred.NegatedAttributeGuards() {
		out = append(out, Warning{
			Code:   WarnNegatedAttributeGuard,
			Params: []string{k},
			Message: fmt.Sprintf("this condition tests that attribute %q is NOT set, so the rule applies to every device "+
				"without it — including devices where it was never set, was set to something other than a number, "+
				"or was set with CLIENT scope. If you meant only the devices configured without it, say so with another test.", k),
		})
	}
	return out
}
