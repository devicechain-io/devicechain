// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"path/filepath"
	"sort"
	"testing"
)

// knownAnonymousMutations is the REMAINING debt: files that still mutate an audited
// row through a zero-value model while naming that row by primary key, with the number
// of such statements in each.
//
// 🔴 IT IS ENFORCED IN BOTH DIRECTIONS, and that is what stops it becoming the usual
// rotting suppression file. A file with MORE than its entry fails, because that is new
// debt. A file with FEWER also fails, because the entry is stale and the next reader
// would take it as an accurate account of what is left. Fixing a site means editing
// this table down in the same commit; the table reaching zero means the entry is
// deleted, and when the map is empty this check is a plain assertion again.
//
// Counts, not line numbers: a line number turns every unrelated edit above it into a
// failure here, which teaches people to re-run and paste rather than to read.
//
// 🔴 THE DEBT IS ZERO as of 2026-09-20, and the map is deliberately kept rather than
// deleted along with it. It is the difference between "nothing is listed" and "nothing
// was found", and the next person to add an entry should have to write it here — where
// the rule above says it has to come back out again — rather than discover the mechanism
// from scratch and reach for a //nolint.
//
// All 21 the scanner originally found are fixed: 11 by handing over the loaded row
// (which supplies the label as well), 9 command-delivery state transitions by seeding
// the key into the literal, since those run on the dispatch path and loading a row for a
// label would buy one query per transition. The twenty-first was not a defect at all —
// a batch update, excluded from the check itself. See
// AssertEveryIdentifiedMutationNamesItsRow for why this is a guard and not a review note.
var knownAnonymousMutations = map[string]int{}

// Every audited mutation that identifies its row must let the journal name it.
//
// This is the repository-wide run; the scanner's own behaviour — that it fires on the
// real shape, reads a chain across line breaks, and stays quiet on the three shapes
// that are allowed to record nothing — is pinned in auditidentity_test.go.
func TestEveryIdentifiedMutationNamesItsRowInTheAuditJournal(t *testing.T) {
	root := workspaceRoot(t)
	found, visited, err := anonymousMutationsUnder(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	for _, dir := range workspaceModuleDirs(t, root) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			t.Fatalf("resolving %s: %v", dir, err)
		}
		if !visited[abs] {
			t.Errorf("the scan of %s never descended into %s, so whatever it reports about "+
				"that tree it did not look at. A clean scan and a scan that reached nothing "+
				"are the same answer, which is why this is checked separately", root, abs)
		}
	}

	counts := map[string][]AnonymousMutation{}
	for _, m := range found {
		rel, err := filepath.Rel(root, m.File)
		if err != nil {
			rel = m.File
		}
		counts[filepath.ToSlash(rel)] = append(counts[filepath.ToSlash(rel)], m)
	}

	for _, file := range sortedKeys(counts) {
		allowed := knownAnonymousMutations[file]
		actual := len(counts[file])
		if actual <= allowed {
			continue
		}
		for _, m := range counts[file][allowed:] {
			t.Errorf("%s: Model(&%s{}) is mutated by %s under %q, which names the row by "+
				"primary key — but the zero-value model leaves the audit journal with no "+
				"EntityPK and no EntityLabel, so the entry records that something changed "+
				"without recording what. Hand the statement the identity it already has: "+
				"Model(&loaded), or Model(&%s{Model: gorm.Model{ID: id}}), which costs no "+
				"extra query",
				m.Pos, m.Model, m.Operation, m.Condition, m.Model)
		}
	}

	for _, file := range sortedKeys(knownAnonymousMutations) {
		allowed := knownAnonymousMutations[file]
		actual := len(counts[file])
		if actual < allowed {
			t.Errorf("knownAnonymousMutations lists %d remaining in %s but the scan finds %d. "+
				"The entry is stale: lower it to %d, or delete the line if that is zero. An "+
				"over-stated ledger is how a suppression file outlives the debt it records",
				allowed, file, actual, actual)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
