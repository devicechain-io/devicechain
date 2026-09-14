// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package dcdir owns ~/.devicechain: the one spelling of the directory dcctl keeps
// its local state in, and the registry of the names under it that are NOT instances.
//
// # WHY A PACKAGE FOR A STRING
//
// Everything under ~/.devicechain is a per-instance directory named after the
// instance — except the SIBLINGS, which are named after what they hold. There were
// two of those and nothing connecting them, which is how the second one broke.
//
// A sibling has to be known in two places that have no reason to know about each
// other: ValidateInstanceName must REFUSE it, so no instance can be bootstrapped on
// top of it, and ListInstances must SKIP it, so it is not enumerated as an instance
// that destroy will then act on. Both lists were written by hand, and `escrow` was
// in both. `sims` — ~/.devicechain/sims, where dcctl sim keeps its records — was in
// neither, because the package that creates it does not import the package that
// enumerates them and nothing asked the question.
//
// 🔴 WHAT THAT COST, MEASURED ON A REAL MACHINE RATHER THAN REASONED. `dcctl
// instances list` reported `sims` as an instance with no record. `dcctl destroy
// --all` therefore put it in the table, counted it in the confirmation the operator
// answers, GUESSED its binding (GuessBinding is Managed:true, cluster `sims`), tried
// to delete a kind cluster of that name, and called removeInstanceState on
// ~/.devicechain/sims — which is every simulator record on the machine.
//
// So the registry below is the single place a sibling is declared, both consumers
// read it, and neither can fall behind the other. The literal ".devicechain" lives
// here alone, enforced by TestOnlyThisPackageSpellsTheConfigDirectory, so a new
// sibling cannot be created anywhere else without going through this file — where
// the registry is the next thing the author reads.
package dcdir

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// DirName is the configuration directory's name under the operator's home. It is
// spelled here and nowhere else in non-test code; see the package comment.
const DirName = ".devicechain"

// The reserved siblings. Each is a directory under ~/.devicechain that holds
// something other than an instance, so no instance may be named after it.
const (
	// Escrow holds root-key escrow artifacts, one per instance. It is deliberately
	// NOT inside the per-instance directories, because `dcctl destroy` removes
	// those whole and an escrow artifact must outlive the cluster it opens.
	Escrow = "escrow"

	// Sims holds `dcctl sim` records, one JSON file per simulator.
	Sims = "sims"
)

// reserved maps each sibling to a noun phrase naming what it holds. The phrase is
// used in the refusal an operator sees, so it reads as the end of "instance name
// %q collides with ___ under ~/.devicechain".
var reserved = map[string]string{
	Escrow: "the root-key escrow directory",
	Sims:   "the simulator record directory",
}

// Reserved reports whether name is a sibling rather than an instance, and if so
// returns the phrase naming what it holds.
func Reserved(name string) (string, bool) {
	what, ok := reserved[name]
	return what, ok
}

// ReservedNames returns every reserved sibling, sorted. For callers that need to
// enumerate rather than ask — chiefly the tests that hold the two consumers to
// this one list.
func ReservedNames() []string {
	out := make([]string, 0, len(reserved))
	for name := range reserved {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Root returns ~/.devicechain WITHOUT creating it. Creating is the caller's job,
// and the callers that do it apply their own mode — 0700, because the tree holds
// cleartext secrets.
func Root() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, DirName), nil
}

// Sibling returns the path of a reserved sibling, without creating it.
//
// It REFUSES an unregistered name rather than building the path anyway. That is the
// whole point of the package: a directory under ~/.devicechain that is not an
// instance and not in the registry is the defect this exists to prevent, and a
// helper that cheerfully returns a path for one would reintroduce it while looking
// like it had been done properly.
func Sibling(name string) (string, error) {
	if _, ok := reserved[name]; !ok {
		return "", fmt.Errorf(
			"%q is not a registered ~/%s sibling; add it to the registry in dcdir, "+
				"which is what makes instance-name validation and instance enumeration "+
				"agree that it is not an instance", name, DirName)
	}
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}
