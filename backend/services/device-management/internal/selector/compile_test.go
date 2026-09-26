// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package selector

import (
	"errors"
	"strings"
	"testing"
)

// A representative set of facet selectors clears the publish gate for their family.
func TestCompile_Accepts(t *testing.T) {
	ok := []string{
		`"climate" in attr`,
		`attr["climate"] == "arid"`,
		`attr["climate"] != "arid"`,
		`!(attr["climate"] == "arid")`,
		`attr["population"] > 1000`,
		`1000 < attr["population"]`, // commuted
		`attr["coastal"] == true`,
		`attr["climate"] == "arid" && attr["population"] >= 100000`,
		`attr["climate"] == "arid" || attr["climate"] == "humid"`,
	}
	for _, src := range ok {
		if _, err := Compile(src, "device"); err != nil {
			t.Errorf("Compile(%q) unexpected error: %v", src, err)
		}
	}
}

// Non-lowerable, non-boolean, and too-expensive selectors are rejected with typed errors.
func TestCompile_Rejects(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want any // a pointer to the expected error type
	}{
		{"non-boolean", `attr["climate"]`, &CompileError{}},
		{"parse error", `attr["climate" == `, &CompileError{}},
		{"bare attr boolean", `attr["on"] && attr["climate"] == "arid"`, &NotLowerableError{}},
		{"two indexes", `attr["a"] == attr["b"]`, &NotLowerableError{}},
		{"non-literal key", `attr[memberType] == "x"`, &NotLowerableError{}},
		{"numeric op on string", `attr["climate"] > "arid"`, &NotLowerableError{}},
		{"unknown ident", `foo["climate"] == "arid"`, &CompileError{}},
		{"function call", `size(attr) > 0`, &NotLowerableError{}},
		{"memberType leaf", `memberType == "device"`, &NotLowerableError{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Compile(c.src, "device")
			if err == nil {
				t.Fatalf("Compile(%q) = nil error, want %T", c.src, c.want)
			}
			switch c.want.(type) {
			case *CompileError:
				var ce *CompileError
				if !errors.As(err, &ce) {
					t.Fatalf("Compile(%q) = %v, want *CompileError", c.src, err)
				}
			case *NotLowerableError:
				var nl *NotLowerableError
				if !errors.As(err, &nl) {
					t.Fatalf("Compile(%q) = %v, want *NotLowerableError", c.src, err)
				}
				if nl.Source != c.src {
					t.Errorf("NotLowerableError.Source = %q, want %q", nl.Source, c.src)
				}
			}
		})
	}
}

// The leaf-count bound rejects a selector carrying more facet comparisons than allowed.
func TestCompile_LeafBound(t *testing.T) {
	// MaxSelectorLeaves+1 OR'd presence leaves.
	parts := make([]string, MaxSelectorLeaves+1)
	for i := range parts {
		parts[i] = `"k` + string(rune('a'+i%26)) + strings.Repeat("x", i) + `" in attr`
	}
	src := strings.Join(parts, " || ")
	_, err := Compile(src, "device")
	var nl *NotLowerableError
	if !errors.As(err, &nl) {
		t.Fatalf("Compile(oversized) = %v, want *NotLowerableError for leaf bound", err)
	}
	if !strings.Contains(nl.Reason, "exceeding the limit") {
		t.Errorf("reason = %q, want leaf-bound message", nl.Reason)
	}
}

// The cost gate compares the estimate against the ceiling it is given: a single presence
// leaf, cheap as it is, is refused at a ceiling of 1, with the ceiling it was held to.
func TestCompile_CostCeiling(t *testing.T) {
	_, err := compile(`"climate" in attr`, "device", 1)
	var ce *CostError
	if !errors.As(err, &ce) {
		t.Fatalf("compile with ceiling 1 = %v, want *CostError", err)
	}
	if ce.Ceiling != 1 {
		t.Fatalf("CostError.Ceiling = %d, want the 1 it was compiled at", ce.Ceiling)
	}
}

// TestASelectorIsGatedAtThePlatformCeiling pins the exported entry point to the platform
// value. The ceiling is a literal, not CostCeiling, so changing the platform value is a
// deliberate edit here too. The cost gate runs before the lowerability check, so this
// comprehension, which would not lower either, is refused on cost.
func TestASelectorIsGatedAtThePlatformCeiling(t *testing.T) {
	const src = `attr.all(k, attr[k] == "x")`
	_, err := Compile(src, "device")
	var ce *CostError
	if !errors.As(err, &ce) {
		t.Fatalf("Compile(%q) = %v, want a *CostError at the platform ceiling", src, err)
	}
	if ce.Ceiling != 100 {
		t.Fatalf("the selector was gated at a ceiling of %d, want the platform's 100", ce.Ceiling)
	}
	if ce.EstimatedMax <= 100 {
		t.Fatalf("the refusal reports an estimate of %d, which is within the ceiling it was refused at", ce.EstimatedMax)
	}
}
