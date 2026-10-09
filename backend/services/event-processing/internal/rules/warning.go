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

// WarnNegatedAttributeGuardDisjunct is the code of the worse shape: the absence test is a disjunct
// (`"t" in m && (!("k" in attr) || m["t"] > attr["k"])`), so the condition is true for every device
// without the attribute whatever its reading, with no threshold at all. Params: [attribute].
const WarnNegatedAttributeGuardDisjunct = "negatedAttributeGuardDisjunct"

// compileWarnings returns the advisory findings for a compiled leaf.
//
// It applies to EVERY rule kind. `attr` holds numbers only, so `!("maint" in attr)` is also true for a
// device whose flag was set as a bool, a string or with CLIENT scope: the "not in maintenance" gate on a
// repeating or aggregate rule is the same trap as the alarm condition, only quieter.
func compileWarnings(pred *predicate.Predicate) []Warning {
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
	for _, k := range pred.DisjunctiveAttributeGuards() {
		out = append(out, Warning{
			Code:   WarnNegatedAttributeGuardDisjunct,
			Params: []string{k},
			Message: fmt.Sprintf("this condition is true whenever attribute %q is NOT set, whatever the reading, so it "+
				"fires for every device without it and no threshold applies to them — including devices where it was "+
				"never set, was set to something other than a number, or was set with CLIENT scope.", k),
		})
	}
	return out
}
