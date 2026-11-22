# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# Identity only. This root exports no credential material and no kube context: those stay on the
# human root (deploy/gke), and the fleet manager's apply identity has no Kubernetes access at all.

output "cluster_name" {
  description = "The cluster's name."
  value       = module.gke_cluster.cluster_name
}

output "cluster_id" {
  description = "The cluster's resource path."
  value       = module.gke_cluster.cluster_id
}

output "location" {
  description = "Where the cluster runs."
  value       = var.location
}

output "pool_names" {
  description = "The node pools that exist."
  value       = module.gke_cluster.pool_names
}
