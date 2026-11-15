// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package assets

import (
	"io/fs"
	"strings"
	"testing"
)

// These checks cover the embedded configuration dcctl actually ships. The
// mocked-provider suite beside the module additionally checks plan behavior.
func TestBackupStoragePolicyShips(t *testing.T) {
	src := objectStoreModule(t)
	compact := strings.Join(strings.Fields(src), " ")
	for _, tc := range []struct {
		name string
		want string
	}{
		{"decided from inputs known at plan time", `gke_standard_backup = var.storage_class == "" && var.standard_disk_class`},
		{"only the GKE input creates a class", `count = local.gke_standard_backup ? 1 : 0`},
		{"HDD quota", `parameters = { type = "pd-standard" }`},
		{"CSI provisioner", `storage_provisioner = "pd.csi.storage.gke.io"`},
		{"new PVC selection and override", `storage_class_name = var.storage_class != "" ? var.storage_class : (local.gke_standard_backup ? kubernetes_storage_class_v1.backup_standard[0].metadata[0].name : null)`},
		{"existing PVC size and class are immutable", `ignore_changes = [spec[0].resources[0].requests, spec[0].storage_class_name]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(compact, tc.want) {
				t.Fatalf("embedded backup storage policy missing %s: %s", tc.name, tc.want)
			}
		})
	}
	// 🔴 NO CLUSTER READ MAY DECIDE THE CLASS. A data source listing the cluster's
	// StorageClasses is unknown when a fresh cluster is planned, and a count that depends
	// on it fails the plan of every new install.
	if strings.Contains(compact, `data "kubernetes_resources"`) {
		t.Fatal("the object-store module must not read the cluster to choose its class; the caller passes standard_disk_class")
	}
	root, err := fs.ReadFile(OpenTofuCluster(), "main.tf")
	if err != nil {
		t.Fatal(err)
	}
	rootCompact := strings.Join(strings.Fields(string(root)), " ")
	for _, want := range []string{
		"storage_class = var.backup_object_store_storage_class",
		"standard_disk_class = var.backup_object_store_gke_standard_disk",
	} {
		if !strings.Contains(rootCompact, want) {
			t.Fatalf("cluster root must pass %q to the object-store module", want)
		}
	}
}
