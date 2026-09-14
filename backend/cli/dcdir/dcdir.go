// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package dcdir owns ~/.devicechain: the one spelling of the directory dcctl keeps
// its local state in, and the inventory of what dcctl puts directly inside it.
//
// # THE LAYOUT, AND WHY INSTANCES ARE NESTED
//
//	~/.devicechain/
//	  instances/<name>/   infra/  instance.json  broker-credentials.json
//	  escrow/             <instance>-rootkey.escrow
//	  sims/               <name>.json
//
// Instances used to sit directly under the root, as peers of escrow and sims. That
// made an instance NAME and a directory name the same namespace, so every sibling
// had to be known in two places that have no reason to know about each other:
// ValidateInstanceName had to refuse it, and ListInstances had to skip it. Both
// lists were written by hand, `escrow` was in both, and `sims` was in neither —
// which meant `dcctl instances list` reported the simulator record directory as an
// instance, and `dcctl destroy --all` counted it in the confirmation, guessed a
// cluster from its name and cleared the directory.
//
// 🔑 NESTING MAKES THE DISTINCTION THE RESERVATION WAS GUARDING. An instance lives
// under instances/, so it cannot collide with a sibling whatever it is called, and
// ListInstances reads one directory in which everything IS an instance. There is no
// list to fall behind, because there is no list.
//
// 🔴 AND THERE IS DELIBERATELY NO MIGRATION FROM THE OLD LAYOUT. Moving whatever is
// not a recognised sibling into instances/ is the obvious thing and it is wrong:
// this registry knows the directories DCCTL creates, not the ones an operator does,
// so a hand-made ~/.devicechain/notes would be swallowed by it. Pre-GA, an instance
// from the old layout is destroyed and re-bootstrapped, which is the convention
// already in force for schema changes. An operator who runs destroy against one gets
// the GUESSING warning rather than a silent no-op, because nothing here reads the
// old location at all.
//
// The literal ".devicechain" lives here alone, enforced by
// TestOnlyThisPackageSpellsTheConfigDirectory, so nothing can be created under the
// root without going through this file — where the inventory is the next thing the
// author reads.
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

// What dcctl puts directly under ~/.devicechain. This is the whole inventory:
// anything else appearing there was not created by dcctl.
const (
	// Instances holds one directory per instance. Nesting them is what stops an
	// instance name from colliding with a sibling; see the package comment.
	Instances = "instances"

	// Escrow holds root-key escrow artifacts, one per instance. It is deliberately
	// NOT inside the per-instance directories, because `dcctl destroy` removes
	// those whole and an escrow artifact must outlive the cluster it opens. Its
	// path is published in the deployment docs, so it stays where it is.
	Escrow = "escrow"

	// Sims holds `dcctl sim` records, one JSON file per simulator.
	Sims = "sims"
)

// members maps each directory dcctl creates under the root to a noun phrase naming
// what it holds, for messages and for the tests that keep this list honest.
var members = map[string]string{
	Instances: "the per-instance state directories",
	Escrow:    "the root-key escrow directory",
	Sims:      "the simulator record directory",
}

// Member reports whether name is something dcctl creates directly under the root,
// and if so returns the phrase naming what it holds.
func Member(name string) (string, bool) {
	what, ok := members[name]
	return what, ok
}

// MemberNames returns the inventory, sorted.
func MemberNames() []string {
	out := make([]string, 0, len(members))
	for name := range members {
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

// Sibling returns the path of one inventory member, without creating it.
//
// It REFUSES a name that is not in the inventory rather than building the path
// anyway. A directory appearing under the root that this package does not know
// about is the thing the package exists to prevent, and a helper that cheerfully
// returned a path for one would reintroduce it while looking careful.
func Sibling(name string) (string, error) {
	if _, ok := members[name]; !ok {
		return "", fmt.Errorf(
			"%q is not in dcdir's inventory of what lives under ~/%s; add it there, "+
				"which is the one place that says what dcctl creates under the root",
			name, DirName)
	}
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

// Instance returns ~/.devicechain/instances/<name>, without creating it and
// WITHOUT validating the name.
//
// 🔴 NOT VALIDATING IS DELIBERATE, and it is a correction carried over from this
// path's previous home: callers on the cleanup paths read an error here as "no home
// directory", so rejecting a name in this funnel silently disarms them. Whatever is
// already on disk has to remain destroyable, including anything an older dcctl
// created. A NEW name is validated where it enters.
func Instance(name string) (string, error) {
	dir, err := Sibling(Instances)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}
