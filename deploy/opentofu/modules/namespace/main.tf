# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

variable "name" {
  description = "Namespace name."
  type        = string
}

variable "create" {
  description = "Whether to create the namespace (false if managed elsewhere)."
  type        = bool
  default     = true
}

resource "kubernetes_namespace_v1" "this" {
  count = var.create ? 1 : 0

  metadata {
    name = var.name
    labels = {
      "app.kubernetes.io/managed-by" = "opentofu"
      "devicechain.io/component"     = "infrastructure"
    }
  }
}

output "name" {
  description = "The namespace name (whether created here or pre-existing)."
  value       = var.name
}
