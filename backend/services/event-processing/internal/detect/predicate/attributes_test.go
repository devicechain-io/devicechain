// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package predicate

import (
	"testing"
)

// TestTrueWithoutAttributes pins the analysis VALUE for each shape: true only for a leaf that is
// definitely true for every event from every device holding none of the attributes it reads.
// Every leaf compiles; whether a true answer is a refusal is rules.Compile's call, per kind.
func TestTrueWithoutAttributes(t *testing.T) {
	cases := []struct {
		src  string
		want bool
		why  string
	}{
		// Definitely true with attr empty, whatever the event and device.
		{`!("lim" in attr)`, true, "bare negated presence"},
		{`!("lim" in attr) || m["t"] > attr["lim"]`, true, "true || error is true"},
		{`!("lim" in attr) || ("t" in m && m["t"] > attr["lim"])`, true, "guarded disjunct: true || unknown"},
		{`m["t"] > 80.0 || !has(attr.lim)`, true, "has() macro, negation on the right"},
		{`size(attr) == 0`, true, "whole-map shape"},
		{`!("lim" in attr) || geo.inFence("yard")`, true, "geo held unknown"},
		{`cel.bind(x, "lim" in attr, !x || m["t"] > 1.0)`, true, "through a compute binding (the canvas path)"},

		// Still depends on the event or the device: not true.
		{`"lim" in attr && "t" in m && m["t"] > attr["lim"]`, false, "the structured generator's output"},
		{`!("lim" in attr) && "t" in m && m["t"] > 80.0`, false, "the fallback idiom (unknown)"},
		{`"t" in m && ("lim" in attr ? m["t"] > attr["lim"] : m["t"] > 80.0)`, false, "the recommended fallback"},
		{`!("lim" in attr) && !("t" in m)`, false, "m is held unknown, not bound empty"},
		{`attr["lim"] > 50.0`, false, "an eval error under empty attr is a runtime skip, not a firing"},
		{`!("lim" in attr) && device == "d1"`, false, "narrowed by identity: the documented limit"},
		{`"a" in attr && !("b" in attr)`, false, "per-key shape: the documented limit"},
		{`true`, false, "reads no attr, never analysed"},
		{`"t" in m && m["t"] > 80.0`, false, "a pure measurement test reads no attr"},
	}
	for _, c := range cases {
		p, err := Compile(c.src)
		if err != nil {
			t.Fatalf("%s (%s): must compile, got %v", c.src, c.why, err)
		}
		if got := p.TrueWithoutAttributes(); got != c.want {
			t.Errorf("TrueWithoutAttributes(%s) = %v, want %v (%s)", c.src, got, c.want, c.why)
		}
	}
}

// TestFallbackIdiomFiresOnAbsentAttribute pins the behaviour the documentation describes for the
// accepted fallback idiom: it fires on the fallback for a device without the attribute, and
// defers to the device's own value when it has one.
func TestFallbackIdiomFiresOnAbsentAttribute(t *testing.T) {
	p, err := Compile(`!("lim" in attr) && "t" in m && m["t"] > 0.0`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	got, err := p.Eval(Input{M: map[string]float64{"t": 1}})
	if err != nil || !got {
		t.Fatalf("no attribute: want (true, nil), got (%v, %v)", got, err)
	}
	got, err = p.Eval(Input{M: map[string]float64{"t": 1}, Attr: map[string]float64{"lim": 5}})
	if err != nil || got {
		t.Fatalf("attribute set: want (false, nil), got (%v, %v)", got, err)
	}
}
