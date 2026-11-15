// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// backupStandardDiskVar asks the cluster root for a dedicated pd-standard StorageClass for
// the in-cluster backup store's NEW volume, so bulk backups draw on GKE's HDD quota rather
// than the SSD quota the databases and broker share.
const backupStandardDiskVar = "backup_object_store_gke_standard_disk"

// gkePersistentDiskProvisioner is the CSI driver the dedicated class names, so it is also
// what has to be present for the class to provision anything.
const gkePersistentDiskProvisioner = "pd.csi.storage.gke.io"

// settleBackupStandardDisk records on st whether the cluster root should create and use the
// pd-standard backup class.
//
// 🔴 DECIDED HERE, BEFORE THE PLAN, AND NOT BY THE MODULE. The module once listed the
// cluster's StorageClasses itself and counted the class from the result; on a fresh cluster
// that listing is unknown at plan time, so the count was too and the plan of every new
// install failed. dcctl can ask the cluster first and hand the root a plain boolean.
//
// Set on every install of a GKE cluster, re-installs included: the module ignores later
// class changes on an existing claim, so an existing volume keeps its class and only the
// (unused until a new volume) StorageClass is added.
func settleBackupStandardDisk(ctx context.Context, st *State, typed kubernetes.Interface) error {
	st.BackupStandardDisk = false
	if typed == nil {
		// A rehearsal has no cluster to ask; the root's default (the cluster default class) stands.
		return nil
	}
	// An operator's own TF_VAR_ answer wins, false included: tofu reads it under dcctl's
	// -var, so emitting one here would silently override it. (The cluster root's directory
	// is extracted afresh by each install, so the environment is where such a value lives.)
	if statedByOperator("", backupStandardDiskVar) {
		return nil
	}
	classes, err := typed.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing StorageClasses to choose the backup store's disk type: %w", err)
	}
	for _, sc := range classes.Items {
		if sc.Provisioner == gkePersistentDiskProvisioner {
			st.BackupStandardDisk = true
			return nil
		}
	}
	return nil
}
