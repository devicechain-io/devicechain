// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ruletest

import (
	"strings"
	"testing"
	"time"
)

// An expectation that names a rule the evaluation did not run is an ERROR, never a pass: a
// neverRaised on a misspelled rule would otherwise hold forever.
func TestExpectationOnARuleThatWasNotEvaluatedIsAnError(t *testing.T) {
	res := Result{Evaluated: []string{"hot"}, Skipped: map[string]string{"grouped": "scoped to the group north@2"}}
	for _, block := range []string{
		"  - neverRaised: {rule: hott}\n",
		"  - raised: {rule: hott}\n",
		"  - neverIntent: {rule: hott, kind: sendCommand}\n",
		"  - intent: {rule: hott}\n",
		"  - cleared: {rule: hott}\n",
	} {
		got := Judge(parseTest(t, block), res, start, time.Hour)[0]
		if got.Passed || !got.Errored || !strings.Contains(got.Message, "rule hott was not evaluated") ||
			!strings.Contains(got.Message, "hot") {
			t.Fatalf("%s: outcome = %+v, want an error naming the missing rule and the rules that ran", strings.TrimSpace(block), got)
		}
	}
	skipped := Judge(parseTest(t, "  - neverRaised: {rule: grouped}\n"), res, start, time.Hour)[0]
	if !skipped.Errored || !strings.Contains(skipped.Message, "rule grouped was skipped") || !strings.Contains(skipped.Message, "north@2") {
		t.Fatalf("skipped: %+v, want an error saying the rule was skipped and why", skipped)
	}
	// An expectation about any rule is still judged.
	if any := Judge(parseTest(t, "  - neverRaised: {}\n"), res, start, time.Hour)[0]; !any.Passed {
		t.Fatalf("any rule: %+v", any)
	}
}

// On a partial result, an absence or an exact count is inconclusive (an error) unless the
// expectation opts in with allowDegraded; a positive expectation is still judged.
func TestDegradedResultMakesNegativeAndCountedExpectationsInconclusive(t *testing.T) {
	res := Result{Evaluated: []string{"hot"}, Degradations: []Degradation{{Reason: "LATE_READINGS", Count: 3}},
		Degraded: "3 reading(s) arrived too far behind",
		Edges:    []Edge{{At: start.Add(time.Minute), Series: "d1", Rule: "hot", Raise: true}}}
	for _, block := range []string{
		"  - neverRaised: {rule: hot, device: d2}\n",
		"  - neverIntent: {rule: hot}\n",
		"  - raised: {rule: hot, count: 1}\n",
		"  - intent: {rule: hot, count: 0}\n",
		"  - cleared: {rule: hot, count: 0}\n",
	} {
		got := Judge(parseTest(t, block), res, start, time.Hour)[0]
		if got.Passed || !got.Errored || !strings.Contains(got.Message, "inconclusive") || !strings.Contains(got.Message, "3 reading(s)") {
			t.Fatalf("%s: outcome = %+v, want inconclusive", strings.TrimSpace(block), got)
		}
		allowed := Judge(parseTest(t, block+"    allowDegraded: true\n"), res, start, time.Hour)[0]
		if allowed.Errored {
			t.Fatalf("%s with allowDegraded: %+v, want it judged", strings.TrimSpace(block), allowed)
		}
	}
	if pos := Judge(parseTest(t, "  - raised: {rule: hot}\n"), res, start, time.Hour)[0]; !pos.Passed {
		t.Fatalf("a positive expectation must still be judged on a partial result: %+v", pos)
	}
	// Degradations alone, with no sentence, still make it inconclusive.
	bare := Result{Evaluated: []string{"hot"}, Degradations: []Degradation{{Reason: "INTENT_LIMIT", Count: 2}}}
	if got := Judge(parseTest(t, "  - neverIntent: {}\n"), bare, start, time.Hour)[0]; !got.Errored || !strings.Contains(got.Message, "INTENT_LIMIT (2)") {
		t.Fatalf("bare degradations: %+v", got)
	}
}
