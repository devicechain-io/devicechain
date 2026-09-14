// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package dcdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootIsTheDirectoryUnderTheOperatorsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := Root()
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	// Spelled out rather than composed from DirName: this is the statement of
	// where dcctl's state actually lands, and a check built from the constant
	// would follow the constant anywhere it moved.
	if want := filepath.Join(home, ".devicechain"); got != want {
		t.Fatalf("Root() = %q, want %q", got, want)
	}
}

func TestRootDoesNotCreateAnything(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Root(); err != nil {
		t.Fatalf("Root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".devicechain")); !os.IsNotExist(err) {
		t.Fatalf("Root created the directory (stat err = %v); callers apply their own mode", err)
	}
}

func TestEveryInventoryMemberResolvesUnderTheRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	names := MemberNames()
	if len(names) == 0 {
		t.Fatal("the inventory is empty, so every assertion below is vacuous")
	}
	for _, name := range names {
		got, err := Sibling(name)
		if err != nil {
			t.Fatalf("Sibling(%q): %v", name, err)
		}
		if want := filepath.Join(home, ".devicechain", name); got != want {
			t.Errorf("Sibling(%q) = %q, want %q", name, got, want)
		}
		if what, ok := Member(name); !ok || what == "" {
			t.Errorf("Member(%q) = %q, %v; every entry needs a phrase naming what it holds", name, what, ok)
		}
	}
}

// TestAnInstanceLivesUnderTheInstancesDirectory is the property the whole layout
// exists for: an instance path is one level deeper than a sibling, so the two
// namespaces cannot meet.
func TestAnInstanceLivesUnderTheInstancesDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := Instance("prod")
	if err != nil {
		t.Fatalf("Instance: %v", err)
	}
	if want := filepath.Join(home, ".devicechain", "instances", "prod"); got != want {
		t.Fatalf("Instance(\"prod\") = %q, want %q", got, want)
	}
}

// TestAnInstanceMayBeNAMEDAfterASibling is what nesting bought. While instances sat
// directly under the root these names had to be REFUSED, in two places that each
// kept their own list — and the list that was missing `sims` is why
// ~/.devicechain/sims was enumerated as an instance and cleared by destroy --all.
// Now the name is merely a name.
func TestAnInstanceMayBeNAMEDAfterASibling(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, name := range MemberNames() {
		got, err := Instance(name)
		if err != nil {
			t.Fatalf("Instance(%q): %v", name, err)
		}
		sibling, err := Sibling(name)
		if err != nil {
			t.Fatalf("Sibling(%q): %v", name, err)
		}
		if got == sibling {
			t.Errorf("instance %q resolves to the sibling path %q", name, got)
		}
		if want := filepath.Join(home, ".devicechain", "instances", name); got != want {
			t.Errorf("Instance(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestAnUnregisteredSiblingIsRefused keeps the inventory the whole truth. A helper
// that built the path anyway would put a directory under ~/.devicechain that this
// package does not account for, which is what the literal gate exists to stop
// happening anywhere else.
func TestAnUnregisteredSiblingIsRefused(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	got, err := Sibling("mystery")
	if err == nil {
		t.Fatalf("Sibling(%q) = %q, want an error", "mystery", got)
	}
	if got != "" {
		t.Errorf("Sibling returned %q alongside its error; a caller ignoring the error would still build the path", got)
	}
	// The refusal has to say what to do about it, because the fix is in a file the
	// author is not looking at.
	if !strings.Contains(err.Error(), "inventory") {
		t.Errorf("refusal does not point at the inventory: %v", err)
	}
}

// TestNothingIsInventoriedThatDoesNotExist keeps the inventory honest in the other
// direction. An entry added "for later" describes a directory nothing creates, and
// it outlives whatever was planned — the same reasoning as the embed allowlist's
// TestNothingIsExemptedThatDoesNotExist.
//
// It is checked against the packages that create these directories rather than
// against a disk, so it holds on a machine that has never run dcctl.
func TestNothingIsInventoriedThatDoesNotExist(t *testing.T) {
	creators := map[string]string{
		Instances: "bootstrap/tofu.go creates per-instance state directories there",
		Clusters:  "bootstrap/cluster_identity.go writes a cluster record there",
		Escrow:    "bootstrap/escrow.go writes root-key artifacts there",
		Sims:      "sim/record.go keeps simulator records there",
	}
	for _, name := range MemberNames() {
		if _, ok := creators[name]; !ok {
			t.Errorf("%q is in the inventory but nothing in dcctl creates it; an entry with "+
				"no directory behind it only describes a plan", name)
		}
	}
	for name := range creators {
		if _, ok := Member(name); !ok {
			t.Errorf("%q is created but not in the inventory", name)
		}
	}
}

// TestAClusterLivesUnderTheClustersDirectory is the instances property one sibling over:
// a cluster's state is a level below clusters/, so it shares a namespace with no other
// cluster's and with nothing else under the root.
func TestAClusterLivesUnderTheClustersDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := Cluster("163e7f17-d87c-42fe-8bc0-e672e35f5ee7")
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	want := filepath.Join(home, ".devicechain", "clusters", "163e7f17-d87c-42fe-8bc0-e672e35f5ee7")
	if got != want {
		t.Fatalf("Cluster = %q, want %q", got, want)
	}
}

// TestAnEmptyClusterIdentityIsRefused is the asymmetry with Instance, and it is the whole
// reason Cluster validates at all.
//
// 🔴 THE EMPTY CASE IS THE ONE THAT MATTERS AND IT IS THE ONE THAT LOOKS HARMLESS. A
// failed identity read hands the empty string onward; without this, filepath.Join folds
// it away and the path resolves to the clusters directory ITSELF — so a caller that
// meant to act on one cluster's state acts on every cluster's, having never been told
// the read failed. The separator cases are the ordinary directory-traversal guard.
func TestAnEmptyClusterIdentityIsRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	clusters, err := Sibling(Clusters)
	if err != nil {
		t.Fatalf("Sibling: %v", err)
	}
	for _, uid := range []string{"", ".", "..", "a/b", `a\b`, "../escape", "x/../y"} {
		got, err := Cluster(uid)
		if err == nil {
			t.Errorf("Cluster(%q) = %q, want an error", uid, got)
			if got == clusters {
				t.Errorf("  and it resolved to the clusters directory itself, which is every cluster's state")
			}
		}
	}
}

// TestAValidIdentityIsStillAccepted is the counterweight. A validator that refused
// everything would pass the test above and break the only caller there is, so the shape
// the API server actually returns is pinned here — both the UUIDs measured on a real
// kind rebuild, which is where this key came from.
func TestAValidIdentityIsStillAccepted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, uid := range []string{
		"163e7f17-d87c-42fe-8bc0-e672e35f5ee7",
		"446b60a1-b6f8-4cf0-9e14-ced15bc26170",
	} {
		if _, err := Cluster(uid); err != nil {
			t.Errorf("Cluster(%q): %v", uid, err)
		}
	}
}
