// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	graphql "github.com/graph-gophers/graphql-go"
)

const fixtureSDL = `
schema { query: Query mutation: Mutation }
type Query { things(criteria: ThingCriteria!): [Thing!]! thingsByToken(tokens: [String!]!): [Thing!]! }
type Mutation { cancelThing(token: String!): Thing! }
type Thing { token: String! name: String }
input ThingCriteria { pageNumber: Int! pageSize: Int! }
`

func fixtureSchemas(t *testing.T) []Named {
	t.Helper()
	s, err := graphql.ParseSchema(fixtureSDL, nil, graphql.UseFieldResolvers())
	if err != nil {
		t.Fatal(err)
	}
	return []Named{{Name: "things", Area: "things", Mount: "/graphql", Schema: s}}
}

// Each invalid case must fire, and the valid one and the skips must not.
func TestFixtureEachDefectFires(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "fixture.md"))
	if err != nil {
		t.Fatal(err)
	}
	blocks := ExtractBlocks("testdata/fixture.md", data)
	r := Check(blocks, fixtureSchemas(t))

	want := map[string]string{
		"unknown field":         "Cannot query field",
		"missing variable decl": "NoUndefinedVariables",
		"wrong argument type":   "ValuesOfCorrectType",
		"unknown area":          "names no served schema",
	}
	got := map[string]string{}
	for _, f := range r.Failures {
		got[headingBefore(data, f.Block.Line)] = f.Detail
		if f.Block.File != "testdata/fixture.md" || f.Block.Line == 0 {
			t.Errorf("failure carries no file:line: %+v", f.Block)
		}
	}
	for heading, frag := range want {
		d, ok := got[heading]
		if !ok {
			t.Errorf("case %q did not fail", heading)
		} else if !strings.Contains(d, frag) {
			t.Errorf("case %q failed with %q, want it to mention %q", heading, d, frag)
		}
	}
	if len(r.Failures) != len(want) {
		t.Errorf("got %d failures, want %d: %+v", len(r.Failures), len(want), r.Failures)
	}
	// valid + 4 invalid are validated; the sdl and the signature are skipped, and reported.
	if r.Checked != 5 {
		t.Errorf("validated %d blocks, want 5", r.Checked)
	}
	reasons := map[string]int{}
	for _, s := range r.Skips {
		reasons[s.Reason]++
	}
	if reasons["sdl"] != 1 || reasons["fragment"] != 1 || len(r.Skips) != 2 {
		t.Errorf("skips = %v, want one sdl and one fragment", reasons)
	}
}

// headingBefore returns the last markdown heading above line, which names the case.
func headingBefore(data []byte, line int) string {
	lines := strings.Split(string(data), "\n")
	for i := line - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "## ") {
			return strings.TrimPrefix(lines[i], "## ")
		}
	}
	return ""
}

// The tilde fence, an indented fence and CRLF endings are all read.
func TestExtractFenceForms(t *testing.T) {
	src := "intro\r\n1. step\r\n\r\n   ```graphql\r\n   query { a }\r\n   ```\r\n\r\n~~~gql\r\n{ b }\r\n~~~\r\n```json\r\n{}\r\n```\r\n"
	b := ExtractBlocks("x.md", []byte(src))
	if len(b) != 2 || b[0].Line != 4 || b[1].Line != 8 {
		t.Fatalf("blocks = %+v", b)
	}
}

// The real tree: every example in the published docs validates against the served schemas.
func TestPublishedDocsExamplesValidate(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	schemas, err := LoadSchemas(filepath.Join(root, "backend", "services"))
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := Walk(filepath.Join(root, "docs", "docs"), filepath.Join(root, "docs", "i18n"))
	if err != nil {
		t.Fatal(err)
	}
	// Floor: a walk that found nothing would validate everything.
	if len(blocks) < 20 {
		t.Fatalf("found only %d graphql blocks in the docs", len(blocks))
	}
	r := Check(blocks, schemas)
	for _, s := range r.Skips {
		t.Logf("skipped (%s): %s:%d", s.Reason, s.Block.File, s.Block.Line)
	}
	for _, f := range r.Failures {
		t.Errorf("%s:%d: %s", f.Block.File, f.Block.Line, f.Detail)
	}
	t.Logf("%d validated, %d failed, %d skipped", r.Checked, len(r.Failures), len(r.Skips))
}
