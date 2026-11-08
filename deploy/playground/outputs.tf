# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

output "get_credentials_command" {
  description = "Adds the cluster to your kubeconfig. Empty unless create_cluster is true."
  value = var.create_cluster ? format(
    "gcloud container clusters get-credentials %s --location %s --project %s",
    var.cluster_name, var.location, var.project_id,
  ) : ""
  depends_on = [google_container_node_pool.this]
}

output "kube_context" {
  description = "The context get-credentials creates, which dcctl install --kube-context and this root's kubeconfig_context take. Empty unless create_cluster is true."
  value       = var.create_cluster ? format("gke_%s_%s_%s", var.project_id, var.location, var.cluster_name) : ""
  depends_on  = [google_container_node_pool.this]
}

output "reset_schedule" {
  description = "The cron expression of the nightly reset, in UTC. Empty unless deploy_driver is true."
  value       = var.deploy_driver ? local.reset_schedule : ""
}

output "driver_namespace" {
  description = "The namespace holding the driver. Empty unless deploy_driver is true."
  value       = var.deploy_driver ? var.namespace : ""
}
