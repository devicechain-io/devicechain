// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"strings"
	"testing"
	"time"
)

// A leaf that tests a geofence AND a measurement is refused at compile, because the runtime's two
// feed scopes intersect at the empty set: a location event carries no measurement and a
// measurement event carries no position. Before this refusal such a rule published clean and then
// evaluated nothing forever, which reads exactly like a condition that never happened.
//
// The cases below are the three distinct routes to the conflict: the value selector a rule
// declares in its own shape, the FeedMetrics a raw leaf earns, and a bare `m` reference that earns
// no scope at all. (There is no structured-gate case, because a structured `when` and raw CEL are
// mutually exclusive and a fence can only arrive as raw CEL — see errFenceAndMetricLeaf.)
func TestFenceAndMetricLeafIsRefused(t *testing.T) {
	cases := []struct {
		name string
		rule Rule
		// because is the fragment of the message that must name WHY this rule conflicts. It is
		// asserted so a future refactor cannot collapse the branches onto one vague string: an
		// author who cannot tell which half of their rule is the measurement cannot fix it.
		because string
	}{
		{
			// The value selector is a top-level rule field, so it survives alongside a raw `when`
			// — and valueGuardedLeaf prepends its presence guard to the fence call, producing the
			// conflict from two halves the author never wrote next to each other.
			name: "value-selector metric beside a raw fence gate",
			rule: Rule{
				ID: "acme/value", Name: "n", Type: TypeDeltaRate,
				Metric: "temp", Op: OpGt, Threshold: ptr(5),
				When: Condition{CEL: `geo.inFence("yard")`},
			},
			because: `measurement "temp"`,
		},
		{
			name: "raw leaf that earned a metric scope",
			rule: Rule{
				ID: "acme/feed", Name: "n", Type: TypeThreshold,
				When: Condition{CEL: `geo.inFence("yard") && "temp" in m && m["temp"] > 80.0`},
			},
			because: "measurement(s) temp",
		},
		{
			name: "raw leaf reading m with no scope earned",
			rule: Rule{
				ID: "acme/opaque", Name: "n", Type: TypeThreshold,
				When: Condition{CEL: `geo.inFence("yard") && size(m) > 3`},
			},
			because: "reads `m`",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(tc.rule, testLimits)
			if err == nil {
				t.Fatal("a rule mixing a geofence with a measurement compiled")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.because) {
				t.Errorf("message does not say which measurement conflicts (want %q): %s", tc.because, msg)
			}
			// The message has to tell the author what to DO. A refusal that only states the
			// prohibition leaves them editing at random.
			if !strings.Contains(msg, "Split it into") {
				t.Errorf("message offers no way forward: %s", msg)
			}
		})
	}
}

// NEGATIVE CONTROL. The refusal is about the COMBINATION, and each half alone must still compile —
// otherwise the test above passes just as well against a compiler that rejects every geofence rule
// or every measurement rule, and the feature is gone rather than guarded.
func TestFenceAloneAndMetricAloneStillCompile(t *testing.T) {
	fenceOnly, err := Compile(Rule{
		ID: "acme/fence", Name: "n", Type: TypeThreshold,
		When: Condition{CEL: `geo.inFence("yard")`},
	}, testLimits)
	if err != nil {
		t.Fatalf("a fence-only rule was refused: %v", err)
	}
	if !fenceOnly.RequiresPosition {
		t.Error("a fence-only rule is not position-scoped, so the conflict check was never reached")
	}
	if fenceOnly.GateMetric != "" || fenceOnly.ValueMetric != "" || len(fenceOnly.FeedMetrics) > 0 {
		t.Errorf("a fence-only rule carries a metric gate: gate=%q value=%q feed=%v",
			fenceOnly.GateMetric, fenceOnly.ValueMetric, fenceOnly.FeedMetrics)
	}

	metricOnly, err := Compile(Rule{
		ID: "acme/metric", Name: "n", Type: TypeThreshold,
		When: Condition{CEL: `"temp" in m && m["temp"] > 80.0`},
	}, testLimits)
	if err != nil {
		t.Fatalf("a measurement-only rule was refused: %v", err)
	}
	if metricOnly.RequiresPosition {
		t.Error("a measurement-only rule is position-scoped")
	}
	if len(metricOnly.FeedMetrics) == 0 {
		t.Error("a measurement-only rule earned no metric scope, so the conflict check was never reached")
	}

	// And a rule that touches neither — the check must not fire on the ordinary case it has no
	// business seeing.
	if _, err := Compile(Rule{
		ID: "acme/plain", Name: "n", Type: TypeThreshold,
		When: Condition{CEL: `device == "d1"`},
	}, testLimits); err != nil {
		t.Fatalf("a rule with neither a fence nor a measurement was refused: %v", err)
	}
}

// The conflict is refused for EVERY rule kind, not just the two the metric scope applies to. The
// windowed and counting kinds are deliberately left feed-everything (no FeedMetrics), so they
// reach the conflict only through the bare-`m` branch — which is exactly the branch a narrower
// check would have omitted, leaving those kinds silently dead.
func TestFenceAndMetricRefusedForWindowedKinds(t *testing.T) {
	for _, r := range []Rule{
		{
			ID: "acme/rep", Name: "n", Type: TypeRepeating,
			When:   Condition{CEL: `geo.inFence("yard") && "temp" in m && m["temp"] > 80.0`},
			Count:  3,
			Window: Duration(5 * time.Minute),
		},
		{
			ID: "acme/dur", Name: "n", Type: TypeDuration,
			When: Condition{CEL: `geo.inFence("yard") && "temp" in m && m["temp"] > 80.0`},
			Hold: Duration(2 * time.Minute),
		},
	} {
		if _, err := Compile(r, testLimits); err == nil {
			t.Errorf("%s: a rule mixing a geofence with a measurement compiled", r.Type)
		}
	}
}
