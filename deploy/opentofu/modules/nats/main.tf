# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# NATS is DeviceChain's single messaging runtime dependency (ADR-003/006/007): it
# carries core messaging, the JetStream KV cache + distributed lock, and the MQTT
# device ingress. Installed from the official chart; JetStream and MQTT are
# enabled, cluster size is driven by the HA toggle (single-node for the launch
# slice, 3-node RAFT under ADR-020).

variable "namespace" {
  type = string
}

variable "release_name" {
  description = "Helm release name; also the Service name services connect to."
  type        = string
  default     = "dc-nats"
}

variable "chart_version" {
  description = "nats chart version; empty installs latest."
  type        = string
  default     = ""
}

variable "jetstream_storage" {
  type    = string
  default = "8Gi"
}

variable "ha" {
  type    = bool
  default = false
}

resource "helm_release" "nats" {
  name       = var.release_name
  namespace  = var.namespace
  repository = "https://nats-io.github.io/k8s/helm/charts/"
  chart      = "nats"
  version    = var.chart_version != "" ? var.chart_version : null

  # https://github.com/nats-io/k8s — config.* maps onto nats-server config.
  values = [yamlencode({
    config = {
      jetstream = {
        enabled = true
        fileStore = {
          pvc = {
            enabled = true
            size    = var.jetstream_storage
          }
        }
      }
      mqtt = {
        enabled = true
      }
      cluster = {
        enabled  = var.ha
        replicas = var.ha ? 3 : 1
      }
    }
  })]
}

output "client_url" {
  description = "NATS client URL."
  value       = "nats://${var.release_name}.${var.namespace}:4222"
}

output "mqtt_url" {
  description = "NATS MQTT ingress URL."
  value       = "tcp://${var.release_name}.${var.namespace}:1883"
}

output "service" {
  description = "In-cluster NATS service host."
  value       = "${var.release_name}.${var.namespace}"
}
