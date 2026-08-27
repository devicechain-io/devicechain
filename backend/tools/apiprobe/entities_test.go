// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// createMutation matches a create* field on a schema's Mutation type. The served
// schemas indent their fields, which is what keeps this from matching the input
// types and comments that also mention the word.
var createMutation = regexp.MustCompile(`(?m)^[\t ]+create[A-Z]\w*\s*\(`)

// servedSchemas returns every tenant-plane schema file. Admin schemas are
// excluded deliberately: they are a separate identity-token surface with their
// own principal, and folding them into one number would make the coverage claim
// mean two different things at once.
func servedSchemas(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..", "services")
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.Contains(path, string(os.PathSeparator)+"graphql"+string(os.PathSeparator)) {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".graphql" && ext != ".gql" {
			return nil
		}
		if strings.Contains(filepath.Base(path), "admin") {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// schemaFor returns the served schema text of one functional area.
func schemaFor(t *testing.T, area string) string {
	t.Helper()
	var body []byte
	for _, s := range servedSchemas(t) {
		if !strings.Contains(s, string(os.PathSeparator)+area+string(os.PathSeparator)) {
			continue
		}
		part, err := os.ReadFile(s)
		if err != nil {
			t.Fatalf("read %s: %v", s, err)
		}
		body = append(body, part...)
	}
	if len(body) == 0 {
		t.Fatalf("no served schema found for area %q", area)
	}
	return string(body)
}

// The denominator in the coverage message is the difference between "5 entities
// pass" and "5 of 26 pass", and only the second tells a reader what a green run
// does not cover. A number typed into a constant drifts the moment somebody adds
// a create mutation, and nothing would fail when it did — so it is measured.
//
// 🔴 RUN THIS WITH -count=1 AFTER TOUCHING A SCHEMA. The files it reads live
// OUTSIDE this module, and Go's test cache does not track them: edit a schema,
// re-run, and a stale PASS is served from cache. That is a general trap in this
// repo, not a quirk of this test.
func TestCoverageDenominatorMatchesTheSchemas(t *testing.T) {
	schemas := servedSchemas(t)
	// A vacuous pass here would look exactly like a thorough one: if the walk
	// found nothing, the count is 0, and 0 != 26 would fail for the wrong
	// reason — or worse, a future refactor could make both sides 0 and agree.
	if len(schemas) < 5 {
		t.Fatalf("found only %d served schema files; the probe is broken, not the constant", len(schemas))
	}

	counted := 0
	perFile := map[string]int{}
	for _, s := range schemas {
		body, err := os.ReadFile(s)
		if err != nil {
			t.Fatalf("read %s: %v", s, err)
		}
		n := len(createMutation.FindAll(body, -1))
		if n > 0 {
			perFile[s] = n
			counted += n
		}
	}

	if counted != tenantCreateMutations {
		t.Errorf("tenantCreateMutations = %d, schemas actually declare %d", tenantCreateMutations, counted)
		for f, n := range perFile {
			t.Logf("  %2d  %s", n, f)
		}
	}
}

// The table claims to cover every create mutation, and that claim is what
// `apiprobe coverage` prints. A row short of the denominator is a real gap and
// says so; a row PAST it means the table counts something twice.
func TestTheTableCoversEveryCreateMutation(t *testing.T) {
	if len(entities) != tenantCreateMutations {
		t.Errorf("the table has %d entries for %d create mutations", len(entities), tenantCreateMutations)
	}
	seen := map[string]bool{}
	for _, e := range entities {
		if seen[e.Mutation] {
			// Two rows on one mutation would inflate the count while leaving
			// something else uncovered — the arithmetic still reaching 26.
			t.Errorf("two entities create via %q", e.Mutation)
		}
		seen[e.Mutation] = true
	}
}

// Every entry has to be usable: a mutation, an input type, a read query, a
// selection and a variable builder. A half-filled row would seed nothing and
// verify nothing while still counting toward the coverage number printed to an
// operator.
func TestEveryEntityIsComplete(t *testing.T) {
	if len(entities) == 0 {
		t.Fatal("the coverage table is empty")
	}
	seen := map[string]bool{}
	for _, e := range entities {
		if seen[e.Name] {
			// Names are receipt keys. A duplicate would make verify check one of
			// them twice and the other never, silently.
			t.Errorf("duplicate entity name %q", e.Name)
		}
		seen[e.Name] = true

		if e.Name == "" || e.Area == "" || e.Mutation == "" || e.Input == "" ||
			e.Read == "" || e.Fields == "" || e.Vars == nil {
			t.Errorf("entity %q is incomplete", e.Name)
			continue
		}
		// seed reads the token off the created object to address the read-back.
		// An entity that never selects one would fail on a cluster, at the end
		// of a drill, for a reason visible here in milliseconds.
		if !strings.Contains(e.Fields, "token") {
			t.Errorf("entity %q does not select a token; verify would have nothing to look it up by", e.Name)
		}
		// A result envelope is only unwrappable if the refusal beside it is
		// asked for too — otherwise a declined create reports as an absent
		// object with no code and no reason.
		if e.Wrap != "" && e.Reject == "" {
			t.Errorf("entity %q unwraps %q but selects no rejection; a refusal would arrive bare", e.Name, e.Wrap)
		}
		if e.Wrap == "" && e.Reject != "" {
			t.Errorf("entity %q selects a rejection but has no envelope to unwrap", e.Name)
		}
	}
}

// Bulk has to agree with the mutation's RETURN type. A list decoded as one
// object, or the reverse, fails at the far end of an upgrade drill with a JSON
// error — while the schema said which it was all along.
//
// 🔑 THIS IS WHAT IS LEFT of a longer test that also matched each document's
// names against the schema text: the mutation, the read query, the input type,
// the argument name and the read's argument spelling. Every one of those is now
// checked by documents_test.go with the SERVER'S OWN VALIDATOR, which reads the
// selection set too — something string matching never did, and the reason a
// broken document could ship. Two checks of the same claim is one that can drift
// from the other, so the approximation went and the real rule stayed.
//
// The return type does NOT go with it, because it is not a claim about the
// document at all: the document validates either way. It is a claim about how
// this tool DECODES the response, which no validator can make on its behalf.
//
// Skipped for an envelope: there the created object is nested one level down, so
// the outer return type says nothing about it. No entry is both wrapped and bulk
// today, and the check would silently pass if one were.
func TestBulkAgreesWithTheDeclaredReturnType(t *testing.T) {
	byArea := map[string]string{}
	for _, e := range entities {
		if e.Wrap != "" {
			continue
		}
		schema, ok := byArea[e.Area]
		if !ok {
			schema = schemaFor(t, e.Area)
			byArea[e.Area] = schema
		}
		returns, ok := returnTypeOf(schema, e.Mutation)
		if !ok {
			t.Errorf("entity %q: could not read the return type of %s", e.Name, e.Mutation)
			continue
		}
		if isList := strings.HasPrefix(returns, "["); isList != e.Bulk {
			t.Errorf("entity %q: Bulk=%v but %s returns %s", e.Name, e.Bulk, e.Mutation, returns)
		}
	}
}

// returnTypeOf reads the declared result type of a field, e.g. "[Device!]!".
// The argument list holds no closing paren of its own, which is what makes the
// lazy match safe here.
func returnTypeOf(schema, field string) (string, bool) {
	m := regexp.MustCompile(`(?m)^[\t ]+` + regexp.QuoteMeta(field) + `\s*\([^)]*\)\s*:\s*(\S+)\s*$`).
		FindStringSubmatch(schema)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// 🔑 THE AREA LIST IS A SPECIFICATION, not a printout. The upgrade rig reads it
// to decide which functional areas to DEPLOY, because outbound-connectors is held
// out of the `default` profile — so an empty or short list means the rig
// bootstraps an instance with no route for a row it is about to write, and the
// refusal that follows looks like the API declining rather than like a service
// that was never installed.
//
// The coverage is computed a second way here rather than read back from the same
// helper: a test that took its expectations from the production list would agree
// with any list at all, including an empty one.
func TestTheAreaListCoversEveryEntityExactlyOnce(t *testing.T) {
	got := probeAreas()

	// allEntities, so a publish in an area no create touches is not silently
	// dropped from what the rig deploys.
	want := map[string]bool{}
	for _, e := range allEntities() {
		want[e.Area] = true
	}
	if len(got) != len(want) {
		t.Errorf("probeAreas returned %d areas for %d distinct areas in the table: %v", len(got), len(want), got)
	}
	seen := map[string]bool{}
	for _, a := range got {
		if seen[a] {
			t.Errorf("%q is listed twice; a rig would pass --enable-area for it twice", a)
		}
		seen[a] = true
		if !want[a] {
			t.Errorf("%q is listed but nothing writes to it", a)
		}
	}
	for a := range want {
		if !seen[a] {
			t.Errorf("no entity's area %q is listed, so a rig would not deploy it and every row there would be refused", a)
		}
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("the area list is not sorted, so its output is not stable between builds: %v", got)
	}
}

func stripSpace(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// The documents are generated rather than written, so the create and read
// selections cannot drift apart. This pins the generation itself: both must
// carry Fields verbatim, and the create must ask for the entity by the argument
// name the schema uses.
func TestBothDocumentsCarryTheSameSelection(t *testing.T) {
	for _, e := range entities {
		create, read := e.createDoc(), e.readDoc()
		if !strings.Contains(create, e.Fields) {
			t.Errorf("entity %q: the create document does not carry Fields", e.Name)
		}
		if !strings.Contains(read, e.Fields) {
			t.Errorf("entity %q: the read document does not carry Fields", e.Name)
		}
		if !strings.Contains(create, e.arg()+":$req") {
			t.Errorf("entity %q: the create document does not pass $req as %q", e.Name, e.arg())
		}
		if e.Wrap != "" && !strings.Contains(create, e.Wrap+"{"+e.Fields+"}") {
			t.Errorf("entity %q: the envelope does not wrap Fields", e.Name)
		}
	}
}

// Dependencies travel through the state by token, and the table is ORDERED so
// that a producer runs before its consumers. Nothing in the type system enforces
// that: a reordering leaves the consumer reading an EMPTY string out of the map,
// which most creates accept as an absent optional and store as a half-built
// entity — a silent gap in the drill, not a failure.
func TestEveryEntityCanBeBuiltInTableOrder(t *testing.T) {
	st := newState("apiprobe")
	for _, e := range entities {
		vars := e.Vars(st)
		req, ok := vars["req"]
		if !ok {
			t.Errorf("entity %q builds no $req variable", e.Name)
			continue
		}
		for _, field := range referencedTokens(req) {
			if field.value == "" {
				t.Errorf("entity %q reads an empty %q; its producer runs later in the table (or not at all)",
					e.Name, field.name)
			}
		}
		// Stand in for the platform: record the token this entity would have
		// been given, so the entries that depend on it see a non-empty value.
		if e.Record != nil {
			e.Record(st, map[string]any{"token": e.Name + "-token"})
		}
	}
}

type namedValue struct {
	name  string
	value string
}

// referencedTokens returns the create input's *Token fields plus the few that
// name a token without saying so (a relationship's endpoints, a batch's device
// list), which are exactly the fields fed from the state.
func referencedTokens(req any) []namedValue {
	var out []namedValue
	collect := func(m map[string]any) {
		for k, v := range m {
			switch k {
			case "source", "target", "relationshipType":
			default:
				if !strings.HasSuffix(k, "Token") {
					continue
				}
			}
			if s, ok := v.(string); ok {
				out = append(out, namedValue{name: k, value: s})
			}
		}
		if list, ok := m["deviceTokens"].([]any); ok {
			for i, v := range list {
				if s, ok := v.(string); ok {
					out = append(out, namedValue{name: "deviceTokens[" + string(rune('0'+i)) + "]", value: s})
				}
			}
		}
	}

	switch t := req.(type) {
	case map[string]any:
		collect(t)
	case []any:
		for _, item := range t {
			if m, ok := item.(map[string]any); ok {
				collect(m)
			}
		}
	}
	return out
}
