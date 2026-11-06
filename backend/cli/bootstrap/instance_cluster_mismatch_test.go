// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	mismatchOldUID = "163e7f17-d87c-42fe-8bc0-e672e35f5ee7"
	mismatchNewUID = "446b60a1-b6f8-4cf0-9e14-ced15bc26170"
)

func TestRefuseInstanceOnAnotherClusterNamesBothClustersAndTheRemedy(t *testing.T) {
	fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "rc", Provider: "gke", Cluster: "dc-ha", KubeContext: "gke_dc-ha", ClusterUID: mismatchOldUID})
	// A credential file next to the record: the check must neither print nor remove it.
	dir, err := instanceRoot("rc")
	if err != nil {
		t.Fatal(err)
	}
	secret := "SUPERSECRETBROKERPASSWORD"
	if err := os.WriteFile(dir+string(os.PathSeparator)+"creds.txt", []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}

	err = RefuseInstanceOnAnotherCluster("rc", ClusterBinding{Cluster: "dc-single", KubeContext: "gke_dc-single"}, mismatchNewUID)
	if err == nil {
		t.Fatal("a record for another cluster must be refused")
	}
	msg := err.Error()
	for _, want := range []string{mismatchOldUID, mismatchNewUID, "dc-ha", "dc-single", dir, filepath.Join(filepath.Dir(filepath.Dir(dir)), "rc.stale-state"), "outside dcctl"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, secret) {
		t.Error("credential contents leaked into the message")
	}
	if b, rerr := os.ReadFile(dir + string(os.PathSeparator) + "creds.txt"); rerr != nil || string(b) != secret {
		t.Errorf("the check must not touch the state: %v", rerr)
	}
}

func TestRefuseInstanceOnAnotherClusterProceedsWhenTheClusterMatches(t *testing.T) {
	fakeHome(t)
	writeRecord(t, InstanceRecord{Instance: "rc", Provider: "gke", KubeContext: "ctx", ClusterUID: mismatchOldUID})
	if err := RefuseInstanceOnAnotherCluster("rc", ClusterBinding{KubeContext: "ctx"}, mismatchOldUID); err != nil {
		t.Fatalf("same cluster must proceed: %v", err)
	}
}

func TestRefuseInstanceOnAnotherClusterKeepsOldBehaviourWithoutAUID(t *testing.T) {
	fakeHome(t)
	// A record from before clusterUid was recorded.
	writeRecord(t, InstanceRecord{Instance: "rc", Provider: "local", KubeContext: "ctx"})
	if err := RefuseInstanceOnAnotherCluster("rc", ClusterBinding{KubeContext: "ctx"}, mismatchNewUID); err != nil {
		t.Fatalf("an old record has nothing to compare: %v", err)
	}
	// No record at all, and an unidentified target.
	if err := RefuseInstanceOnAnotherCluster("fresh", ClusterBinding{KubeContext: "ctx"}, mismatchNewUID); err != nil {
		t.Fatalf("no record: %v", err)
	}
	writeRecord(t, InstanceRecord{Instance: "rc2", Provider: "local", KubeContext: "ctx", ClusterUID: mismatchOldUID})
	if err := RefuseInstanceOnAnotherCluster("rc2", ClusterBinding{KubeContext: "ctx"}, ""); err != nil {
		t.Fatalf("unknown target UID: %v", err)
	}
}
