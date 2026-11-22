# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# SYNTHETIC. The same resources as ../../../flat, behind a module call.
terraform {
  required_providers {
    google = {
      source = "hashicorp/google"
    }
  }
}

variable "cluster_name" { type = string }
variable "location" { type = string }
variable "labels" { type = map(string) }
variable "pools" {
  type = map(object({
    machine_type = string
    node_count   = number
    disk_size_gb = number
  }))
}

resource "google_container_cluster" "this" {
  name                     = var.cluster_name
  location                 = var.location
  remove_default_node_pool = true
  initial_node_count       = 1
  deletion_protection      = false
  resource_labels          = var.labels
}

resource "google_container_node_pool" "this" {
  for_each = { for name, pool in var.pools : name => pool if pool.node_count > 0 }

  name       = each.key
  cluster    = google_container_cluster.this.id
  location   = var.location
  node_count = each.value.node_count

  node_config {
    machine_type = each.value.machine_type
    disk_size_gb = each.value.disk_size_gb
    labels       = var.labels
  }
}

resource "terraform_data" "cluster_settled" {
  count = 1

  triggers_replace = {
    cluster       = google_container_cluster.this.id
    pools         = join(",", sort([for name, pool in google_container_node_pool.this : "${name}:${pool.id}:${pool.node_count}"]))
    labels        = jsonencode(var.labels)
    pool_settings = jsonencode({ for name, pool in var.pools : name => pool if pool.node_count > 0 })
  }
}
