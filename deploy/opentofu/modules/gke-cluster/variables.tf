# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# NO VARIABLE HERE HAS A DEFAULT. Each root passes every value, so there is exactly one default
# set (the public root's, deploy/gke/variables.tf) and a root that forgets one fails at plan time
# instead of silently taking a value nobody chose.

variable "project_id" {
  description = "The Google Cloud project the cluster is created in. Billing must be linked and the container and compute APIs enabled."
  type        = string
}

variable "location" {
  description = "A zone (us-east4-b) for a zonal cluster, or a region (us-east4) for a regional one. A regional cluster creates the node pools in EVERY zone of the region, so it runs three times the nodes this file asks for."
  type        = string
}

variable "cluster_name" {
  description = "Name of the GKE cluster."
  type        = string
}

variable "release_channel" {
  description = "GKE release channel: RAPID, REGULAR, STABLE or EXTENDED. DeviceChain needs Kubernetes 1.29 or newer, which every channel is past."
  type        = string

  validation {
    condition     = contains(["RAPID", "REGULAR", "STABLE", "EXTENDED"], var.release_channel)
    error_message = "release_channel must be one of RAPID, REGULAR, STABLE or EXTENDED."
  }
}

variable "database_machine_type" {
  description = "Machine type for the database pool. Choose memory: Postgres keeps a small buffer cache of its own and relies on the node's page cache for the rest. See the README."
  type        = string
}

variable "database_node_count" {
  description = "Nodes in the database pool. The pool is tainted, so only a pod placed there runs there; once the databases are placed on it, --ha puts one instance of each database on a different node, which needs 3."
  type        = number

  validation {
    condition     = var.database_node_count >= 1 && floor(var.database_node_count) == var.database_node_count
    error_message = "database_node_count must be a whole number of at least 1."
  }
}

variable "database_disk_type" {
  description = "Boot disk type for the database pool's nodes. The databases do NOT use it — they write to their own persistent volumes, whose class is chosen by the cluster's default StorageClass (see the README). pd-standard counts against the region's DISKS_TOTAL_GB quota, which leaves SSD_TOTAL_GB to those volumes; pd-balanced and pd-ssd would draw on SSD_TOTAL_GB too. Changing it recreates this pool's nodes. The README's quota step gives the budget."
  type        = string
}

variable "database_disk_size_gb" {
  description = "Boot disk size for the database pool's nodes, in GB. A standard disk's speed grows with its size (see the README), so it is not set smaller to save quota."
  type        = number
}

variable "services_machine_type" {
  description = "Machine type for the services pool, where everything but the databases runs. CPU binds first, then memory: on 4 GB nodes NATS and the services left too little page cache. The default is t2d-standard-4: 4 vCPU (four physical cores, no SMT) and 16 GB. See the README, including which zones offer T2D."
  type        = string
}

variable "services_node_count" {
  description = "Nodes in the services pool. `dcctl install --ha` needs at least 3: NATS places one server per node, and only untainted nodes count."
  type        = number

  validation {
    condition     = var.services_node_count >= 1 && floor(var.services_node_count) == var.services_node_count
    error_message = "services_node_count must be a whole number of at least 1."
  }
}

variable "services_disk_type" {
  description = "Boot disk type for the services pool's nodes. NATS does NOT use it — it writes to its own persistent volumes, whose class is chosen by the cluster's default StorageClass (see the README). pd-standard counts against the region's DISKS_TOTAL_GB quota, which leaves SSD_TOTAL_GB to those volumes; pd-balanced and pd-ssd would draw on SSD_TOTAL_GB too. Changing it recreates this pool's nodes. The README's quota step gives the budget."
  type        = string
}

variable "services_disk_size_gb" {
  description = "Boot disk size for the services pool's nodes, in GB. A standard disk's speed grows with its size (see the README), so it is not set smaller to save quota."
  type        = number
}

variable "loadgen_node_count" {
  description = "Nodes in a separate, tainted pool for a load generator, so a load test does not take CPU from the platform it measures. 0 creates no pool."
  type        = number

  validation {
    condition     = var.loadgen_node_count >= 0 && floor(var.loadgen_node_count) == var.loadgen_node_count
    error_message = "loadgen_node_count must be a whole number, 0 or more."
  }
}

variable "loadgen_machine_type" {
  description = "Machine type for the load-generator pool."
  type        = string
}

variable "loadgen_disk_type" {
  description = "Boot disk type for the load-generator pool's nodes. pd-standard counts against the region's DISKS_TOTAL_GB quota; pd-balanced and pd-ssd count against SSD_TOTAL_GB, which the install's volumes need. Changing it recreates this pool's nodes."
  type        = string
}

variable "loadgen_disk_size_gb" {
  description = "Boot disk size for the load-generator pool's nodes, in GB. A standard disk's speed grows with its size (see the README), so it is not set smaller to save quota."
  type        = number
}

variable "wait_for_cluster_operations" {
  description = <<-EOT
    Make `apply` wait until GKE has finished every operation on the cluster before it returns.

    GKE is not done when the cluster and its node pools exist: it goes on to apply their
    settings (the monitoring configuration, for one) as an UPDATE_CLUSTER operation, during
    which the cluster is RECONCILING. An install started in that window can time out against an
    API server that is being reconfigured. The wait runs wait-for-cluster-operations.sh, which
    needs bash and the Google Cloud CLI (`gcloud`) on the machine that runs `tofu apply`; set
    this to false only if you have neither and will wait yourself (`gcloud container clusters
    describe NAME --location LOCATION --format='value(status)'` must say RUNNING).
  EOT
  type        = bool
}

variable "cluster_operations_timeout_seconds" {
  description = "How long the wait for the cluster's operations may take before `apply` fails."
  type        = number

  validation {
    condition     = var.cluster_operations_timeout_seconds >= 60
    error_message = "cluster_operations_timeout_seconds must be at least 60."
  }
}

variable "labels" {
  description = "Labels applied to the cluster and its nodes, for finding (and billing) what this created."
  type        = map(string)
}

variable "spot" {
  description = "Run every node pool on spot VMs: cheaper, and preempted at any time. For a rig that can be re-run, never for an install that holds data."
  type        = bool
}

variable "node_service_account" {
  description = "The Google service account every node runs as, as an email. null keeps the project's default node identity."
  type        = string
  nullable    = true

  validation {
    condition     = var.node_service_account == null || can(regex("^[a-z0-9._-]+@[a-z0-9.-]+\\.iam\\.gserviceaccount\\.com$", var.node_service_account))
    error_message = "node_service_account must be a service account email (NAME@PROJECT.iam.gserviceaccount.com), or null."
  }
}

variable "workload_pool" {
  description = "The cluster's workload identity pool (PROJECT.svc.id.goog). null leaves workload identity off."
  type        = string
  nullable    = true

  validation {
    condition     = var.workload_pool == null || can(regex("^[a-z][a-z0-9.:-]*\\.svc\\.id\\.goog$", var.workload_pool))
    error_message = "workload_pool must look like PROJECT.svc.id.goog, or be null."
  }
}

variable "network" {
  description = "The VPC network the cluster joins, as a name or a self link. null keeps the project's default network."
  type        = string
  nullable    = true
}

variable "subnetwork" {
  description = "The subnetwork the cluster's nodes join. null lets GKE choose for the network."
  type        = string
  nullable    = true
}

variable "ownership_labels" {
  description = "Labels that identify who owns the cluster, merged into the CLUSTER's labels only (never the pools' node labels, and never the wait's triggers). Empty for the public root."
  type        = map(string)

  validation {
    condition     = alltrue([for k, v in var.ownership_labels : can(regex("^[a-z][a-z0-9_-]{0,62}$", k)) && can(regex("^[a-z0-9_-]{0,63}$", v))])
    error_message = "ownership_labels keys and values must be GCP label strings: lower-case letters, digits, underscore and hyphen, at most 63 characters, keys starting with a letter."
  }
}
