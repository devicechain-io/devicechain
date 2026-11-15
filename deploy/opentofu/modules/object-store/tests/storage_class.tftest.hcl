# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

mock_provider "kubernetes" {
  mock_data "kubernetes_resources" {
    defaults = {
      objects = [{ provisioner = "pd.csi.storage.gke.io" }]
    }
  }
}

variables {
  namespace = "dc-system"
  buckets   = ["dc-rdb", "dc-tsdb"]
  storage   = "160Gi"
}

run "gke_uses_hdd_not_ssd_quota" {
  command = plan
  assert {
    condition     = kubernetes_persistent_volume_claim_v1.data.spec[0].storage_class_name == "dc-object-store-standard"
    error_message = "new GKE backup PVC must use the dedicated standard disk class"
  }
  assert {
    condition     = kubernetes_storage_class_v1.backup_standard[0].parameters.type == "pd-standard" && kubernetes_storage_class_v1.backup_standard[0].storage_provisioner == "pd.csi.storage.gke.io"
    error_message = "standard-rwo/pd-balanced still consumes SSD quota; backups need pd-standard"
  }
  assert {
    condition     = kubernetes_storage_class_v1.backup_standard[0].reclaim_policy == "Retain" && kubernetes_storage_class_v1.backup_standard[0].volume_binding_mode == "WaitForFirstConsumer"
    error_message = "backup disks must be retained and bound with their consumer"
  }
}

run "other_providers_keep_the_cluster_default" {
  command = plan
  override_data {
    target = data.kubernetes_resources.storage_classes
    values = { objects = [{ provisioner = "ebs.csi.aws.com" }] }
  }
  assert {
    condition     = length(kubernetes_storage_class_v1.backup_standard) == 0 && !local.gke_standard_backup
    error_message = "other providers must retain the cluster default"
  }
}

run "explicit_override_wins" {
  command = plan
  variables {
    storage_class = "custom-retained"
  }
  assert {
    condition     = length(data.kubernetes_resources.storage_classes) == 0 && length(kubernetes_storage_class_v1.backup_standard) == 0 && kubernetes_persistent_volume_claim_v1.data.spec[0].storage_class_name == "custom-retained"
    error_message = "an explicit class must bypass detection and reach a new PVC"
  }
}

# The upgrade this change must survive: a claim created before detection existed
# (no class in the configuration, the cluster default chosen by the API server)
# is re-planned on GKE. The class is immutable, so a planned change would replace
# the claim; it must stay where it is.
run "pre_detection_claim_is_created" {
  override_data {
    target = data.kubernetes_resources.storage_classes
    values = { objects = [] }
  }
}

run "upgrade_on_gke_leaves_the_existing_claim_class" {
  plan_options {
    refresh = false
  }
  assert {
    condition     = kubernetes_persistent_volume_claim_v1.data.spec[0].storage_class_name != "dc-object-store-standard"
    error_message = "an upgrade on GKE moved an existing claim onto the new class; the claim would be replaced"
  }
}
