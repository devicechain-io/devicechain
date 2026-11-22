# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# A CLOSED input surface. The only things a caller chooses are the cluster's identity, where it
# runs, whether it is spot, and how big its two pools are. Everything that affects who may touch the
# cluster (the node identity, workload identity, release channel, labels, the wait) is a LITERAL in
# main.tf, not a variable: a golden test lists these names, so adding a variable is a reviewed act.

variable "project_id" {
  description = "The Google Cloud project the cluster is created in, from the onboarding record."
  type        = string
}

variable "location" {
  description = "A zone or a region, from the onboarding record."
  type        = string
}

variable "cluster_name" {
  description = "The deterministic cloud name, dcf- followed by the cluster uid in lower-case base32."
  type        = string

  validation {
    condition     = can(regex("^dcf-[a-z2-7]{26}$", var.cluster_name))
    error_message = "cluster_name must be dcf- followed by 26 lower-case base32 characters."
  }
}

variable "cluster_uid" {
  description = "The fleet cluster's uid, recorded as a label on the cluster."
  type        = string

  validation {
    condition     = can(regex("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$", var.cluster_uid))
    error_message = "cluster_uid must be a lower-case uuid."
  }
}

variable "ownership_nonce" {
  description = "The 128-bit ownership nonce, 26 lower-case base32 characters, recorded as a label on the cluster. Confidential before the first submit."
  type        = string
  sensitive   = true

  validation {
    condition     = can(regex("^[a-z2-7]{26}$", var.ownership_nonce))
    error_message = "ownership_nonce must be 26 lower-case base32 characters."
  }
}

variable "data_class" {
  description = "disposable or retained. Informational: it is recorded as a label and authorizes nothing."
  type        = string

  validation {
    condition     = contains(["disposable", "retained"], var.data_class)
    error_message = "data_class must be disposable or retained."
  }
}

variable "network" {
  description = "The VPC network, as a name or self link, or null for the project's default."
  type        = string
  default     = null
}

variable "subnetwork" {
  description = "The subnetwork, or null."
  type        = string
  default     = null
}

variable "spot" {
  description = "Run both pools on spot VMs. For a rig that can be re-run."
  type        = bool
  default     = false
}

variable "database_machine_type" {
  description = "Machine type for the database pool."
  type        = string
  default     = "n2-standard-4"
}

variable "database_node_count" {
  description = "Nodes in the database pool."
  type        = number
  default     = 3

  validation {
    condition     = var.database_node_count >= 1 && floor(var.database_node_count) == var.database_node_count
    error_message = "database_node_count must be a whole number of at least 1."
  }
}

variable "services_machine_type" {
  description = "Machine type for the services pool."
  type        = string
  default     = "t2d-standard-4"
}

variable "services_node_count" {
  description = "Nodes in the services pool."
  type        = number
  default     = 3

  validation {
    condition     = var.services_node_count >= 1 && floor(var.services_node_count) == var.services_node_count
    error_message = "services_node_count must be a whole number of at least 1."
  }
}
