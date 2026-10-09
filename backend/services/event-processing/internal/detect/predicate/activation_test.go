// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package predicate

import (
	"strings"
	"testing"
	"time"
)

// A rule that reads something the input does not carry must behave exactly as it did when the
// activation was a map: a presence-guarded read is a clean false, an unguarded read of an absent
// key is an evaluation error (no such key) — never a default — and a nil map is an empty one.
func TestActivationMissingFieldsBehaveAsEmptyMaps(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		cel     string
		in      Input
		want    bool
		wantErr string
	}{
		{"nil M, guarded in", `"t" in m`, Input{}, false, ""},
		{"nil Attr, guarded in", `"t" in attr`, Input{}, false, ""},
		{"nil Anchors, guarded in", `"site" in anchors`, Input{}, false, ""},
		{"nil M, size", `size(m) == 0`, Input{}, true, ""},
		{"nil Attr, size", `size(attr) == 0`, Input{}, true, ""},
		{"nil Anchors, size", `size(anchors) == 0`, Input{}, true, ""},
		{"empty (non-nil) M, guarded read", `"t" in m && m["t"] > 1.0`, Input{M: map[string]float64{}}, false, ""},
		{"absent key, unguarded read", `m["t"] > 1.0`, Input{M: map[string]float64{"x": 1}}, false, "no such key"},
		{"nil M, unguarded read", `m["t"] > 1.0`, Input{}, false, "no such key"},
		{"nil Attr, unguarded read", `m["t"] > attr["t"]`, Input{M: map[string]float64{"t": 2}}, false, "no such key"},
		{"present key", `m["t"] > 1.0`, Input{M: map[string]float64{"t": 2}}, true, ""},
		{"present anchor", `anchors["site"] == "s1"`, Input{Anchors: map[string]string{"site": "s1"}}, true, ""},
		{"present attr", `attr["t"] == 3.0`, Input{Attr: map[string]float64{"t": 3}}, true, ""},
		{"zero device", `device == ""`, Input{}, true, ""},
		{"zero time", `occurred == timestamp("0001-01-01T00:00:00Z")`, Input{}, true, ""},
		{"time bound", `occurred == timestamp("2026-07-09T12:00:00Z")`, Input{Occurred: now}, true, ""},
		{"nil position reaches fence call", `geo.inFence("f")`, Input{}, false, "position"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := Compile(c.cel)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			got, err := p.Eval(c.in)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("want error containing %q, got (%v, %v)", c.wantErr, got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// The activation answers (nil, false) for any name that is not one of the six bindings, as a map
// does for an absent key.
func TestInputActivationUnknownName(t *testing.T) {
	a := inputActivation{in: &Input{}}
	if v, ok := a.ResolveName("nope"); ok || v != nil {
		t.Fatalf("unknown name resolved to (%v, %v)", v, ok)
	}
	for _, n := range []string{VarDevice, VarAnchors, VarOccurred, VarM, VarAttr, VarGeo} {
		if _, ok := a.ResolveName(n); !ok {
			t.Fatalf("%q must always resolve", n)
		}
	}
	if a.Parent() != nil {
		t.Fatal("input activation has no parent")
	}
}
