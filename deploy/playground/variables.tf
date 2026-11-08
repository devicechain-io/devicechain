# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# ---------------------------------------------------------------------------
# What this root does
# ---------------------------------------------------------------------------

variable "create_cluster" {
  description = "Create the GKE cluster, its one spot node pool, and the budget alert. False (the default) creates nothing in Google Cloud, which is how the in-cluster half is applied to a kind cluster."
  type        = bool
  default     = false
}

variable "deploy_driver" {
  description = "Deploy the playground driver and its nightly reset into the cluster the kubeconfig names. Apply this AFTER DeviceChain is installed on that cluster: the driver signs in to the instance."
  type        = bool
  default     = false
}

# ---------------------------------------------------------------------------
# The cluster (create_cluster)
# ---------------------------------------------------------------------------

variable "project_id" {
  description = "A Google Cloud project used for the playground and nothing else. It must NOT be the project the performance rounds run in: the playground's budget alert is only meaningful if nothing else bills there."
  type        = string
  default     = ""

  validation {
    condition     = trimspace(var.project_id) == "" || can(regex("^[a-z][a-z0-9-]{4,28}[a-z0-9]$", var.project_id))
    error_message = "project_id must be a Google Cloud project ID: 6-30 characters, lower-case letters, digits and hyphens, starting with a letter."
  }
}

variable "billing_account" {
  description = "The billing account ID (XXXXXX-XXXXXX-XXXXXX) the budget alert is created on. Needed when create_cluster is true."
  type        = string
  default     = ""
}

variable "budget_alert_email" {
  description = "Where the budget alert is mailed. Needed when create_cluster is true: an alert nobody receives is not an alert."
  type        = string
  default     = ""
}

variable "budget_usd" {
  description = "The monthly budget in US dollars. Alerts go out at 50%, 90% and 100% of actual spend and at 100% of forecast spend. A Google budget alerts; it does not stop spending."
  type        = number
  default     = 300

  validation {
    condition     = var.budget_usd > 0
    error_message = "budget_usd must be greater than zero."
  }
}

variable "location" {
  description = "A zone for a zonal cluster. Zonal on purpose: a regional cluster would run its node pool in every zone of the region."
  type        = string
  default     = "us-east4-b"

  validation {
    condition     = can(regex("^[a-z]+-[a-z]+[0-9]+-[a-z]$", var.location))
    error_message = "location must be a zone such as us-east4-b, not a region."
  }
}

variable "control_plane_authorized_cidrs" {
  description = "IPv4 ranges allowed to reach the Kubernetes API endpoint, such as your own address as 203.0.113.7/32. Empty leaves Google's default, which is reachable from anywhere (every request still has to authenticate). Set it: nothing about the playground needs the API open to the internet."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for c in var.control_plane_authorized_cidrs : can(cidrnetmask(c))])
    error_message = "control_plane_authorized_cidrs must be IPv4 CIDR ranges such as 203.0.113.7/32."
  }
}

variable "cluster_name" {
  description = "Name of the GKE cluster."
  type        = string
  default     = "dc-playground"
}

variable "machine_type" {
  description = "Machine type of the one node pool. The compact install of the default profile is the workload."
  type        = string
  default     = "e2-standard-8"
}

variable "node_count" {
  description = "Nodes in the pool."
  type        = number
  default     = 1

  validation {
    condition     = var.node_count >= 1 && var.node_count <= 3 && floor(var.node_count) == var.node_count
    error_message = "node_count must be a whole number from 1 to 3. The playground is one small pool."
  }
}

variable "spot" {
  description = "Run the pool on spot VMs. They are cheap and Google may reclaim them at any time: the instance restarts and the nightly seed makes the playground whole again, which is acceptable for a demo and would not be for anything else."
  type        = bool
  default     = true
}

variable "disk_size_gb" {
  description = "Boot disk size of each node, in GB."
  type        = number
  default     = 100
}

variable "release_channel" {
  description = "GKE release channel: RAPID, REGULAR, STABLE or EXTENDED."
  type        = string
  default     = "REGULAR"

  validation {
    condition     = contains(["RAPID", "REGULAR", "STABLE", "EXTENDED"], var.release_channel)
    error_message = "release_channel must be one of RAPID, REGULAR, STABLE or EXTENDED."
  }
}

variable "deletion_protection" {
  description = "Refuse to delete the cluster until this is set false and applied. On by default: a public demo is not a scratch cluster."
  type        = bool
  default     = true
}

variable "labels" {
  description = "Labels put on the cluster and its nodes."
  type        = map(string)
  default     = { purpose = "playground" }
}

# ---------------------------------------------------------------------------
# The driver (deploy_driver)
# ---------------------------------------------------------------------------

variable "kubeconfig_path" {
  description = "Kubeconfig the in-cluster half is applied through."
  type        = string
  default     = "~/.kube/config"
}

variable "kubeconfig_context" {
  description = "Context in that kubeconfig. Empty uses the current context, which is a risk when more than one cluster is configured: name it."
  type        = string
  default     = ""
}

variable "namespace" {
  description = "Namespace the driver is deployed into. Not the instance's own."
  type        = string
  default     = "dc-playground"
}

variable "instance_namespace" {
  description = "The namespace of the DeviceChain instance the driver seeds (dci-<instance id>). The driver's network policy allows it to reach this namespace and no other."
  type        = string
  default     = "dci-playground"
}

variable "ingress_namespace" {
  description = "The namespace of the ingress controller the driver reaches the instance through (dcctl install puts Traefik in traefik). The driver's network policy allows it."
  type        = string
  default     = "traefik"
}

variable "driver_image" {
  description = "The driver image: dcctl, dc-simulator and the playground binary. Build it with image/Dockerfile and push it to a registry you own; this root names no image because there is no published one."
  type        = string
  default     = ""
}

variable "superuser_password" {
  description = "The instance superuser's password, which dcctl sim create signs in with to mint each sim's scoped identity. It lands in this root's state and in a Secret: keep the state in an encrypted, access-controlled backend. Read it from the instance's own Secret; do not invent one."
  type        = string
  sensitive   = true
  default     = ""
}

variable "instance_id" {
  description = "The instance id (--instance of dcctl sim)."
  type        = string
  default     = "playground"
}

variable "sim_server" {
  description = "The host dcctl sim reaches the instance at, from inside the cluster (--server). Normally the ingress controller's Service DNS name."
  type        = string
  default     = ""
}

variable "sim_host_header" {
  description = "The Host the instance's Gateway routes on (--host-header), when sim_server is a cluster address and not that hostname. Empty passes none."
  type        = string
  default     = ""
}

variable "sim_tls" {
  description = "Reach the instance over https/wss (--tls)."
  type        = bool
  default     = false
}

variable "sim_create_args" {
  description = "Further flags for dcctl sim create ONLY, such as --ingress and --mqtt-broker. dcctl sim destroy does not have them and refuses them as unknown flags, so they are kept apart from the ones both take. The in-cluster defaults dcctl derives are a workstation's, so a real deployment sets at least the device ingress and the MQTT gateway."
  type        = list(string)
  default     = []
}

variable "scenarios" {
  description = "The dc-simulator scenarios seeded as tenants, each fed live by its simulator."
  type        = list(string)
  default     = ["sitepulse", "buildingpulse", "fleetpulse"]

  validation {
    condition     = length(var.scenarios) > 0 && length(distinct(var.scenarios)) == length(var.scenarios) && alltrue([for s in var.scenarios : contains(["sitepulse", "buildingpulse", "fleetpulse", "widgetlab", "devicepulse"], s)])
    error_message = "scenarios must be a non-empty list without repeats, drawn from sitepulse, buildingpulse, fleetpulse, widgetlab and devicepulse. towerpulse needs a tier of its own and is not allowed on the playground."
  }
}

variable "slots" {
  description = "Rotation slots. A deleted tenant's token is held for 12 hours, so the nightly reset creates a fresh slot and destroys the others instead of recreating a tenant under its own name; fewer than 3 slots would ask for a token that is still held."
  type        = number
  default     = 3

  validation {
    condition     = var.slots >= 3 && var.slots <= 10 && floor(var.slots) == var.slots
    error_message = "slots must be a whole number from 3 to 10."
  }
}

variable "reset_hour_utc" {
  description = "The UTC hour the playground's day begins. The driver rotates at it, and the reset CronJob fires five minutes after it."
  type        = number
  default     = 3

  validation {
    condition     = var.reset_hour_utc >= 0 && var.reset_hour_utc <= 23 && floor(var.reset_hour_utc) == var.reset_hour_utc
    error_message = "reset_hour_utc must be a whole hour from 0 to 23."
  }
}

variable "seed" {
  description = "The generation seed every sim is created with, so a slot holds the same content every day."
  type        = number
  default     = 1
}

variable "network_policy" {
  description = "Confine the driver and the reset job with NetworkPolicies. A cluster whose network plugin does not enforce them ignores the objects."
  type        = bool
  default     = true
}
