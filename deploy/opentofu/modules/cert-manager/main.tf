# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# cert-manager (ADR-002): issues and renews the TLS certificates that secure the
# ingress. OpenTofu installs cert-manager and its CRDs; the cert Issuer (and the
# Certificate, via ingress-shim) is rendered by the Helm chart once these CRDs
# exist — which also avoids the Terraform "create a custom resource before its
# CRD exists" plan-time problem.

variable "namespace" {
  type    = string
  default = "cert-manager"
}

variable "release_name" {
  type    = string
  default = "cert-manager"
}

variable "chart_version" {
  description = "cert-manager chart version; empty installs latest. Pin for reproducibility."
  type        = string
  default     = ""
}

resource "helm_release" "cert_manager" {
  name             = var.release_name
  namespace        = var.namespace
  create_namespace = true
  repository       = "https://charts.jetstack.io"
  chart            = "cert-manager"
  version          = var.chart_version != "" ? var.chart_version : null

  # Install the cert-manager CRDs with the chart so the Issuer/Certificate kinds
  # the Helm chart renders are available.
  set {
    name  = "installCRDs"
    value = "true"
  }
}

output "namespace" {
  value = var.namespace
}
