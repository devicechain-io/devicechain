// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package predicate

import (
	"reflect"
	"testing"
)

// TestMetricRefsComplete covers the forms that pin a constant metric key: index, field select,
// the `in` membership test, the `has()` macro, and combinations. Each yields a sorted,
// de-duplicated, COMPLETE reference set the runtime can scope the feed on.
func TestMetricRefsComplete(t *testing.T) {
	cases := []struct {
		src  string
		want []string
	}{
		{`m["temp"] > 30.0`, []string{"temp"}},
		{`"temp" in m && m["temp"] > 30.0`, []string{"temp"}},
		{`m.temp > 30.0`, []string{"temp"}},
		{`has(m.temp) && m.temp > 30.0`, []string{"temp"}},
		{`m["temp"] > 30.0 && m["hum"] < 20.0`, []string{"hum", "temp"}},
		{`m["temp"] > 30.0 || m["temp"] < -5.0`, []string{"temp"}}, // dup collapses
		// A leaf that reads no measurement at all is COMPLETE with an EMPTY set (it applies to
		// every event) — the caller reads len==0 as "do not scope".
		{`device == "d1"`, nil},
		{`attr["limit"] > 5.0`, nil},
		// A dynamic bound compared to a constant-keyed metric is still fully scoped on m.
		{`"temp" in m && m["temp"] > attr["limit"]`, []string{"temp"}},
	}
	for _, tc := range cases {
		p := mustCompile(t, tc.src)
		got, complete := p.MetricRefs()
		if !complete {
			t.Errorf("%q: complete=false, want true", tc.src)
		}
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: metrics=%v, want %v", tc.src, got, tc.want)
		}
	}
}

// TestMetricRefsIncomplete covers every way a leaf touches `m` opaquely: a dynamic key, a
// whole-map operation, or a comprehension. The reference set cannot be trusted, so complete is
// false and the caller must fall back to feeding every event rather than risk dropping a raise.
func TestMetricRefsIncomplete(t *testing.T) {
	cases := []string{
		`m[device] > 30.0`,          // dynamic index key
		`size(m) > 3`,               // whole-map operation
		`m.exists(k, m[k] > 100.0)`, // comprehension iterating m
		`m.all(k, m[k] >= 0.0)`,     // comprehension iterating m
	}
	for _, src := range cases {
		p := mustCompile(t, src)
		if _, complete := p.MetricRefs(); complete {
			t.Errorf("%q: complete=true, want false (opaque m use)", src)
		}
	}
}

// TestMetricRefsMixedOpaque proves a single opaque use taints the whole result even when other
// references are constant-keyed: a leaf that reads m["temp"] AND size(m) is not scopeable.
func TestMetricRefsMixedOpaque(t *testing.T) {
	p := mustCompile(t, `m["temp"] > 30.0 && size(m) > 1`)
	if _, complete := p.MetricRefs(); complete {
		t.Fatalf("mixed constant + opaque use must report complete=false")
	}
}

// TestScopableMetricsSound is the review-D4 soundness guard: a leaf's feed may be metric-scoped
// ONLY when the leaf is provably false without those metrics, so scoping never drops a raise. The
// guarded-conjunction forms (the intended D4 target) are scopable; every shape that can be TRUE on
// a measurement-less event — via attr, device, a negated presence, or a disjunction — is NOT.
func TestScopableMetricsSound(t *testing.T) {
	scopable := []struct {
		src  string
		want []string
	}{
		{`"temp" in m && m["temp"] > 80.0`, []string{"temp"}},
		{`has(m.temp) && m.temp > 80.0`, []string{"temp"}},
		{`"temp" in m && m["temp"] > attr["lim"]`, []string{"temp"}}, // dynamic bound, still false w/o temp
		{`("t" in m && m["t"] > 80.0) || ("h" in m && m["h"] < 10.0)`, []string{"h", "t"}},
		{`("temp" in m && m["temp"] > 80.0) && attr["x"] > 0.0`, []string{"temp"}}, // AND: false absorbs
	}
	for _, tc := range scopable {
		p := mustCompile(t, tc.src)
		got, ok := p.ScopableMetrics()
		if !ok {
			t.Errorf("%q: ScopableMetrics ok=false, want scopable %v", tc.src, tc.want)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: scopable metrics=%v, want %v", tc.src, got, tc.want)
		}
	}

	// Each of these references a metric yet can be TRUE on a metric-less event — scoping would drop
	// a raise. They must NOT be scopable (the exact HIGH the D4 review reproduced).
	notScopable := []string{
		`("temp" in m && m["temp"] > 80.0) || attr["override"] > 0.0`, // true via attr on a metric-less event
		`!("temp" in m)`, // "raise when telemetry lacks temp"
		`device == "d1" ? true : m["temp"] > 80.0`,   // m only in the else branch
		`m["temp"] > 80.0 || attr["override"] > 0.0`, // OR absorbs the missing-key error
		`("temp" in m) == false`,                     // negation as equality
		`m["temp"] > 80.0`,                           // unguarded: errors→skips, needs no scope
		`size(m) > 3`,                                // opaque m use (incomplete refs)
		`device == "d1"`,                             // references no measurement at all
	}
	for _, src := range notScopable {
		p := mustCompile(t, src)
		if metrics, ok := p.ScopableMetrics(); ok {
			t.Errorf("%q: ScopableMetrics ok=true (metrics=%v) — MUST NOT scope (can raise without its metrics)", src, metrics)
		}
	}
}
