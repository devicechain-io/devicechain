// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"strings"
	"testing"

	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// TestFunctionCostDocsMatchTheCharges pins the cost words the generated function reference
// prints to what the library really charges, so the page cannot describe a charge the code
// does not make. A function whose words change with its charge fails here first.
func TestFunctionCostDocsMatchTheCharges(t *testing.T) {
	frame := types.Bytes(make([]byte, 16))
	// What each distinct cost phrase promises, as the real charge for one concrete call.
	perPhrase := map[string]func(f *fnSpec) uint64{
		costOne: func(f *fnSpec) uint64 {
			return f.actual([]ref.Val{frame, types.Int(0), types.Int(0), types.Int(8)}, types.Int(0))
		},
		"1 per byte read (the len argument)": func(f *fnSpec) uint64 {
			return f.actual([]ref.Val{frame, types.Int(0), types.Int(3)}, types.Int(0)) - 3 + 1
		},
		"1 per byte copied, at least 1": func(f *fnSpec) uint64 {
			return f.actual(nil, types.Bytes(make([]byte, 10))) - 10 + 1
		},
		costPerByte: func(f *fnSpec) uint64 {
			return f.actual([]ref.Val{types.String("abcd")}, types.Bytes(nil)) - 4 + 1
		},
		"1 per byte of the frame, at least 1": func(f *fnSpec) uint64 {
			return f.actual([]ref.Val{frame}, nil) - 16 + 1
		},
	}
	seen := map[string]bool{}
	for i := range ownedFuncs {
		f := &ownedFuncs[i]
		if strings.HasPrefix(f.name, "test.") {
			continue // a native a test registers, not part of the library
		}
		if f.costDoc == "" {
			t.Errorf("%s has no cost words", f.name)
			continue
		}
		check, ok := perPhrase[f.costDoc]
		if !ok {
			t.Errorf("%s: cost words %q have no check", f.name, f.costDoc)
			continue
		}
		seen[f.costDoc] = true
		// check returns 1 when the real charge is exactly what the words promise.
		if got := check(f); got != 1 {
			t.Errorf("%s: the charge does not match %q (deviation %d)", f.name, f.costDoc, int64(got)-1)
		}
		if f.costDoc == costOne && f.static == nil {
			t.Errorf("%s has no static cost", f.name)
		}
	}
	for phrase := range perPhrase {
		if !seen[phrase] {
			t.Errorf("no function uses the cost words %q: the check is dead", phrase)
		}
	}
	// A math function has no estimator of its own, so the standard charge is all it pays.
	for _, d := range mathDocs {
		if _, own := byName[d.Name]; own {
			t.Errorf("%s now has its own charge, but its cost words say %q", d.Name, d.Cost)
		}
		if d.Cost != mathCost {
			t.Errorf("%s: cost words %q, want %q", d.Name, d.Cost, mathCost)
		}
	}
	for _, d := range Functions() {
		if strings.HasPrefix(d.Name, "test.") {
			continue
		}
		if d.Cost == "" {
			t.Errorf("%s: Functions() carries no cost", d.Name)
		}
	}
}
