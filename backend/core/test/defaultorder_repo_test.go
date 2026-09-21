// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"path/filepath"
	"testing"
)

// Every model's declared list order must say which table its columns belong to.
//
// 🔴 WHY THIS IS A GUARD AND NOT A CONVENTION. Exactly one implementation in the tree
// failed to qualify its columns, and nothing anywhere would have said so.
// It was correct only for as long as no read of that
// table grew a join — and the failure it was waiting to produce is not a subtly wrong
// order but an outright refusal from Postgres (ambiguous column reference) on whichever
// query added the join, far from the method that chose the clause.
//
// 🔑 A CONVENTION FOLLOWED BY ALL BUT ONE IS A CONVENTION NOBODY IS CHECKING. That is the
// whole argument for asking the tree: the next model declares its order by copying
// whichever neighbour its author happened to open, so the single unqualified example was
// one copy away from being two.
//
// The scanner's own behaviour — that it reads the signature rather than the name alone,
// that it refuses a clause it cannot split, and that it fails rather than reporting clean
// when it recognizes nothing — is pinned in defaultorder_test.go. This file only runs it
// over the repository.
func TestEveryDeclaredListOrderNamesItsTable(t *testing.T) {
	root := workspaceRoot(t)
	found, total, visited, err := unqualifiedOrdersUnder(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	// 🔴 THE COUNT IS REPORTED, NOT WRITTEN DOWN. Twice a number for this was put in a
	// comment and twice it was wrong — the second time because it came from a grep, which
	// counts DefaultOrder lines sitting inside this package's own raw-string fixtures as
	// though they were declarations. The parser disagrees with the grep, and the parser is
	// what runs, so the scan reports its own total and nobody has to keep a number honest.
	t.Logf("scanned %d %s implementations under %s", total, orderMethod, root)

	// A floor, because the total==0 refusal only catches a matcher that broke COMPLETELY.
	// One that broke partially — a signature check that started rejecting most methods —
	// would sail past it while reporting clean over the handful it still recognized.
	const fewestPlausible = 30
	if total < fewestPlausible {
		t.Errorf("the scan recognized only %d %s implementations, which is far below the "+
			"tree's known population: a matcher that has partially stopped matching reports "+
			"clean over whatever it still sees, and that is indistinguishable from a tree "+
			"with nothing wrong in it", total, orderMethod)
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

	for _, u := range found {
		rel, err := filepath.Rel(root, u.File)
		if err != nil {
			rel = u.File
		}
		t.Errorf("%s: %s.DefaultOrder() returns %q, which %s",
			filepath.ToSlash(rel), u.Type, u.Order, u.Reason)
	}
}
