// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package predicate

import (
	"reflect"
	"testing"
)

// TestNegatedAttributeGuards pins which leaves are reported as carrying an absence test for a
// device attribute joined by && at the top level, and which are not. The positive form and every
// unrelated negation must stay silent: the warning is only worth reading while it is rare.
func TestNegatedAttributeGuards(t *testing.T) {
	cases := []struct {
		src  string
		want []string
		why  string
	}{
		{`!("lim" in attr) && "t" in m && m["t"] > 80.0`, []string{"lim"}, "the fallback idiom"},
		{`"t" in m && m["t"] > 80.0 && !("lim" in attr)`, []string{"lim"}, "negation last"},
		{`"t" in m && (m["t"] > 80.0 && !("lim" in attr))`, []string{"lim"}, "nested conjunction"},
		{`!has(attr.lim) && m["t"] > 80.0`, []string{"lim"}, "has() macro"},
		{`("lim" in attr) == false && m["t"] > 80.0`, []string{"lim"}, "== false"},
		{`false == ("lim" in attr) && m["t"] > 80.0`, []string{"lim"}, "false == ..."},
		{`!("b" in attr) && !("a" in attr) && m["t"] > 1.0`, []string{"a", "b"}, "two attributes, sorted"},
		{`!("a" in attr) && !("a" in attr) && m["t"] > 1.0`, []string{"a"}, "deduplicated"},
		{`"a" in attr && !("b" in attr) && m["t"] > 1.0`, []string{"b"}, "guarded a, absent b"},

		{`("lim" in attr) && "t" in m && m["t"] > attr["lim"]`, nil, "the guarded positive form"},
		{`"t" in m && ("lim" in attr ? m["t"] > attr["lim"] : m["t"] > 80.0)`, nil, "the recommended fallback"},
		{`!("t" in m) && m["x"] > 1.0`, nil, "negated presence in m, not attr"},
		{`!(m["t"] > 1.0) && "lim" in attr`, nil, "unrelated negation"},
		{`!("lim" in attr) || m["t"] > 80.0`, nil, "disjunction is not a conjunct"},
		{`!(("lim" in attr) && m["t"] > 1.0)`, nil, "negated conjunction"},
		{`"t" in m && m["t"] > 80.0`, nil, "reads no attr"},
		{`true`, nil, "constant"},
	}
	for _, c := range cases {
		p, err := Compile(c.src)
		if err != nil {
			t.Fatalf("Compile(%s): %v", c.src, err)
		}
		if got := p.NegatedAttributeGuards(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("NegatedAttributeGuards(%s) = %v, want %v (%s)", c.src, got, c.want, c.why)
		}
	}
}
