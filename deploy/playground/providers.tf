# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# Google credentials come from the environment (`gcloud auth application-default
# login`, or GOOGLE_APPLICATION_CREDENTIALS). With create_cluster = false nothing here
# touches Google, so the provider needs no project and no credentials.
provider "google" {
  project = var.create_cluster ? var.project_id : null
}

# The in-cluster half targets whatever cluster the kubeconfig names, the way the
# deploy/opentofu roots do. It is NOT wired to the cluster this root creates: a provider
# configured from a resource created in the same apply cannot be planned on a first run.
# Create the cluster, install DeviceChain, then apply again with deploy_driver = true
# (see README.md).
provider "kubernetes" {
  config_path    = var.kubeconfig_path
  config_context = var.kubeconfig_context != "" ? var.kubeconfig_context : null
}
