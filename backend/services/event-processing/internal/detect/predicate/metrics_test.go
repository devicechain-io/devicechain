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
