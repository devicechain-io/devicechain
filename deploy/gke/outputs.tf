# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The outputs depend on the whole module, whose last resource is the wait for the cluster's
# operations, so nothing that reads them is handed a cluster that is still moving.

output "get_credentials_command" {
  description = "Adds the cluster to your kubeconfig."
  depends_on  = [module.gke_cluster]
  value = format(
    "gcloud container clusters get-credentials %s --location %s --project %s",
    module.gke_cluster.cluster_name, var.location, var.project_id,
  )
}

output "kube_context" {
  description = "The context get-credentials creates, which is what `dcctl install --kube-context` and `dcctl bootstrap --kube-context` take."
  value       = format("gke_%s_%s_%s", var.project_id, var.location, module.gke_cluster.cluster_name)
  depends_on  = [module.gke_cluster]
}

output "database_node_selector" {
  description = "The node label that selects the database pool, as key=value. GKE puts it on every node of a pool."
  value       = module.gke_cluster.database_node_selector
}

output "database_taint" {
  description = "The taint on the database pool, as key=value:Effect: a pod must tolerate it to run there."
  value       = module.gke_cluster.database_taint
}
