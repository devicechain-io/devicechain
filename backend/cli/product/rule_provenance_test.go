// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestProductApplyStampsRuleDoor(t *testing.T) {
	b := fixture(t)
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := b.Write(dir); err != nil {
		t.Fatal(err)
	}
	f := newFake(t)
	apply(t, f, dir, env(defaultEnv))
	for i := range b.Profiles {
		if len(b.Profiles[i].Rules) > 0 {
			b.Profiles[i].Rules[0].Name = "Revised"
		}
	}
	if err := b.Write(dir); err != nil {
		t.Fatal(err)
	}
	apply(t, f, dir, env(defaultEnv))
	seen := map[string]bool{}
	for _, call := range f.calls {
		for _, op := range []string{"ProductCreateRule", "ProductUpdateRule"} {
			if !strings.Contains(call.query, op+"(") {
				continue
			}
			seen[op] = true
			input := call.vars["request"].(map[string]any)
			if input["authoredVia"] != "PRODUCT_AS_CODE" {
				t.Errorf("%s authoredVia = %v", op, input["authoredVia"])
			}
		}
	}
	if len(seen) != 2 {
		t.Fatalf("did not exercise create and update: %v", seen)
	}
	// Authoring history is observational; it must not manufacture product drift.
	if p := plan(t, f, dir, env(defaultEnv)); !p.Empty() {
		t.Fatal("unchanged product has drift")
	}
}
