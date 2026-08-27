// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var publishMutation = regexp.MustCompile(`(?m)^[\t ]+publish[A-Z]\w*\s*\(`)

// Same reasoning as the create denominator, and the same trap: these files live
// OUTSIDE this module, so Go's test cache does not track them.
//
// 🔴 RUN WITH -count=1 AFTER TOUCHING A SCHEMA.
func TestThePublishDenominatorMatchesTheSchemas(t *testing.T) {
	schemas := servedSchemas(t)
	// A vacuous pass would look identical to a thorough one: a walk that found
	// nothing counts 0, and a future refactor could make both sides 0 and agree.
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
		if n := len(publishMutation.FindAll(body, -1)); n > 0 {
			perFile[s] = n
			counted += n
		}
	}
	if counted != tenantPublishMutations {
		t.Errorf("tenantPublishMutations = %d, schemas actually declare %d", tenantPublishMutations, counted)
		for f, n := range perFile {
			t.Logf("  %2d  %s", n, f)
		}
	}
}

// The table claims to cover every publish, and a green drill is read as covering
// what the table claims. A row short is a real gap; a row past means something is
// counted twice while something else is uncovered and the arithmetic still agrees.
func TestThePublishTableCoversEveryPublishMutation(t *testing.T) {
	if len(publishes) != tenantPublishMutations {
		t.Errorf("the publish table has %d entries for %d publish mutations",
			len(publishes), tenantPublishMutations)
	}
	seen := map[string]bool{}
	for _, p := range publishes {
		if seen[p.Mutation] {
			t.Errorf("two publishes go through %q", p.Mutation)
		}
		seen[p.Mutation] = true
	}
}

// 🔴 THE ONE THAT CANNOT BE CAUGHT BY A SCHEMA VALIDATOR. `Of` names the entity being
// published, and its token is derived from that name. A name that matches no row in
// the table yields a token nothing owns — and the publish would then be REFUSED at
// run time, at the far end of a drill, for a typo visible here in microseconds. The
// document validates perfectly either way, because `token` is just a String.
func TestEveryPublishNamesAnEntityInTheTable(t *testing.T) {
	known := map[string]bool{}
	for _, e := range entities {
		known[e.Name] = true
	}
	for _, p := range publishes {
		if !known[p.Of] {
			t.Errorf("publish %q publishes %q, which is not an entity in the table", p.Name, p.Of)
		}
	}
}

// A half-filled row would publish nothing and verify nothing while still counting
// toward the coverage number an operator reads.
func TestEveryPublishIsComplete(t *testing.T) {
	if len(publishes) == 0 {
		t.Fatal("the publish table is empty")
	}
	seen := map[string]bool{}
	for _, p := range publishes {
		if seen[p.Name] {
			// Names are receipt keys. A duplicate would make verify check one twice
			// and the other never, silently.
			t.Errorf("duplicate publish name %q", p.Name)
		}
		seen[p.Name] = true

		if p.Name == "" || p.Area == "" || p.Mutation == "" || p.Read == "" ||
			p.Of == "" || p.Fields == "" {
			t.Errorf("publish %q is incomplete", p.Name)
			continue
		}
		// The version number is what makes one version distinguishable from
		// another. A selection without it would compare two snapshots that agree
		// on every other field and call them the same row.
		if !strings.Contains(p.Fields, "version") {
			t.Errorf("publish %q does not select `version`; two snapshots would be indistinguishable", p.Name)
		}
	}
}

// 🔴 A PUBLISH MUST NOT COLLIDE WITH A CREATE ON THE RECEIPT. Both write Recorded
// rows keyed by Name into one list, and verify resolves them through one map — so a
// shared name would make one of the two silently unverifiable.
func TestPublishNamesDoNotCollideWithEntityNames(t *testing.T) {
	seen := map[string]string{}
	for _, e := range allEntities() {
		if prev, dup := seen[e.Name]; dup {
			t.Errorf("%q is used by both %s and %s", e.Name, prev, e.Mutation)
		}
		seen[e.Name] = e.Mutation
	}
	if len(seen) != len(entities)+len(publishes) {
		t.Errorf("allEntities() yielded %d distinct names for %d+%d rows",
			len(seen), len(entities), len(publishes))
	}
}

// THE COUNTERWEIGHT for the document tests: they prove a publish document is VALID,
// which a document reading back the wrong parent would also be. This pins the pairing
// — the mutation, its versions query, and the entity whose token both are keyed by.
func TestEachPublishReadsBackTheThingItPublished(t *testing.T) {
	st := newState("apiprobe")
	for _, p := range publishes {
		e := p.entity()
		vars := e.Vars(st)
		if got, want := vars["token"], st.tok(p.Of); got != want {
			t.Errorf("publish %q sends token %v, but %q's token is %v", p.Name, got, p.Of, want)
		}
		// deviceProfileVersions must belong to publishDeviceProfile, not to some
		// other area's publish that happens to validate.
		if !strings.Contains(e.readDoc(), p.Read+"(token:$token)") {
			t.Errorf("publish %q reads back through an unexpected document: %s", p.Name, e.readDoc())
		}
		if !strings.Contains(e.createDoc(), p.Mutation+"(token:$token") {
			t.Errorf("publish %q publishes through an unexpected document: %s", p.Name, e.createDoc())
		}
	}
}
