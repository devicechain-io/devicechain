// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sdldesc

import (
	"path/filepath"
	"strings"
	"testing"
)

// THE GATE. Every served schema, against its allowlist.
//
// RUN WITH -count=1 AFTER TOUCHING A SCHEMA: the schemas live outside this module and
// Go's test cache does not track them.
func TestServedSchemasAreDescribed(t *testing.T) {
	served, err := LoadServed(filepath.Join("..", "..", "..", "services"))
	if err != nil {
		t.Fatal(err)
	}
	// Floor, by NAME: a discovery that silently lost a plane would check less and pass.
	names := map[string]bool{}
	elems := 0
	for _, s := range served {
		names[s.Name] = true
		elems += len(s.Elements)
	}
	for _, want := range []string{"device-management", "user-management", "user-management-admin",
		"user-management-settings", "ai-inference-admin"} {
		if !names[want] {
			t.Errorf("served schema %q not found; discovery is reading less than the tree serves", want)
		}
	}
	if elems < 1000 {
		t.Errorf("only %d elements across %d schemas; the enumeration is reading less than the tree", elems, len(served))
	}

	allow, err := ReadAllowlists("undescribed")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range Check(served, allow) {
		t.Error(f)
	}
}

const fixture = `
schema { query: Query }

"""Marks a field experimental."""
directive @beta(
  """Why."""
  reason: String
) on FIELD_DEFINITION

type Query {
  """Find things."""
  things(
    """Filter by name."""
    name: String
    limit: Int
  ): [Thing!]!
  # a maintainer comment is NOT a description
  commented: Int
}

"""A thing the fixture knows about."""
type Thing {
  """Unique token for the thing."""
  token: String!
  undescribed: Int
}

"""Input for a thing."""
input ThingInput {
  """The name to give it."""
  name: String
  bare: Int
}

"""How bright."""
enum Level {
  """Fully lit."""
  HIGH
  LOW
}

"""One of several things."""
union Either = Thing

"""When."""
scalar Time
`

func fixtureServed(t *testing.T, sdl string) []Served {
	t.Helper()
	els, err := Parse(sdl)
	if err != nil {
		t.Fatal(err)
	}
	return []Served{{Name: "fx", Path: "fx.graphql", Elements: els}}
}

// The enumeration covers every kind, leaves the builtins and the root type itself out,
// and reads only string descriptions.
func TestUndescribedEnumeration(t *testing.T) {
	got := strings.Join(Undescribed(fixtureServed(t, fixture)[0]), " ")
	want := "Level.LOW Query.commented Query.things(limit:) Thing.undescribed ThingInput.bare"
	if got != want {
		t.Errorf("undescribed = %q\nwant          %q", got, want)
	}
	all := map[string]bool{}
	for _, e := range fixtureServed(t, fixture)[0].Elements {
		all[e.Coord] = true
	}
	for _, c := range []string{"@beta", "@beta(reason:)", "Either", "Time", "Level.HIGH", "ThingInput.name",
		"Query.things(name:)", "Thing.token"} {
		if !all[c] {
			t.Errorf("element %s not enumerated", c)
		}
	}
	for _, c := range []string{"Query", "String", "Int", "__Schema", "@deprecated", "@include"} {
		if all[c] {
			t.Errorf("element %s should not be enumerated", c)
		}
	}
}

func findings(t *testing.T, sdl string, allow map[string][]string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range Check(fixtureServed(t, sdl), allow) {
		out[f.Schema+" "+f.Coord] = f.Detail
	}
	return out
}

func TestCheckRules(t *testing.T) {
	full := []string{"Level.LOW", "Query.commented", "Query.things(limit:)", "Thing.undescribed", "ThingInput.bare"}

	t.Run("complete allowlist passes", func(t *testing.T) {
		if f := findings(t, fixture, map[string][]string{"fx": full}); len(f) != 0 {
			t.Errorf("unexpected findings: %v", f)
		}
	})

	t.Run("undescribed and unlisted fails, comment does not count", func(t *testing.T) {
		f := findings(t, fixture, map[string][]string{"fx": full[:1]})
		for _, c := range full[1:] {
			if !strings.Contains(f["fx "+c], "no description") {
				t.Errorf("%s: want a no-description finding, got %q", c, f["fx "+c])
			}
		}
	})

	t.Run("described but still listed is stale", func(t *testing.T) {
		f := findings(t, fixture, map[string][]string{"fx": append(append([]string{}, full...), "Thing.token")})
		if !strings.Contains(f["fx Thing.token"], "described now") {
			t.Errorf("got %v", f)
		}
	})

	t.Run("listed but absent is stale", func(t *testing.T) {
		f := findings(t, fixture, map[string][]string{"fx": append(append([]string{}, full...), "Thing.gone")})
		if !strings.Contains(f["fx Thing.gone"], "not in the schema") {
			t.Errorf("got %v", f)
		}
	})

	t.Run("listed twice", func(t *testing.T) {
		f := findings(t, fixture, map[string][]string{"fx": append(append([]string{}, full...), "Level.LOW")})
		if !strings.Contains(f["fx Level.LOW"], "twice") {
			t.Errorf("got %v", f)
		}
	})

	t.Run("allowlist for an unserved schema", func(t *testing.T) {
		f := findings(t, fixture, map[string][]string{"fx": full, "renamed": nil})
		if !strings.Contains(f["renamed "], "not served") {
			t.Errorf("got %v", f)
		}
	})

	t.Run("quality", func(t *testing.T) {
		for _, tc := range []struct{ desc, want string }{
			{"See ADR-042 for why.", "decision record"},
			{"adr 7 says so", "decision record"},
			{"TODO describe", "placeholder"},
			{"Token.", "restates the name"},
			{"token", "restates the name"},
			{"The token that identifies the thing.", ""},
		} {
			sdl := strings.Replace(fixture, `"""Unique token for the thing."""`, `"""`+tc.desc+`"""`, 1)
			got := findings(t, sdl, map[string][]string{"fx": full})["fx Thing.token"]
			if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
				t.Errorf("%q: got %q, want %q", tc.desc, got, tc.want)
			}
		}
	})
}

// The leaf of each coordinate shape, which the restates-the-name rule compares against.
func TestLeafName(t *testing.T) {
	for in, want := range map[string]string{
		"Thing": "Thing", "Thing.token": "token", "Query.things(limit:)": "limit",
		"Level.LOW": "LOW", "@beta": "beta", "@beta(reason:)": "reason",
	} {
		if got := leafName(in); got != want {
			t.Errorf("leafName(%q) = %q, want %q", in, got, want)
		}
	}
}
