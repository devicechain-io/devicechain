# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The NGINX ingress controller (ADR-002): the L7 entry point that fronts the
# DeviceChain GraphQL/HTTP surface. OpenTofu installs the controller (the
# capability); the per-instance Ingress *resource* that routes to the app
# Services is rendered by the Helm chart (deploy/helm/devicechain), which knows
# the enabled functional areas.

variable "namespace" {
  type    = string
  default = "ingress-nginx"
}

variable "release_name" {
  type    = string
  default = "ingress-nginx"
}

variable "chart_version" {
  description = "ingress-nginx chart version; empty installs latest. Pin for reproducibility."
  type        = string
  default     = ""
}

variable "ingress_class" {
  description = "IngressClass name the controller registers (referenced by the Helm chart's Ingress)."
  type        = string
  default     = "nginx"
}

variable "use_host_port" {
  description = <<-EOT
    Bind the controller to the node's host 80/443 (hostPort) and expose it via a
    NodePort Service instead of a LoadBalancer. This is the local-kind recipe: a
    LoadBalancer Service stays <pending> on kind unless cloud-provider-kind owns
    the host ports — which collides with kind's own 80/443 extraPortMappings — so
    the helm release never goes ready and the apply times out. With host ports the
    controller binds the ingress-ready node directly and is reachable at
    localhost:80/443 with no LoadBalancer. Leave false for real clouds (GKE/EKS/AKS),
    where a LoadBalancer Service is correct.
  EOT
  type        = bool
  default     = false
}

locals {
  ingress_controller_base = {
    ingressClassResource = {
      name    = var.ingress_class
      enabled = true
    }
    ingressClass = var.ingress_class
  }
  # kind recipe: bind node 80/443 directly, no LoadBalancer to wait on.
  ingress_controller_host_port = {
    hostPort     = { enabled = true }
    service      = { type = "NodePort" }
    nodeSelector = { "ingress-ready" = "true" }
    tolerations = [{
      key      = "node-role.kubernetes.io/control-plane"
      operator = "Equal"
      effect   = "NoSchedule"
    }]
  }
}

resource "helm_release" "ingress_nginx" {
  name             = var.release_name
  namespace        = var.namespace
  create_namespace = true
  repository       = "https://kubernetes.github.io/ingress-nginx"
  chart            = "ingress-nginx"
  version          = var.chart_version != "" ? var.chart_version : null

  # yamlencode is inside the conditional so both branches are strings (an HCL
  # ternary requires both arms to share a type; the merged host-port object and
  # the base object do not).
  values = [
    var.use_host_port
    ? yamlencode({ controller = merge(local.ingress_controller_base, local.ingress_controller_host_port) })
    : yamlencode({ controller = local.ingress_controller_base })
  ]
}

output "ingress_class" {
  description = "IngressClass name to set on Ingress resources."
  value       = var.ingress_class
}

output "namespace" {
  value = var.namespace
}
