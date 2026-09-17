// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The cluster prerequisite state lives under ~/.devicechain/clusters/<uid>, outside the
// instance tree that destroy has always removed. Before the prerequisites had a root of
// their own, a teardown took all of the infrastructure state with the instance directory;
// after it, the prerequisite half outlived its cluster and one more accumulated with each
// rebuild — found live, on the first round-trip, not by a test.
//
// 🔑 THE RULE IS ABOUT THE CLUSTER, NOT THE COMMAND. State describing a cluster that no
// longer exists describes nothing, and the UID it is filed under can never come back. State
// describing a cluster that is still RUNNING describes installed prerequisites, and
// removing it would leave them unmanaged. So every row below turns on whether the cluster
// is gone when the command finishes.

// plantClusterState writes what a bootstrap leaves under the cluster directory: the record,
// and a prerequisite state file one level down.
func plantClusterState(t *testing.T, home, uid string) string {
	t.Helper()
	dir := filepath.Join(home, ".devicechain", "clusters", uid)
	if err := os.MkdirAll(filepath.Join(dir, "infra", "cluster"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"cluster.json", filepath.Join("infra", "cluster", "terraform.tfstate")} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The UID measured on a real kind cluster (see cluster_identity_test.go), and a second
// one standing for a different cluster on the same machine.
const (
	destroyedClusterUID = "163e7f17-d87c-42fe-8bc0-e672e35f5ee7"
	bystanderClusterUID = "446b60a1-5c0e-4a8e-9d8f-2b4a3e6f7c10"
)

func TestAClusterThatIsGoneTakesItsPrerequisiteStateWithIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		managed bool
		present bool
	}{
		{"managed, already gone", true, false},
		{"adopted, already gone", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := fakeHome(t)
			writeRecord(t, InstanceRecord{
				Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c",
				Managed: tc.managed, ClusterUID: destroyedClusterUID,
			})
			gone := plantClusterState(t, home, destroyedClusterUID)
			// 🔴 THE BYSTANDER. A removal that cleared clusters/ wholesale — or resolved an
			// empty UID to the clusters directory itself — would pass the assertion above it
			// and delete the state of a cluster that is still running.
			kept := plantClusterState(t, home, bystanderClusterUID)
			p := &fakeProvider{name: "local", present: map[string]bool{"c": tc.present}}

			captureOutput(t, func() {
				if err := Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "inst", AssumeYes: true}}); err != nil {
					t.Errorf("Destroy: %v", err)
				}
			})

			if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the prerequisite state of a cluster that is gone survived the destroy (%v); "+
					"it describes nothing, and one more accumulates with every rebuild", err)
			}
			if _, err := os.Stat(filepath.Join(kept, "infra", "cluster", "terraform.tfstate")); err != nil {
				t.Errorf("destroy removed ANOTHER cluster's prerequisite state: %v", err)
			}
		})
	}
}

// 🔴 AND EVERY PATH THAT LEAVES THE CLUSTER RUNNING KEEPS IT. The prerequisites are still
// installed there, and their state is the only description of them.
func TestAClusterThatIsStillRunningKeepsItsPrerequisiteState(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Options
		rec  InstanceRecord
	}{
		{
			// The uninstall reaches a real cluster and fails under test, which is the
			// point: the cluster is there, so nothing about it may be cleared.
			name: "managed, cluster present",
			opts: Options{Instance: "inst", AssumeYes: true},
			rec: InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c",
				Managed: true, ClusterUID: destroyedClusterUID},
		},
		{
			name: "adopted, cluster present",
			opts: Options{Instance: "inst", AssumeYes: true},
			rec: InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c",
				Managed: false, ClusterUID: destroyedClusterUID},
		},
		{
			name: "dry run",
			opts: Options{Instance: "inst", AssumeYes: true, DryRun: true},
			rec: InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c",
				Managed: true, ClusterUID: destroyedClusterUID},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := fakeHome(t)
			writeRecord(t, tc.rec)
			dir := plantClusterState(t, home, destroyedClusterUID)
			p := &fakeProvider{name: "local", present: map[string]bool{"c": true}}

			captureOutput(t, func() {
				_ = Destroy(context.Background(), p, DestroyOptions{Options: tc.opts})
			})

			if _, err := os.Stat(filepath.Join(dir, "infra", "cluster", "terraform.tfstate")); err != nil {
				t.Errorf("the prerequisite state of a cluster still running was removed: %v", err)
			}
		})
	}
}

// 🔴 NO UID, NOTHING TO NAME. An instance recorded before the identity existed must still
// destroy cleanly, and must not reach for the clusters directory with an empty key — which
// joins to the clusters directory ITSELF.
func TestAnInstanceWithNoRecordedIdentityLeavesEveryClusterStateAlone(t *testing.T) {
	home := fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "inst", Provider: "local", Cluster: "c", KubeContext: "kind-c", Managed: true})
	kept := plantClusterState(t, home, bystanderClusterUID)
	// Gone, so the destroy reaches removeGoneClusterState — with an empty UID.
	p := &fakeProvider{name: "local", present: map[string]bool{}}

	captureOutput(t, func() {
		if err := Destroy(context.Background(), p, DestroyOptions{Options: Options{Instance: "inst", AssumeYes: true}}); err != nil {
			t.Errorf("Destroy: %v", err)
		}
	})

	if _, err := os.Stat(filepath.Join(kept, "infra", "cluster", "terraform.tfstate")); err != nil {
		t.Errorf("a destroy with no cluster identity removed cluster state it could not have named: %v", err)
	}
}
