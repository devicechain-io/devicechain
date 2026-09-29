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
