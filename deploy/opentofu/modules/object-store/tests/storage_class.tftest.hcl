# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The backup volume's class is decided from INPUTS, never by listing the cluster's
# StorageClasses: on a fresh cluster such a listing is unknown at plan time, and a
# count that depends on it fails the plan of every new install.

mock_provider "kubernetes" {}

variables {
  namespace = "dc-system"
  buckets   = ["dc-rdb", "dc-tsdb"]
  storage   = "160Gi"
}

run "gke_uses_hdd_not_ssd_quota" {
  command = plan
  variables {
    standard_disk_class = true
  }
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
  assert {
    condition     = length(kubernetes_storage_class_v1.backup_standard) == 0 && !local.gke_standard_backup && kubernetes_persistent_volume_claim_v1.data.spec[0].storage_class_name != "dc-object-store-standard"
    error_message = "other providers must retain the cluster default"
  }
}

run "explicit_override_wins" {
  command = plan
  variables {
    standard_disk_class = true
    storage_class       = "custom-retained"
  }
  assert {
    condition     = length(kubernetes_storage_class_v1.backup_standard) == 0 && kubernetes_persistent_volume_claim_v1.data.spec[0].storage_class_name == "custom-retained"
    error_message = "an explicit class must win over the standard disk class and reach a new PVC"
  }
}

# The upgrade this change must survive: a claim created before the standard disk
# class existed (no class in the configuration, the cluster default chosen by the
# API server) is re-planned on GKE. The class is immutable, so a planned change
# would replace the claim; it must stay where it is.
run "pre_standard_class_claim_is_created" {
  variables {
    standard_disk_class = false
  }
}

run "upgrade_on_gke_leaves_the_existing_claim_class" {
  plan_options {
    refresh = false
  }
  variables {
    standard_disk_class = true
  }
  assert {
    condition     = kubernetes_persistent_volume_claim_v1.data.spec[0].storage_class_name != "dc-object-store-standard"
    error_message = "an upgrade on GKE moved an existing claim onto the new class; the claim would be replaced"
  }
  assert {
    condition     = length(kubernetes_storage_class_v1.backup_standard) == 1
    error_message = "the upgrade should still create the class for any volume made later"
  }
}
