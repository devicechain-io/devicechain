# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# SYNTHETIC. The thin root over the module.
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

locals {
  pools = {
    database = { machine_type = "n2-standard-4", node_count = var.database_node_count, disk_size_gb = 100 }
    services = { machine_type = "t2d-standard-4", node_count = var.services_node_count, disk_size_gb = 100 }
    loadgen  = { machine_type = "n2-standard-4", node_count = var.loadgen_node_count, disk_size_gb = 100 }
  }
}

module "gke_cluster" {
  source = "./modules/gke-cluster"

  cluster_name = var.cluster_name
  location     = var.location
  labels       = var.labels
  pools        = local.pools
}

# The pre-for_each loadgen pool address stays HERE, in the root, ahead of the moves into
# the module: old address, then root address, then module address.
moved {
  from = google_container_node_pool.loadgen[0]
  to   = google_container_node_pool.this["loadgen"]
}

moved {
  from = google_container_cluster.this
  to   = module.gke_cluster.google_container_cluster.this
}

moved {
  from = google_container_node_pool.this
  to   = module.gke_cluster.google_container_node_pool.this
}

moved {
  from = terraform_data.cluster_settled
  to   = module.gke_cluster.terraform_data.cluster_settled
}
