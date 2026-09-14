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

func TestEveryReservedSiblingResolvesUnderTheRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	names := ReservedNames()
	if len(names) == 0 {
		t.Fatal("the registry is empty, so every assertion below is vacuous")
	}
	for _, name := range names {
		got, err := Sibling(name)
		if err != nil {
			t.Fatalf("Sibling(%q): %v", name, err)
		}
		if want := filepath.Join(home, ".devicechain", name); got != want {
			t.Errorf("Sibling(%q) = %q, want %q", name, got, want)
		}
		if what, ok := Reserved(name); !ok || what == "" {
			t.Errorf("Reserved(%q) = %q, %v; every entry needs a phrase for the refusal message", name, what, ok)
		}
	}
}

// TestAnUnregisteredSiblingIsRefused is the property the package exists for. A
// helper that built the path anyway would put a directory under ~/.devicechain that
// neither consumer of the registry knows about — the defect, recreated by the thing
// meant to prevent it.
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
	if !strings.Contains(err.Error(), "registry") {
		t.Errorf("refusal does not point at the registry: %v", err)
	}
}

// TestNothingIsReservedThatDoesNotExist keeps the registry honest in the other
// direction. A name reserved "for later" refuses an instance name on behalf of a
// directory nothing creates, and the reservation outlives whatever was planned —
// the same reasoning as the embed allowlist's TestNothingIsExemptedThatDoesNotExist.
//
// It is checked against the packages that create these directories rather than
// against a disk, so it holds on a machine that has never run dcctl.
func TestNothingIsReservedThatDoesNotExist(t *testing.T) {
	creators := map[string]string{
		Escrow: "bootstrap/escrow.go writes root-key artifacts there",
		Sims:   "sim/record.go keeps simulator records there",
	}
	for _, name := range ReservedNames() {
		if _, ok := creators[name]; !ok {
			t.Errorf("%q is reserved but nothing in dcctl creates it; a reservation with no "+
				"directory behind it only refuses instance names for free", name)
		}
	}
	for name := range creators {
		if _, ok := Reserved(name); !ok {
			t.Errorf("%q is created but not reserved", name)
		}
	}
}
