// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"reflect"
	"testing"
	"time"
)

// TestCompileWarnsOnNegatedAttributeGuard pins the advisory: a threshold or duration condition that
// ANDs an absence test for an attribute compiles (the fallback idiom is deliberate) and carries a
// stable warning naming the attribute. The positive form and unrelated negations carry none, and a
// gate kind keeps its "not in maintenance" filter silent.
func TestCompileWarnsOnNegatedAttributeGuard(t *testing.T) {
	const fallback = `!("lim" in attr) && "t" in m && m["t"] > 80.0`
	warned := map[string]Rule{
		"threshold": {ID: "a", Name: "n", Type: TypeThreshold, When: Condition{CEL: fallback}},
		"duration": {ID: "b", Name: "n", Type: TypeDuration, Hold: Duration(time.Minute),
			When: Condition{CEL: fallback}},
		"has() macro": {ID: "c", Name: "n", Type: TypeThreshold,
			When: Condition{CEL: `!has(attr.lim) && "t" in m && m["t"] > 80.0`}},
	}
	for name, r := range warned {
		cr, err := Compile(r, testLimits)
		if err != nil {
			t.Fatalf("%s: must compile, got %v", name, err)
		}
		want := []string{"lim"}
		if len(cr.Warnings) != 1 || cr.Warnings[0].Code != WarnNegatedAttributeGuard ||
			!reflect.DeepEqual(cr.Warnings[0].Params, want) || cr.Warnings[0].Message == "" {
			t.Errorf("%s: want one %s warning with params %v, got %+v", name, WarnNegatedAttributeGuard, want, cr.Warnings)
		}
	}

	silent := map[string]Rule{
		"guarded positive form": {ID: "d", Name: "n", Type: TypeThreshold,
			When: Condition{CEL: `("lim" in attr) && "t" in m && m["t"] > attr["lim"]`}},
		"unrelated negation": {ID: "e", Name: "n", Type: TypeThreshold,
			When: Condition{CEL: `!("x" in m) || m["t"] > 80.0`}},
		"structured dynamic threshold": {ID: "f", Name: "n", Type: TypeThreshold,
			When: Condition{Metric: "t", Op: OpGt, ThresholdAttr: "lim"}},
		"repeating gate": {ID: "g", Name: "n", Type: TypeRepeating, Count: 5, Window: Duration(10 * time.Minute),
			When: Condition{CEL: `!("maint" in attr) && "t" in m`}},
	}
	for name, r := range silent {
		cr, err := Compile(r, testLimits)
		if err != nil {
			t.Fatalf("%s: must compile, got %v", name, err)
		}
		if len(cr.Warnings) != 0 {
			t.Errorf("%s: want no warnings, got %+v", name, cr.Warnings)
		}
	}
}
