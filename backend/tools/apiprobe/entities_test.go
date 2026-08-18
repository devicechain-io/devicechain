// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"regexp"
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

// Every entry has to be usable: a create document, a read document, both response
// keys, and a variable builder. A half-filled row would seed nothing and verify
// nothing while still counting toward the coverage number printed to an operator.
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

		if e.Name == "" || e.Area == "" || e.Create == "" || e.Read == "" ||
			e.CreateKey == "" || e.ReadKey == "" || e.Vars == nil {
			t.Errorf("entity %q is incomplete", e.Name)
			continue
		}
		if !strings.Contains(e.Create, e.CreateKey) {
			t.Errorf("entity %q: CreateKey %q does not appear in its mutation", e.Name, e.CreateKey)
		}
		if !strings.Contains(e.Read, e.ReadKey) {
			t.Errorf("entity %q: ReadKey %q does not appear in its query", e.Name, e.ReadKey)
		}
	}
}

// 🔑 The comparison is between the create response and the read response, so a
// read that selects FEWER fields silently narrows what this tool can detect —
// and narrows it invisibly, because the missing field simply never differs.
// (A read selecting MORE would fail loudly on the first run, which is why only
// one direction needs pinning.)
func TestReadSelectsEveryFieldTheCreateReturns(t *testing.T) {
	selection := regexp.MustCompile(`\{([a-zA-Z ]+)\}\s*\}?\s*$`)
	for _, e := range entities {
		created := selection.FindStringSubmatch(e.Create)
		read := selection.FindStringSubmatch(e.Read)
		if created == nil || read == nil {
			t.Errorf("entity %q: could not extract a field selection from its documents", e.Name)
			continue
		}
		want := strings.Fields(created[1])
		got := map[string]bool{}
		for _, f := range strings.Fields(read[1]) {
			got[f] = true
		}
		for _, f := range want {
			if !got[f] {
				t.Errorf("entity %q: create selects %q but the read-back query does not", e.Name, f)
			}
		}
	}
}
