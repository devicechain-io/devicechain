# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# A GKE cluster to install DeviceChain into. It creates the cluster and nothing
# inside it: DeviceChain itself goes on with `dcctl install --kube-context`, as it
# would on any cluster, so this root and the deploy/opentofu roots never share state.
#
# The cluster and its pools are defined once, in modules/gke-cluster, which the fleet roots use
# too. This root is that module plus every default and the outputs a person wants.

module "gke_cluster" {
  source = "../opentofu/modules/gke-cluster"

  project_id                         = var.project_id
  location                           = var.location
  cluster_name                       = var.cluster_name
  release_channel                    = var.release_channel
  database_machine_type              = var.database_machine_type
  database_node_count                = var.database_node_count
  database_disk_type                 = var.database_disk_type
  database_disk_size_gb              = var.database_disk_size_gb
  services_machine_type              = var.services_machine_type
  services_node_count                = var.services_node_count
  services_disk_type                 = var.services_disk_type
  services_disk_size_gb              = var.services_disk_size_gb
  loadgen_node_count                 = var.loadgen_node_count
  loadgen_machine_type               = var.loadgen_machine_type
  loadgen_disk_type                  = var.loadgen_disk_type
  loadgen_disk_size_gb               = var.loadgen_disk_size_gb
  wait_for_cluster_operations        = var.wait_for_cluster_operations
  cluster_operations_timeout_seconds = var.cluster_operations_timeout_seconds
  labels                             = var.labels
}

# The addresses the cluster, its pools and the settle resource had before they moved into the
# module. A state applied by an earlier checkout moves with these on its next apply (a plan shows
# every one as a move, nothing changed).
#
# THE CHAIN READS OLD ADDRESS, THEN ROOT ADDRESS, THEN MODULE ADDRESS, so the pre-for_each loadgen
# block stays HERE, ahead of the moves into the module.
#
# An OLD checkout applied to a state that has already moved plans the opposite: every resource looks
# like an orphan, so it would delete the cluster and create a new one. See the README.

# The load-generator pool was its own resource before the pools shared one
# definition. Same name, so a cluster that has one keeps it rather than replacing it.
moved {
  from = google_container_node_pool.loadgen[0]
  to   = google_container_node_pool.this["loadgen"]
}

moved {
  from = google_container_cluster.this
  to   = module.gke_cluster.google_container_cluster.this
}

moved {
  from = google_container_node_pool.this
  to   = module.gke_cluster.google_container_node_pool.this
}

moved {
  from = terraform_data.cluster_settled
  to   = module.gke_cluster.terraform_data.cluster_settled
}
