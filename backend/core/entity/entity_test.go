// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package entity

import "testing"

func TestType_Valid(t *testing.T) {
	for _, typ := range All {
		if !typ.Valid() {
			t.Errorf("entity type %q in All is not Valid()", typ)
		}
	}
	for _, bad := range []Type{"", "widget", "Device", "devicegroup", "groups"} {
		if bad.Valid() {
			t.Errorf("unexpected type %q reported Valid()", bad)
		}
	}
}

func TestType_AllUnique(t *testing.T) {
	seen := make(map[Type]bool, len(All))
	for _, typ := range All {
		if seen[typ] {
			t.Errorf("duplicate entity type %q in All", typ)
		}
		seen[typ] = true
	}
	if len(All) != 5 {
		t.Fatalf("expected 5 entity types, got %d", len(All))
	}
}

func TestType_String(t *testing.T) {
	if TypeGroup.String() != "group" {
		t.Fatalf("unexpected string value %q", TypeGroup.String())
	}
}
