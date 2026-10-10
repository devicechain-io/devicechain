// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import "testing"

// A served schema describes itself with spec string descriptions only. A `#` comment is
// a maintainer note and must never reach introspection: under graphql-go's default
// (legacy) reading it would, and every decision-record citation and piece of internal
// rationale in a schema file would be served as the public description of the element
// below it.
func TestServedDescriptionsAreStringsNotComments(t *testing.T) {
	const sdl = `
schema { query: Query }

# maintainer note on the root type
type Query {
  """The public description of thing."""
  thing: Thing
  # maintainer note only, see ADR-000
  other: Int
}

"""A thing."""
type Thing { id: ID }
`
	s := MustParseSchema(sdl, nil)
	var thing, other string
	var gotOther bool
	for _, typ := range s.Inspect().Types() {
		if typ.Name() == nil {
			continue
		}
		switch *typ.Name() {
		case "Query":
			for _, f := range *typ.Fields(nil) {
				switch f.Name() {
				case "thing":
					if d := f.Description(); d != nil {
						thing = *d
					}
				case "other":
					gotOther = true
					if d := f.Description(); d != nil {
						other = *d
					}
				}
			}
		case "Thing":
			if d := typ.Description(); d == nil || *d != "A thing." {
				t.Errorf("type description = %v, want %q", d, "A thing.")
			}
		}
	}
	if thing != "The public description of thing." {
		t.Errorf("string description not served: got %q", thing)
	}
	if !gotOther {
		t.Fatal("field other not found; the walk above is not reading the schema")
	}
	if other != "" {
		t.Errorf("a # comment was served as a description: %q", other)
	}
}
