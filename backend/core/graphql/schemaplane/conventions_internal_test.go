// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schemaplane

import (
	"strings"
	"testing"
)

// Dir's duplicate-mount refusal has two ways to fire, and this pins the one that is
// a property of the TABLE. Classify keys on the normalized filename, so two files in
// one directory collide on a mount either because they are one convention under two
// spellings — schema.gql beside schema.graphql, which the scanner now reaches and
// TestDirRefusesTwoSpellingsOfOneMount covers — or because the table itself maps two
// names to one mount. That second premise is checked here, so the refusal is not
// resting on a property nothing asserts.
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

// Every alternate extension has to be something Ext is not, or Lint would refuse
// the only spelling this repository is allowed to use.
func TestNoAlternateExtensionIsTheCanonicalOne(t *testing.T) {
	if len(altExts) == 0 {
		t.Fatal("no alternate extension is known, so Lint refuses nothing and a .gql schema " +
			"would be skipped by the header gate in silence again")
	}
	for _, e := range altExts {
		if e == Ext {
			t.Fatalf("%q is both the canonical extension and an alternate one", e)
		}
	}
}

// 🔴 THE COUNTERWEIGHT TO NORMALIZATION. Reading through the extension is only safe
// while it changes nothing else: every convention, under every spelling a release
// might have used, must land on exactly the mount and plane the canonical name lands
// on. This is the arm that catches a normalization that quietly moves an endpoint —
// serving an identity-token surface on the tenant plane, or the reverse.
func TestNormalizingAnExtensionDoesNotMoveAMount(t *testing.T) {
	for _, c := range conventions {
		canonical, err := Classify("backend/services/x/graphql/" + c.File)
		if err != nil {
			t.Fatalf("%s does not classify at all: %v", c.File, err)
		}
		if canonical.Mount != c.Mount || canonical.Plane != c.Plane {
			t.Fatalf("%s classifies to %s/%s, want %s/%s",
				c.File, canonical.Mount, canonical.Plane, c.Mount, c.Plane)
		}
		stem := strings.TrimSuffix(c.File, Ext)
		for _, alt := range altExts {
			path := "backend/services/x/graphql/" + stem + alt
			got, aerr := Classify(path)
			if aerr != nil {
				t.Errorf("%s: %v", path, aerr)
				continue
			}
			if got.Mount != canonical.Mount || got.Plane != canonical.Plane {
				t.Errorf("%s classifies to %s/%s, but %s classifies to %s/%s; normalizing the "+
					"extension must not move the endpoint or the principal that reaches it",
					path, got.Mount, got.Plane, c.File, canonical.Mount, canonical.Plane)
			}
			if got.Path != path {
				t.Errorf("%s: Path is %q; the file has to be reported as it was found, because "+
					"that is the path a caller opens", path, got.Path)
			}
		}
	}
}

// A file under an unrelated extension stays a file this package ignores. Normalizing
// every extension would turn resolvers.go into a schema named resolvers.graphql, and
// the error it then raised would be about the wrong thing.
func TestCanonicalNameLeavesANonSchemaFileAlone(t *testing.T) {
	for _, name := range []string{"resolvers.go", "README.md", "schema.json", "schema"} {
		if got, shaped := canonicalName(name); shaped {
			t.Errorf("%s was treated as a schema artifact and renamed to %s", name, got)
		}
	}
}
