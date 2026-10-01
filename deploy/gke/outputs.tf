# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

output "get_credentials_command" {
  description = "Adds the cluster to your kubeconfig."
  value = format(
    "gcloud container clusters get-credentials %s --location %s --project %s",
    google_container_cluster.this.name, var.location, var.project_id,
  )
}

output "kube_context" {
  description = "The context get-credentials creates, which is what `dcctl install --kube-context` and `dcctl bootstrap --kube-context` take."
  value       = format("gke_%s_%s_%s", var.project_id, var.location, google_container_cluster.this.name)
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
