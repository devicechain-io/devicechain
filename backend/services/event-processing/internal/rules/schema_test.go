// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"encoding/json"
	"testing"
	"time"
)

// TestDurationCanonicalization proves a Duration parses any Go duration string but always
// re-marshals to the canonical form — so `"300s"` and `"5m"` both round-trip to `"5m0s"`.
// This is the property the form/canvas byte-identity contract (ADR-053) leans on.
func TestDurationCanonicalization(t *testing.T) {
	for _, in := range []string{`"300s"`, `"5m"`, `"5m0s"`} {
		var d Duration
		if err := json.Unmarshal([]byte(in), &d); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
		if d.D() != 5*time.Minute {
			t.Fatalf("unmarshal %s = %v, want 5m", in, d.D())
		}
		out, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != `"5m0s"` {
			t.Fatalf("canonical marshal = %s, want \"5m0s\"", out)
		}
	}
}

// TestRuleJSONByteIdentity proves an authored rule serializes canonically: marshal →
// unmarshal → re-marshal yields byte-identical JSON. A form-authored and a canvas-authored
// rule that decode to the same Rule therefore re-encode identically — the load-bearing
// property that lets both authoring surfaces target one schema.
func TestRuleJSONByteIdentity(t *testing.T) {
	r := Rule{
		ID: "r1", Name: "hot-average", Description: "avg temp over 5m",
		Type: TypeAggregate, Mode: ModeTumbling, Metric: "temperature",
		Agg: AggAvg, Op: OpGt, Threshold: ptr(30), Window: Duration(5 * time.Minute),
		When: Condition{Metric: "temperature", Op: OpGe, Threshold: ptr(0)},
	}
	first, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back Rule
	if err := json.Unmarshal(first, &back); err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(back)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("round-trip not byte-identical:\n first=%s\nsecond=%s", first, second)
	}
	// And the decoded rule must still compile — the contract survives serialization.
	if _, err := Compile(back, testLimits); err != nil {
		t.Fatalf("round-tripped rule should compile: %v", err)
	}
}
