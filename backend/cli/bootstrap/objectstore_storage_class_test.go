// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// The cluster root counts the backup store's pd-standard class from a boolean dcctl hands
// it, so the count is known when a fresh cluster is planned. These hold the boolean to the
// cluster dcctl is installing into, and hold it to the cluster root alone.
func TestBackupStandardDiskFollowsTheClustersProvisioner(t *testing.T) {
	want := backupStandardDiskVar + "=true"
	for _, tc := range []struct {
		name    string
		classes []runtime.Object
		want    bool
	}{
		{name: "GKE", classes: []runtime.Object{
			storageClass("standard", "kubernetes.io/gce-pd", false),
			storageClass("standard-rwo", "pd.csi.storage.gke.io", true),
		}, want: true},
		{name: "kind", classes: []runtime.Object{storageClass("standard", "rancher.io/local-path", true)}},
		{name: "other cloud", classes: []runtime.Object{storageClass("gp3", "ebs.csi.aws.com", true)}},
		{name: "GKE in-tree only, no CSI driver to provision the class",
			classes: []runtime.Object{storageClass("standard", "kubernetes.io/gce-pd", true)}},
		{name: "no classes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TF_VAR_"+backupStandardDiskVar, "")
			// A State reused from an earlier GKE run must not carry its answer here.
			st := &State{Instance: "test", BackupStandardDisk: true}
			if err := settleBackupStandardDisk(t.Context(), st, fake.NewSimpleClientset(tc.classes...)); err != nil {
				t.Fatal(err)
			}
			if st.BackupStandardDisk != tc.want {
				t.Fatalf("BackupStandardDisk = %v, want %v", st.BackupStandardDisk, tc.want)
			}
			cluster, instance, err := splitVars(infraVars(st))
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(cluster, want); got != tc.want {
				t.Fatalf("cluster root vars carry %q = %v, want %v: %v", want, got, tc.want, cluster)
			}
			if slices.ContainsFunc(instance, func(v string) bool { return strings.HasPrefix(v, backupStandardDiskVar+"=") }) {
				t.Fatalf("the instance root does not declare %s: %v", backupStandardDiskVar, instance)
			}
		})
	}
}

func TestBackupStandardDiskRefusesAnUnreadableCluster(t *testing.T) {
	typed := fake.NewSimpleClientset()
	typed.PrependReactor("list", "storageclasses", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	if err := settleBackupStandardDisk(t.Context(), &State{}, typed); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("err = %v, want the list failure", err)
	}
}

// An operator who states the variable keeps it, false included: dcctl's -var would win.
func TestBackupStandardDiskLeavesAnOperatorsAnswer(t *testing.T) {
	t.Setenv("TF_VAR_"+backupStandardDiskVar, "false")
	st := &State{}
	typed := fake.NewSimpleClientset(storageClass("standard-rwo", gkePersistentDiskProvisioner, true))
	if err := settleBackupStandardDisk(t.Context(), st, typed); err != nil || st.BackupStandardDisk {
		t.Fatalf("err = %v, BackupStandardDisk = %v; the operator's TF_VAR must stand", err, st.BackupStandardDisk)
	}
}

// The rehearsal path has no cluster: it decides nothing and the root default stands.
func TestBackupStandardDiskWithoutAClusterKeepsTheDefault(t *testing.T) {
	st := &State{BackupStandardDisk: true}
	if err := settleBackupStandardDisk(t.Context(), st, nil); err != nil || st.BackupStandardDisk {
		t.Fatalf("err = %v, BackupStandardDisk = %v", err, st.BackupStandardDisk)
	}
}

// 🔴 THE CALL SITE, not only the function: installClusterVars is the one producer of the
// cluster root's variables, and a detection that is never called there leaves every GKE
// install on the SSD-backed default class with every test above still green.
func TestInstallClusterVarsSettlesTheBackupDisk(t *testing.T) {
	src, err := os.ReadFile("controlplane.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func installClusterVars(")
	if start < 0 {
		t.Fatal("installClusterVars not found")
	}
	body = body[start:]
	settle := strings.Index(body, "settleBackupStandardDisk(ctx, st, typed)")
	split := strings.Index(body, "splitVars(infraVars(st))")
	if settle < 0 || split < 0 || settle > split {
		t.Fatal("installClusterVars must settle the backup disk before it computes the cluster root's variables")
	}
}
