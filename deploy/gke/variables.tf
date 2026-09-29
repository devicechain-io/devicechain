# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

variable "project_id" {
  description = "The Google Cloud project the cluster is created in. Billing must be linked and the container and compute APIs enabled."
  type        = string
}

variable "location" {
  description = "A zone (us-east4-b) for a zonal cluster, or a region (us-east4) for a regional one. A regional cluster creates the node pools in EVERY zone of the region, so it runs three times the nodes this file asks for."
  type        = string
  default     = "us-east4-b"
}

variable "cluster_name" {
  description = "Name of the GKE cluster."
  type        = string
  default     = "devicechain"
}

variable "release_channel" {
  description = "GKE release channel: RAPID, REGULAR, STABLE or EXTENDED. DeviceChain needs Kubernetes 1.29 or newer, which every channel is past."
  type        = string
  default     = "REGULAR"

  validation {
    condition     = contains(["RAPID", "REGULAR", "STABLE", "EXTENDED"], var.release_channel)
    error_message = "release_channel must be one of RAPID, REGULAR, STABLE or EXTENDED."
  }
}

variable "node_machine_type" {
  description = "Machine type for the nodes DeviceChain runs on."
  type        = string
  default     = "n2-standard-8"
}

variable "node_count" {
  description = "Nodes in the DeviceChain pool. `dcctl install --ha` needs at least 3: the relational store, NATS and the event store each place one replica per node."
  type        = number
  default     = 3

  validation {
    condition     = var.node_count >= 1 && floor(var.node_count) == var.node_count
    error_message = "node_count must be a whole number of at least 1."
  }
}

variable "node_disk_type" {
  description = "Boot disk type for every node. The databases and NATS do NOT use it — they write to their own persistent volumes, whose class is chosen by the cluster's default StorageClass (see the README) — so the balanced disk is enough here and keeps the regional SSD quota for those volumes."
  type        = string
  default     = "pd-balanced"
}

variable "node_disk_size_gb" {
  description = "Boot disk size for every node, in GB."
  type        = number
  default     = 50
}

variable "loadgen_node_count" {
  description = "Nodes in a separate, tainted pool for a load generator, so a load test does not take CPU from the platform it measures. 0 creates no pool."
  type        = number
  default     = 0

  validation {
    condition     = var.loadgen_node_count >= 0 && floor(var.loadgen_node_count) == var.loadgen_node_count
    error_message = "loadgen_node_count must be a whole number, 0 or more."
  }
}

variable "loadgen_machine_type" {
  description = "Machine type for the load-generator pool."
  type        = string
  default     = "n2-standard-4"
}

variable "labels" {
  description = "Labels applied to the cluster and its nodes, for finding (and billing) what this created."
  type        = map(string)
  default     = { app = "devicechain" }
}
