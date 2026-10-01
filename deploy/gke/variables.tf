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

variable "database_machine_type" {
  description = "Machine type for the database pool. Choose memory: Postgres keeps a small buffer cache of its own and relies on the node's page cache for the rest. See the README."
  type        = string
  default     = "n2-standard-4"
}

variable "database_node_count" {
  description = "Nodes in the database pool. The pool is tainted, so only a pod placed there runs there; once the databases are placed on it, --ha puts one instance of each database on a different node, which needs 3."
  type        = number
  default     = 3

  validation {
    condition     = var.database_node_count >= 1 && floor(var.database_node_count) == var.database_node_count
    error_message = "database_node_count must be a whole number of at least 1."
  }
}

variable "database_disk_type" {
  description = "Boot disk type for the database pool's nodes. The databases do NOT use it — they write to their own persistent volumes, whose class is chosen by the cluster's default StorageClass (see the README). pd-balanced and pd-ssd both count against the region's SSD_TOTAL_GB quota, which those volumes also draw on; pd-standard counts against a different quota but is slower, and is unmeasured for DeviceChain. Changing it recreates this pool's nodes. The README's quota step gives the budget."
  type        = string
  default     = "pd-balanced"
}

variable "database_disk_size_gb" {
  description = "Boot disk size for the database pool's nodes, in GB."
  type        = number
  default     = 50
}

variable "services_machine_type" {
  description = "Machine type for the services pool, where everything but the databases runs. Choose CPU: the services use little memory. See the README."
  type        = string
  default     = "n2-highcpu-4"
}

variable "services_node_count" {
  description = "Nodes in the services pool. `dcctl install --ha` needs at least 3: NATS places one server per node, and only untainted nodes count."
  type        = number
  default     = 3

  validation {
    condition     = var.services_node_count >= 1 && floor(var.services_node_count) == var.services_node_count
    error_message = "services_node_count must be a whole number of at least 1."
  }
}

variable "services_disk_type" {
  description = "Boot disk type for the services pool's nodes. NATS does NOT use it — it writes to its own persistent volumes, whose class is chosen by the cluster's default StorageClass (see the README). pd-balanced and pd-ssd both count against the region's SSD_TOTAL_GB quota, which those volumes also draw on; pd-standard counts against a different quota but is slower, and is unmeasured for DeviceChain. Changing it recreates this pool's nodes. The README's quota step gives the budget."
  type        = string
  default     = "pd-balanced"
}

variable "services_disk_size_gb" {
  description = "Boot disk size for the services pool's nodes, in GB."
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

variable "loadgen_disk_type" {
  description = "Boot disk type for the load-generator pool's nodes. pd-balanced and pd-ssd count against the region's SSD_TOTAL_GB quota; pd-standard counts against a different one. Changing it recreates this pool's nodes."
  type        = string
  default     = "pd-balanced"
}

variable "loadgen_disk_size_gb" {
  description = "Boot disk size for the load-generator pool's nodes, in GB."
  type        = number
  default     = 50
}

variable "labels" {
  description = "Labels applied to the cluster and its nodes, for finding (and billing) what this created."
  type        = map(string)
  default     = { app = "devicechain" }
}
