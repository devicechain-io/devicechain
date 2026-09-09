// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schemaplane

import "testing"

// Dir's duplicate-mount refusal is a backstop that cannot fire while every entry in
// `conventions` names a distinct mount — Classify keys on the exact filename, so two
// files in one directory can only collide on a mount if the TABLE lets them. This is
// where that premise is actually checked, so the refusal is not resting on a property
// nothing asserts.
func TestEachMountHasExactlyOneFilenameConvention(t *testing.T) {
	if len(conventions) == 0 {
		t.Fatal("the convention table is empty, so nothing can be classified at all")
	}
	seenMount := map[string]string{}
	seenFile := map[string]bool{}
	for _, c := range conventions {
		if prev, dup := seenMount[c.Mount]; dup {
			t.Errorf("%s and %s both map to mount %s; Dir would then have to concatenate two "+
				"files for one endpoint, which is the fold this package exists to prevent",
				prev, c.File, c.Mount)
		}
		seenMount[c.Mount] = c.File
		if seenFile[c.File] {
			t.Errorf("%s appears twice in the convention table; the first match wins and the "+
				"second is dead", c.File)
		}
		seenFile[c.File] = true
		if c.Plane != PlaneTenant && c.Plane != PlaneIdentity {
			t.Errorf("%s carries plane %q, which is not one this package defines", c.File, c.Plane)
		}
	}
}

// Every refused extension has to be something Ext is not, or the refusal would
// reject the only artifact shape this package accepts.
func TestNoRefusedExtensionIsTheAcceptedOne(t *testing.T) {
	if len(refusedExts) == 0 {
		t.Fatal("nothing is refused, so a .gql schema would be skipped in silence again")
	}
	for _, e := range refusedExts {
		if e == Ext {
			t.Fatalf("%q is both the accepted extension and a refused one", e)
		}
	}
}
