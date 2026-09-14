// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package dcdir owns ~/.devicechain: the one spelling of the directory dcctl keeps
// its local state in, and the inventory of what dcctl puts directly inside it.
//
// # THE LAYOUT, AND WHY INSTANCES ARE NESTED
//
//	~/.devicechain/
//	  instances/<name>/   infra/  instance.json  broker-credentials.json
//	  clusters/<uid>/     cluster.json
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
	"strings"
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

	// Clusters holds one directory per cluster dcctl has installed prerequisites on.
	//
	// 🔴 IT IS KEYED ON THE CLUSTER'S IDENTITY, NEVER ON A NAME. A kind cluster
	// deleted and recreated wears the same context name — `kind delete cluster` then
	// `kind create cluster` produces byte-identical `kind-<name>` — while being a
	// different cluster holding none of the resources the old state describes.
	// Measured, not assumed: two rounds on one name gave kube-system UIDs
	// 163e7f17-… and 446b60a1-…, each stable while its cluster lived. So the
	// directory under here is named by the UID, and the context name is recorded
	// INSIDE it for a human to read.
	Clusters = "clusters"

	// Sims holds `dcctl sim` records, one JSON file per simulator.
	Sims = "sims"
)

// members maps each directory dcctl creates under the root to a noun phrase naming
// what it holds, for messages and for the tests that keep this list honest.
var members = map[string]string{
	Instances: "the per-instance state directories",
	Clusters:  "the per-cluster prerequisite state directories",
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

// Cluster returns ~/.devicechain/clusters/<uid>, without creating it.
//
// 🔴 UNLIKE Instance, THIS ONE VALIDATES, AND THE ASYMMETRY IS THE POINT. An instance
// name is an operator's word that may already be on disk from an older dcctl, so
// refusing one here would disarm the cleanup paths that have to remain able to remove
// whatever is there. A cluster UID is not a word anybody chose: it is read back from
// the API server, and every value this cannot build a path from — empty above all —
// means the READ failed rather than that an unusual cluster was found. An empty uid
// resolves to the clusters directory itself, which is every cluster's state rather
// than one cluster's, and the caller that would then remove it is a caller acting on
// a failure it did not notice.
func Cluster(uid string) (string, error) {
	switch {
	case uid == "":
		return "", fmt.Errorf(
			"cluster identity is empty, so there is no per-cluster directory to name; " +
				"this means reading the cluster's identity failed, not that the cluster has none")
	case uid == "." || uid == "..":
		return "", fmt.Errorf("cluster identity %q is not a usable directory name", uid)
	case strings.ContainsAny(uid, `/\`) || strings.Contains(uid, ".."):
		return "", fmt.Errorf("cluster identity %q may not contain a path separator or \"..\"", uid)
	}
	dir, err := Sibling(Clusters)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, uid), nil
}
