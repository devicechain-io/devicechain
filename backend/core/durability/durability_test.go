// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package durability

import "testing"

func TestTheZeroValueIsNotAClass(t *testing.T) {
	var c Class
	if c.Valid() {
		t.Fatal("the zero Class must be invalid, or an unclassified declaration would pass")
	}
	if c != Unclassified {
		t.Fatalf("zero value is %v, want Unclassified", c)
	}
}

func TestNamesAreStable(t *testing.T) {
	// The documentation table is rendered from these strings.
	want := map[Class]string{MustBackUp: "must-back-up", Rebuildable: "rebuildable", Transient: "transient", Unclassified: "unclassified"}
	for c, s := range want {
		if c.String() != s {
			t.Errorf("%d renders %q, want %q", int(c), c.String(), s)
		}
	}
	for _, c := range []Class{MustBackUp, Rebuildable, Transient} {
		if !c.Valid() {
			t.Errorf("%v must be valid", c)
		}
	}
	if Class(99).Valid() {
		t.Error("an out-of-range class must be invalid")
	}
}
