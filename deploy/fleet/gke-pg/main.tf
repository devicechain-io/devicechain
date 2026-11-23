# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# A fleet cluster, created through the same module deploy/gke uses. This root contains NO in-cluster
# provider and creates nothing inside the cluster: installing DeviceChain into it is a different act,
# by a different authority, from creating it.

module "gke_cluster" {
  source = "../../opentofu/modules/gke-cluster"

  project_id   = var.project_id
  location     = var.location
  cluster_name = var.cluster_name

  # Trust-affecting attributes are LITERALS, not variables. The nodes run as a dedicated
  # least-privilege identity, and workload identity is on.
  node_service_account = format("dcf-node@%s.iam.gserviceaccount.com", var.project_id)
  workload_pool        = format("%s.svc.id.goog", var.project_id)
  release_channel      = "REGULAR"
  labels               = { app = "devicechain-fleet" }

  # What recovery finds the cluster by. The nonce and the uid are LABELS on the cluster, not on the
  # pools. The data-class label is informational: nothing reads a label to authorize anything.
  ownership_labels = {
    "dc-fleet-cluster"    = var.cluster_uid
    "dc-fleet-owner"      = var.ownership_nonce
    "dc-fleet-data-class" = var.data_class
  }

  spot       = var.spot
  network    = var.network
  subnetwork = var.subnetwork

  database_machine_type = var.database_machine_type
  database_node_count   = var.database_node_count
  database_disk_type    = "pd-standard"
  database_disk_size_gb = 100
  services_machine_type = var.services_machine_type
  services_node_count   = var.services_node_count
  services_disk_type    = "pd-standard"
  services_disk_size_gb = 100
  loadgen_node_count    = 0
  loadgen_machine_type  = "n2-standard-4"
  loadgen_disk_type     = "pd-standard"
  loadgen_disk_size_gb  = 100

  # The runner has no gcloud: the manager's Observer watches the cluster's operations instead.
  wait_for_cluster_operations        = false
  cluster_operations_timeout_seconds = 1800
}
