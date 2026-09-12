// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
)

// 🔴 THIS IS THE WIRING TEST, AND IT IS THE HALF NOTHING ELSE COVERS. The rollback
// machinery is tested in the bootstrap package against its own API; what is tested here
// is that the command layer CALLS it, and calls it for the right error. A capture and a
// Restore that are both correct and connected to nothing is the recurring defect this
// pair exists to rule out.

// refusedHome puts a record on disk exactly as `dcctl bootstrap` does before the pipeline
// starts, and returns the capture taken beforehand.
func refusedHome(t *testing.T, instance string) (home string, prior bootstrap.PriorLocalState) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	prior = bootstrap.CapturePriorLocalState(instance)
	if err := bootstrap.WriteInstanceRecord(bootstrap.InstanceRecord{
		Instance: instance, Provider: "local", Cluster: "alpha-cluster",
		KubeContext: "kind-alpha-cluster",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".devicechain", instance)); err != nil {
		t.Fatalf("the fixture never wrote the record this test is about: %v", err)
	}
	return home, prior
}

func recordDirExists(t *testing.T, home, instance string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(home, ".devicechain", instance))
	return err == nil
}

func TestTheCommandLayerClearsTheRecordAfterASecondInstanceRefusal(t *testing.T) {
	home, prior := refusedHome(t, "bravo")

	// Wrapped the way Pipeline.Run wraps every step error — an unwrapped fixture would
	// pass while the real path never matched.
	refusal := fmt.Errorf("step %q: %w", "Refuse a second instance", &bootstrap.ErrSecondInstance{
		Holds: []string{"alpha"}, Wanted: "bravo", Provider: "local",
	})
	unwindLocalRecordOnSecondInstance(bootstrap.Options{Instance: "bravo"}, prior, refusal)

	if recordDirExists(t, home, "bravo") {
		t.Fatal("`dcctl instances list` would still show an instance that was refused before " +
			"anything was installed, and no dcctl path can clear it")
	}
}

// 🔴 THE NEGATIVE CONTROL, AND IT IS WHY THIS IS KEYED ON THE ERROR RATHER THAN ON
// FAILURE. Every other way a bootstrap can fail may have left a cluster half-built, and
// the record is the only thing that can name it. Widening this to "any error" restores
// the orphan the record exists to prevent.
func TestEveryOtherFailureKeepsTheRecord(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"a step that failed for some other reason", errors.New("step \"Apply infrastructure\": boom")},
		{"the rebuild refusal, which is about the SAME instance", errors.New(
			"step \"Refuse a rebuild\": instance \"bravo\" is already running in this cluster")},
		{"a successful run", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, prior := refusedHome(t, "bravo")
			unwindLocalRecordOnSecondInstance(bootstrap.Options{Instance: "bravo"}, prior, tc.err)
			if !recordDirExists(t, home, "bravo") {
				t.Fatal("the record was cleared after a failure that may have left a cluster " +
					"behind, so nothing can name the cluster to destroy it")
			}
		})
	}
}

// A dry run writes no record, so there is nothing to undo — and undoing anyway would act
// on a starting point this run never changed.
func TestADryRunIsNotUnwound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	prior := bootstrap.CapturePriorLocalState("bravo")
	// A live record for the same name, of the kind a dry run must never touch.
	if err := bootstrap.WriteInstanceRecord(bootstrap.InstanceRecord{
		Instance: "bravo", Provider: "local", Cluster: "bravo-cluster",
	}); err != nil {
		t.Fatal(err)
	}

	refusal := fmt.Errorf("step %q: %w", "Refuse a second instance",
		&bootstrap.ErrSecondInstance{Holds: []string{"alpha"}, Wanted: "bravo"})
	unwindLocalRecordOnSecondInstance(
		bootstrap.Options{Instance: "bravo", DryRun: true}, prior, refusal)

	if !recordDirExists(t, home, "bravo") {
		t.Fatal("a --dry-run removed a record it never wrote")
	}
}
