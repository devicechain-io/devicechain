// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	graphql "github.com/graph-gophers/graphql-go"
)

// The fixture has no subscription root, like 11 of the 14 real schemas.
const fixtureSDL = `
schema { query: Query mutation: Mutation }
type Query { things(criteria: ThingCriteria!): [Thing!]! thingsByToken(tokens: [String!]!): [Thing!]! }
type Mutation { cancelThing(token: String!): Thing! createThing(request: ThingInput!): Thing! }
type Thing { token: String! name: String }
input ThingCriteria { pageNumber: Int! pageSize: Int! }
input ThingInput { token: String! name: String }
`

var fixtureAllow = []AllowedSkip{
	{"testdata/fixture.md", "sdl", "type Thing"},
	{"testdata/fixture.md", "signature", "cancelThing(token"},
}

func fixtureSchemas(t *testing.T) []Named {
	t.Helper()
	s, err := graphql.ParseSchema(fixtureSDL, nil, graphql.UseFieldResolvers())
	if err != nil {
		t.Fatal(err)
	}
	return []Named{{Name: "things", Area: "things", Mount: "/graphql", Schema: s}}
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

func failuresByHeading(t *testing.T, data []byte, r Report) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, f := range r.Failures {
		if f.Block.Line == 0 || f.Block.File == "" {
			t.Errorf("failure carries no file:line: %+v", f)
		}
		h := headingBefore(data, f.Block.Line)
		if _, dup := got[h]; dup {
			t.Errorf("two failures under heading %q", h)
		}
		got[h] = f.Detail
	}
	return got
}

func assertFires(t *testing.T, got, want map[string]string) {
	t.Helper()
	for heading, frag := range want {
		d, ok := got[heading]
		if !ok {
			t.Errorf("case %q did not fail", heading)
		} else if !strings.Contains(d, frag) {
			t.Errorf("case %q failed with %q, want it to mention %q", heading, d, frag)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d failures, want %d: %v", len(got), len(want), got)
	}
}

// Each invalid case must fire, and the valid ones and the allowlisted skips must not.
func TestFixtureEachDefectFires(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "fixture.md"))
	if err != nil {
		t.Fatal(err)
	}
	r := Check(ExtractBlocks("testdata/fixture.md", data), nil, fixtureSchemas(t), fixtureAllow)
	assertFires(t, failuresByHeading(t, data, r), map[string]string{
		"unknown field":               "Cannot query field",
		"missing variable decl":       "NoUndefinedVariables",
		"wrong argument type":         "ValuesOfCorrectType",
		"unknown area":                "names no served schema",
		"subscription without a root": "declares a subscription root",
		"capitalised keyword":         "allowlist",
		"misspelt keyword":            "allowlist",
		"bare selection":              "allowlist",
		"description lead":            "not recognizable",
		"empty block":                 "empty block",
		"sdl then operation":          "Cannot query field",
	})
	// "valid" and "valid with attribute-style info string" are attributed to the schema.
	if len(r.Matches) != 2 {
		t.Errorf("matched %d blocks, want 2: %+v", len(r.Matches), r.Matches)
	}
	for _, m := range r.Matches {
		if len(m.Schemas) != 1 || m.Schemas[0] != "things" {
			t.Errorf("attribution = %v, want [things]", m.Schemas)
		}
	}
	reasons := map[string]int{}
	for _, s := range r.Skips {
		reasons[s.Reason]++
	}
	if reasons["sdl"] != 2 || reasons["signature"] != 1 || reasons["selection"] != 3 {
		t.Errorf("skips = %v", reasons)
	}
}

// An allowlist entry that nothing matches is a failure, so the list cannot go stale.
func TestStaleAllowlistEntryFails(t *testing.T) {
	r := Check(nil, nil, fixtureSchemas(t), []AllowedSkip{{"nowhere.md", "sdl", "type"}})
	if len(r.Failures) != 1 || !strings.Contains(r.Failures[0].Detail, "stale allowlist") {
		t.Fatalf("failures = %+v", r.Failures)
	}
}

// Where a schema does declare a subscription root, the subscription is validated for real.
func TestSubscriptionValidatedWhereARootExists(t *testing.T) {
	sdl := strings.Replace(fixtureSDL, "schema { query: Query mutation: Mutation }",
		"schema { query: Query mutation: Mutation subscription: Subscription }", 1) +
		`type Subscription { stream(id: String!): Thing! }`
	s, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers())
	if err != nil {
		t.Fatal(err)
	}
	schemas := append(fixtureSchemas(t), Named{Name: "streams", Schema: s})
	good := Block{File: "a.md", Line: 1, Text: `subscription { stream(id: "x") { token } }`}
	bad := Block{File: "a.md", Line: 5, Text: `subscription { stream(bogus: "x") { token } }`}
	r := Check([]Block{good, bad}, nil, schemas, nil)
	if len(r.Matches) != 1 || r.Matches[0].Schemas[0] != "streams" || len(r.Failures) != 1 || r.Failures[0].Block.Line != 5 {
		t.Fatalf("report = %+v", r)
	}
}

// When nothing validates, every tied schema is named, not one arbitrary "closest".
func TestClosestListsEveryTiedSchema(t *testing.T) {
	a := fixtureSchemas(t)[0]
	b := a
	b.Name = "other"
	r := Check([]Block{{File: "a.md", Line: 1, Text: `query { zzz }`}}, nil, []Named{a, b}, nil)
	if len(r.Failures) != 1 || !strings.Contains(r.Failures[0].Detail, "things, other") {
		t.Fatalf("failures = %+v", r.Failures)
	}
}

func TestCurlPayloadsFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "curl.md"))
	if err != nil {
		t.Fatal(err)
	}
	ps := ExtractPayloads("testdata/curl.md", data)
	if len(ps) != 6 {
		t.Fatalf("extracted %d payloads, want 6 (the non-graphql -d is ignored)", len(ps))
	}
	r := Check(nil, ps, fixtureSchemas(t), nil)
	if r.Payloads != 6 {
		t.Errorf("payloads checked = %d", r.Payloads)
	}
	assertFires(t, failuresByHeading(t, data, r), map[string]string{
		"bad variable field":               "bogus",
		"wrong variable type":              "ThingInput",
		"undeclared variable":              "NoUndefinedVariables",
		"unknown endpoint":                 "addresses no served schema",
		"unknown field in a literal query": "Cannot query field",
	})
	if len(r.Matches) != 1 {
		t.Errorf("matches = %+v, want only the valid payload", r.Matches)
	}
}

// The tilde fence, an indented fence, CRLF endings and attribute-style info strings are all read.
func TestExtractFenceForms(t *testing.T) {
	src := "intro\r\n1. step\r\n\r\n   ```graphql\r\n   query { a }\r\n   ```\r\n\r\n~~~gql\r\n{ b }\r\n~~~\r\n```json\r\n{}\r\n```\r\n```graphql{1,3}\r\n{ c }\r\n```\r\n```graphql title=\"x\"\r\n{ d }\r\n```\r\n"
	b := ExtractBlocks("x.md", []byte(src))
	if len(b) != 4 || b[0].Line != 4 || b[1].Line != 8 || b[2].Line != 14 || b[3].Line != 17 {
		t.Fatalf("blocks = %+v", b)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cliTree(t *testing.T, en, es, zh string) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "backend", "services", "things", "graphql", "schema.graphql"), fixtureSDL)
	writeFile(t, filepath.Join(root, "docs", "docs", "a.md"), en)
	writeFile(t, filepath.Join(root, "docs", "i18n", "es", "docusaurus-plugin-content-docs", "current", "a.md"), es)
	writeFile(t, filepath.Join(root, "docs", "i18n", "zh-CN", "docusaurus-plugin-content-docs", "current", "a.md"), zh)
	return root
}

// The CLI's exit status is the CI signal, so it is tested directly.
func TestRunExitStatus(t *testing.T) {
	good := "```graphql\nquery { thingsByToken(tokens: [\"a\"]) { token } }\n```\n"
	bad := "```graphql\nquery { thingsByToken(tokens: [\"a\"]) { nope } }\n```\n"
	cases := []struct {
		name       string
		en, es, zh string
		want       int
		outHas     string
	}{
		{"clean", good, good, good, 0, "ok docs/docs/a.md:1 -> things"},
		{"a bad example", bad, good, good, 1, "FAIL docs/docs/a.md:1"},
		{"a bad translation", good, bad, good, 1, "FAIL docs/i18n/es/"},
		{"a locale lost an example", good, good, "no graphql here\n", 1, "per-locale"},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		got := run(cliTree(t, c.en, c.es, c.zh), nil, &out, &errb)
		if got != c.want || !strings.Contains(out.String(), c.outHas) {
			t.Errorf("%s: exit %d (want %d), out %q, err %q", c.name, got, c.want, out.String(), errb.String())
		}
	}
	var out, errb bytes.Buffer
	if got := run(t.TempDir(), nil, &out, &errb); got != 2 {
		t.Errorf("no services tree: exit %d, want 2", got)
	}
}

// The real tree: every example in the published docs validates against the served schemas.
func TestPublishedDocsExamplesValidate(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	schemas, err := LoadSchemas(filepath.Join(root, "backend", "services"))
	if err != nil {
		t.Fatal(err)
	}
	blocks, payloads, err := Walk(root, filepath.Join("docs", "docs"), filepath.Join("docs", "i18n"))
	if err != nil {
		t.Fatal(err)
	}
	// Every locale carries what English does, and the locales are the ones we ship.
	locales, err := Locales(root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(locales, ",") != "es,zh-CN" {
		t.Fatalf("locales = %v, want es and zh-CN", locales)
	}
	counts, err := Parity(locales, blocks, payloads)
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 3 || counts["es"] != counts["en"] || counts["zh-CN"] != counts["en"] {
		t.Fatalf("per-locale counts = %v, want en, es and zh-CN equal", counts)
	}
	// Floors: a walk that found little would validate little.
	if counts["en"][0] < 20 || counts["en"][1] < 7 {
		t.Fatalf("English has only %v [blocks, payloads]", counts["en"])
	}
	r := Check(blocks, payloads, schemas, allowedSkips)
	for _, f := range r.Failures {
		t.Errorf("%s:%d: %s", f.Block.File, f.Block.Line, f.Detail)
	}
	// The skipped set is exactly the allowlist, one skip per entry.
	if len(r.Skips) != len(allowedSkips) || len(allowedSkips) != 6 {
		t.Errorf("skipped %d blocks, allowlist has %d, want 6 each", len(r.Skips), len(allowedSkips))
	}
	for _, s := range r.Skips {
		t.Logf("skipped (%s): %s:%d", s.Reason, s.Block.File, s.Block.Line)
	}
	t.Logf("%d graphql blocks validated, %d curl payloads, %d failed, %d skipped; per locale %v",
		r.Checked, r.Payloads, len(r.Failures), len(r.Skips), counts)
}
