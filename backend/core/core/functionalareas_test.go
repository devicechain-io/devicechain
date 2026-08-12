// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// writeAtomicWriterLayout builds the directory shape Kubernetes ACTUALLY projects
// a ConfigMap volume into — a timestamped data directory, a `..data` symlink to
// it, and one key symlink per area pointing through `..data`.
//
// A flat directory of files would be the obvious fixture and is the wrong one: it
// omits the two `..`-prefixed entries that are the whole reason the reader filters
// anything, so it would pass against an implementation that filters nothing. This
// mirrors a listing captured from a live pod.
func writeAtomicWriterLayout(t *testing.T, areas ...string) string {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "..2026_08_12_02_48_51.185100639")
	if err := os.Mkdir(data, 0o755); err != nil {
		t.Fatalf("creating timestamped data dir: %v", err)
	}
	for _, a := range areas {
		if err := os.WriteFile(filepath.Join(data, a), []byte("{}"), 0o644); err != nil {
			t.Fatalf("writing key %q: %v", a, err)
		}
	}
	if err := os.Symlink(data, filepath.Join(root, "..data")); err != nil {
		t.Fatalf("linking ..data: %v", err)
	}
	for _, a := range areas {
		if err := os.Symlink(filepath.Join("..data", a), filepath.Join(root, a)); err != nil {
			t.Fatalf("linking key %q: %v", a, err)
		}
	}
	return root
}

func TestEnabledFunctionalAreasReadsTheAtomicWriterLayout(t *testing.T) {
	dir := writeAtomicWriterLayout(t, "user-management", "device-management", "event-processing")

	got, err := EnabledFunctionalAreasIn(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"device-management", "event-processing", "user-management"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The negative control for the test above. It asserts the fixture really does
// contain the entries the reader has to filter — otherwise the fixture is a flat
// directory in disguise and the passing test above measures nothing.
func TestFixtureActuallyContainsTheEntriesThatMustBeFiltered(t *testing.T) {
	dir := writeAtomicWriterLayout(t, "user-management")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	var raw []string
	for _, e := range entries {
		raw = append(raw, e.Name())
	}
	// os.ReadDir omits "." and ".." but returns dotfiles, so an unfiltered reader
	// sees THREE entries for a one-area instance.
	if len(raw) != 3 {
		t.Fatalf("fixture does not reproduce the atomic-writer layout: got %v, want 3 entries "+
			"(..data, the timestamped dir, and the key)", raw)
	}
	if !slices.Contains(raw, "..data") {
		t.Errorf("fixture is missing ..data — it cannot prove the filter works: %v", raw)
	}
}

func TestEnabledFunctionalAreasReadsAFlatDirectory(t *testing.T) {
	// Not every deployment is a kubelet-projected volume: a developer running a
	// service outside the chart may populate the directory by hand.
	dir := t.TempDir()
	for _, a := range []string{"user-management", "device-management"} {
		if err := os.WriteFile(filepath.Join(dir, a), []byte("{}"), 0o644); err != nil {
			t.Fatalf("writing %q: %v", a, err)
		}
	}

	got, err := EnabledFunctionalAreasIn(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"device-management", "user-management"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestEnabledFunctionalAreasErrorsWhenTheDirectoryIsAbsent(t *testing.T) {
	// A service run outside the chart has no config mount at all. This must be an
	// error rather than an empty set: "no areas are deployed" is a claim the
	// console would act on, and it is never true.
	if _, err := EnabledFunctionalAreasIn(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected an error for a missing directory, got nil")
	}
}
