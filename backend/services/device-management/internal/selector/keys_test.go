// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package selector

import (
	"reflect"
	"testing"
)

// Keys returns the distinct facet keys a selector references, in first-appearance
// order, across every lowerable leaf shape (equality, presence, numeric compare) and
// through &&/||/!. It is the reverse-index input for ADR-062, so it must be exact.
func TestSelectorKeys(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"single equality", `attr["climate"] == "arid"`, []string{"climate"}},
		{"conjunction distinct", `attr["climate"] == "arid" && attr["region"] == "west"`, []string{"climate", "region"}},
		{"presence + numeric", `"beta" in attr && attr["level"] > 5`, []string{"beta", "level"}},
		{"disjunction + not", `attr["a"] == "x" || !(attr["b"] == "y")`, []string{"a", "b"}},
		{"deduped, first-appearance order", `attr["k"] == "x" && attr["k"] != "y"`, []string{"k"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sel, err := Compile(c.src, "device", 0)
			if err != nil {
				t.Fatalf("compile %q: %v", c.src, err)
			}
			got := sel.Keys()
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Keys() = %v, want %v", got, c.want)
			}
		})
	}
}
