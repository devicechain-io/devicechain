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
		{"discovery is overridable", `count = var.storage_class == "" ? 1 : 0`},
		{"GKE CSI detection", `sc.provisioner == "pd.csi.storage.gke.io"`},
		{"GKE migrated in-tree detection", `sc.provisioner == "kubernetes.io/gce-pd"`},
		{"other providers do not get a class", `count = local.gke_standard_backup ? 1 : 0`},
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
	root, err := fs.ReadFile(OpenTofuCluster(), "main.tf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(strings.Fields(string(root)), " "), "storage_class = var.backup_object_store_storage_class") {
		t.Fatal("cluster root must pass the operator's backup class override to the module")
	}
}
