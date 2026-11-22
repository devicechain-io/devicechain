# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# SYNTHETIC. A miniature of deploy/gke's flat shape, with the same google resource types, so
# that hack/check-gke-extraction-plans.sh can be shown to fail before the real root is touched.
terraform {
  required_version = ">= 1.10"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.4.0"
    }
  }
}

variable "project_id" { type = string }
variable "cluster_name" { type = string }
variable "location" { type = string }
variable "labels" {
  type    = map(string)
  default = { app = "devicechain" }
}
variable "database_node_count" {
  type    = number
  default = 3
}
variable "services_node_count" {
  type    = number
  default = 3
}
variable "loadgen_node_count" {
  type    = number
  default = 0
}

provider "google" {
  project = var.project_id
}

resource "google_container_cluster" "this" {
  name                     = var.cluster_name
  location                 = var.location
  remove_default_node_pool = true
  initial_node_count       = 1
  deletion_protection      = false
  resource_labels          = var.labels
}

locals {
  pools = {
    database = { machine_type = "n2-standard-4", node_count = var.database_node_count, disk_size_gb = 100 }
    services = { machine_type = "t2d-standard-4", node_count = var.services_node_count, disk_size_gb = 100 }
    loadgen  = { machine_type = "n2-standard-4", node_count = var.loadgen_node_count, disk_size_gb = 100 }
  }
}

resource "google_container_node_pool" "this" {
  for_each = { for name, pool in local.pools : name => pool if pool.node_count > 0 }

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
    pool_settings = jsonencode({ for name, pool in local.pools : name => pool if pool.node_count > 0 })
  }
}

moved {
  from = google_container_node_pool.loadgen[0]
  to   = google_container_node_pool.this["loadgen"]
}
