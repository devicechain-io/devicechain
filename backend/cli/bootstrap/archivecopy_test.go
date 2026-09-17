// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// archivedInstallState is a bootstrap on a cluster installed with in-cluster backups.
func archivedInstallState() *State {
	rec := aCompleteInstall()
	rec.Phase = installPhaseInstalled
	st := aWritableState()
	st.Install = &rec
	return st
}

// clusterArchiveSecret writes the cluster's archive credential the way the install
// does, carrying whatever else is passed alongside the two contract keys.
func clusterArchiveSecret(t *testing.T, c *fake.Clientset, uid string, data, labels map[string]string) {
	t.Helper()
	spec := ownedSecret{Name: "dc-object-store-credentials", Namespace: infraNamespace, Type: corev1.SecretTypeOpaque,
		Labels: labels, Data: data, Scope: ownerCluster}
	if err := writeOwnedSecret(context.Background(), c, clusterOwner(uid), spec, time.Now); err != nil {
		t.Fatalf("writing the cluster's archive credential: %v", err)
	}
	settleStringDataLikeAnAPIServer(t, c)
}

// 🔴 EXACTLY THE CONTRACT, NOTHING ELSE OFF THE LIVE OBJECT. Whatever was added to the
// cluster's Secret — a key, a label a controller keys on — is not the instance's to carry
// into its own namespace.
func TestTheInstanceArchiveCopyCarriesOnlyTheContractKeys(t *testing.T) {
	st := archivedInstallState()
	c := fake.NewSimpleClientset()
	clusterArchiveSecret(t, c, testClusterUID,
		map[string]string{"MINIO_ROOT_USER": "os-user", "MINIO_ROOT_PASSWORD": "os-secret", "CONSOLE_TOKEN": "extra"},
		map[string]string{"cnpg.io/reload": "true", "app.kubernetes.io/component": "object-store"})

	got, err := readClusterArchiveCredential(context.Background(), c, st)
	if err != nil {
		t.Fatalf("the cluster's own archive credential was refused: %v", err)
	}
	if got == nil {
		t.Fatal("a cluster with backups gave the instance no archive credential")
	}
	wantData := map[string]string{"MINIO_ROOT_USER": "os-user", "MINIO_ROOT_PASSWORD": "os-secret"}
	if !maps.Equal(got.Data, wantData) {
		t.Errorf("the copy carries %v, want exactly the two contract keys %v", got.Data, wantData)
	}
	wantLabels := map[string]string{"app.kubernetes.io/component": "database-backup"}
	if !maps.Equal(got.Labels, wantLabels) {
		t.Errorf("the copy carries labels %v, want %v", got.Labels, wantLabels)
	}
	if got.Type != corev1.SecretTypeOpaque || got.Scope != ownerCluster || got.Namespace != infraNamespace ||
		got.Name != "dc-object-store-credentials" {
		t.Errorf("the copy is not the cluster's Opaque archive credential: %+v", got)
	}
}

func TestTheInstanceArchiveCopyRefusesWhatIsNotTheClustersCredential(t *testing.T) {
	contract := map[string]string{"MINIO_ROOT_USER": "os-user", "MINIO_ROOT_PASSWORD": "os-secret"}

	t.Run("gone", func(t *testing.T) {
		_, err := readClusterArchiveCredential(context.Background(), fake.NewSimpleClientset(), archivedInstallState())
		if err == nil || !strings.Contains(err.Error(), "dcctl install") {
			t.Errorf("a missing archive credential was not refused with the install as its remedy: %v", err)
		}
	})
	t.Run("another cluster's", func(t *testing.T) {
		c := fake.NewSimpleClientset()
		clusterArchiveSecret(t, c, "446b60a1-5c0e-4a8e-9d8f-2b4a3e6f7c10", contract, nil)
		if got, err := readClusterArchiveCredential(context.Background(), c, archivedInstallState()); err == nil {
			t.Errorf("another cluster's archive credential was copied: %+v", got)
		}
	})
	t.Run("no secret key", func(t *testing.T) {
		c := fake.NewSimpleClientset()
		clusterArchiveSecret(t, c, testClusterUID, map[string]string{"MINIO_ROOT_USER": "os-user"}, nil)
		_, err := readClusterArchiveCredential(context.Background(), c, archivedInstallState())
		if err == nil || !strings.Contains(err.Error(), "MINIO_ROOT_PASSWORD") {
			t.Errorf("a credential missing its secret key was copied: %v", err)
		}
	})
	t.Run("backups off", func(t *testing.T) {
		st := archivedInstallState()
		st.Install.Settings.DatabaseBackups = false
		// Nothing in the cluster at all: a read would have been refused as "gone".
		got, err := readClusterArchiveCredential(context.Background(), fake.NewSimpleClientset(), st)
		if got != nil || err != nil {
			t.Errorf("a cluster with no backups was asked for an archive credential: %+v, %v", got, err)
		}
	})
}
