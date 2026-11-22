# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# Identity and placement only. The commands a person runs to reach the cluster (get-credentials, the
# kube context) are built by the root that wants them: a root run by a manager exports none.

output "cluster_name" {
  description = "The cluster name."
  value       = google_container_cluster.this.name
  depends_on  = [terraform_data.cluster_settled]
}

output "cluster_id" {
  description = "The cluster resource path (projects/PROJECT/locations/LOCATION/clusters/NAME)."
  value       = google_container_cluster.this.id
  depends_on  = [terraform_data.cluster_settled]
}

output "pool_names" {
  description = "The node pools that exist, by pool key."
  value       = { for key, pool in google_container_node_pool.this : key => pool.name }
  depends_on  = [terraform_data.cluster_settled]
}

output "database_node_selector" {
  description = "The node label that selects the database pool, as key=value. GKE puts it on every node of a pool."
  value       = format("cloud.google.com/gke-nodepool=%s", google_container_node_pool.this["database"].name)
}

output "database_taint" {
  description = "The taint on the database pool, as key=value:Effect: a pod must tolerate it to run there."
  value = join(",", [
    for t in local.pools.database.taints :
    format("%s=%s:%s", t.key, t.value, local.kubernetes_taint_effect[t.effect])
  ])
}
