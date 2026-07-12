// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"reflect"
	"testing"
	"time"
)

// A raw-CEL threshold leaf is metric-scoped from its referenced measurements (review D4), so the
// runtime feeds it only events carrying one of them. A structured leaf already carries GateMetric
// and gets no FeedMetrics; a raw leaf that references m opaquely, or references no measurement,
// gets none either (fall back to feed-everything).
func TestCompileFeedMetricsThreshold(t *testing.T) {
	cases := []struct {
		name string
		when Condition
		want []string
	}{
		{"raw single metric", Condition{CEL: `"temp" in m && m["temp"] > 80.0`}, []string{"temp"}},
		{"raw two metrics", Condition{CEL: `("t" in m && m["t"] > 80.0) || ("h" in m && m["h"] < 10.0)`}, []string{"h", "t"}},
		{"raw opaque m use", Condition{CEL: `size(m) > 3`}, nil},
		{"raw no measurement", Condition{CEL: `device == "d1"`}, nil},
		{"structured gets none", Condition{Metric: "temp", Op: OpGt, Threshold: ptr(80)}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr, err := Compile(Rule{ID: "acme/r", Name: "n", Type: TypeThreshold, When: tc.when}, testLimits)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			if !reflect.DeepEqual(cr.FeedMetrics, tc.want) {
				t.Fatalf("FeedMetrics=%v, want %v", cr.FeedMetrics, tc.want)
			}
			// A structured threshold's GateMetric and a raw threshold's FeedMetrics are mutually
			// exclusive: whichever scopes the feed, only one is set.
			if cr.GateMetric != "" && len(cr.FeedMetrics) > 0 {
				t.Fatalf("GateMetric %q and FeedMetrics %v both set", cr.GateMetric, cr.FeedMetrics)
			}
		})
	}
}

// A raw-CEL duration leaf is scoped the same way (a non-matching event cancels the hold, so an
// off-metric false is just as harmful as for threshold).
func TestCompileFeedMetricsDuration(t *testing.T) {
	cr, err := Compile(Rule{
		ID: "acme/d", Name: "n", Type: TypeDuration,
		When: Condition{CEL: `"temp" in m && m["temp"] > 80.0`},
		Hold: Duration(2 * time.Minute),
	}, testLimits)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if want := []string{"temp"}; !reflect.DeepEqual(cr.FeedMetrics, want) {
		t.Fatalf("FeedMetrics=%v, want %v", cr.FeedMetrics, want)
	}
}

// The windowed/counting kinds observe their falling edge by aging, not by an immediate non-match,
// so they are deliberately left feed-everything even with a raw leaf — no FeedMetrics.
func TestCompileFeedMetricsOnlyThresholdAndDuration(t *testing.T) {
	rep, err := Compile(Rule{
		ID: "acme/rep", Name: "n", Type: TypeRepeating,
		When:   Condition{CEL: `"temp" in m && m["temp"] > 80.0`},
		Count:  3,
		Window: Duration(5 * time.Minute),
	}, testLimits)
	if err != nil {
		t.Fatalf("compile repeating: %v", err)
	}
	if rep.FeedMetrics != nil {
		t.Fatalf("repeating must not get FeedMetrics, got %v", rep.FeedMetrics)
	}
}
