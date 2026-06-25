# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# Both providers target an existing cluster via the supplied kubeconfig. A
# cloud-specific wrapper would instead pass cluster credentials from its
# cluster-provisioning resources (e.g. an EKS/GKE module's outputs).

locals {
  kube_context = var.kubeconfig_context != "" ? var.kubeconfig_context : null
}

provider "kubernetes" {
  config_path    = var.kubeconfig_path
  config_context = local.kube_context
}

provider "helm" {
  kubernetes {
    config_path    = var.kubeconfig_path
    config_context = local.kube_context
  }
}
