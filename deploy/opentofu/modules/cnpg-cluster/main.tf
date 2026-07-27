# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# A DeviceChain database store as a CloudNativePG Cluster (ADR-020 A2).
#
# Generic on image, instance count, durability and bootstrap SQL, so it serves
# BOTH stores exactly as modules/postgres did: `rdb` (A2.3) and `tsdb` (A2.4).
# Keeping one module is the point -- the two stores differ in four values, and a
# second copy would let them drift in the other forty.
#
# WHY A HELM RELEASE AND NOT A `kubernetes_manifest`
#
# A `kubernetes_manifest` resource needs its CRD present at PLAN time. The CNPG
# CRDs are installed by the cnpg module in the SAME apply, so a plan against a
# cluster that does not yet have them fails before anything runs -- and it fails
# on a fresh install only, which is the worst possible place to discover it.
# Helm renders and applies together, so this sidesteps the ordering entirely.
# The cert-manager module documents the same trap for the ingress Issuer.
#
# WHY THE CHART IS LOCAL
#
# Because the upstream cnpg/cluster chart fails open on the two fields this
# module exists to set -- see the long header in chart/templates/cluster.yaml
# for the measurement. In short: a misspelled `services` or `roles` key renders
# a Cluster with no `managed:` block and exits 0.
#
# ONE STORAGE SHAPE, HA OR NOT (decision D4)
#
# This module runs on every install, including `instances = 1`. Backup is not
# an HA feature: a single-instance install needs point-in-time recovery more
# than a replicated one, and if CNPG were the HA-only path then hack/dr-rig.sh
# would be drilling a restore that non-HA production never executes. HA is then
# an instance count, not a storage migration.

terraform {
  required_providers {
    helm = {
      source = "hashicorp/helm"
    }
    kubernetes = {
      source = "hashicorp/kubernetes"
    }
  }
}

variable "namespace" {
  type = string
}

variable "name" {
  description = "Cluster object name. NOT the DNS name clients use -- that is var.alias_service_name. These must differ: CNPG reserves <cluster>-rw/-ro/-r, so naming the Cluster after the legacy Service would collide with its own generated names."
  type        = string
}

variable "alias_service_name" {
  description = "The Service name clients connect to -- the DNS contract this store already has. CNPG creates it as a managed service whose selector it maintains, so it follows the primary across a failover, and its name lands in the server certificate's SANs."
  type        = string
}

variable "image" {
  type = string
}

variable "instances" {
  description = "Number of Postgres instances. 1 is a valid, supported production topology (decision D4); 3 is the smallest count at which synchronous replication tolerates a standby loss."
  type        = number

  validation {
    condition     = var.instances >= 1
    error_message = "instances must be at least 1."
  }
}

variable "synchronous" {
  description = <<-EOT
    Enable synchronous replication.

    🔴 Requires at least `synchronous_number + 2` instances, and the chart
    refuses to render below that. The reason is the whole reason A2.3 costs a
    third node: with `number: 1` and only two instances, losing either standby
    stalls every write, which is WORSE for availability than the single node it
    replaced while being better for durability. Three instances mean one
    standby can go without the cluster losing its sync candidate.
  EOT
  type        = bool
  default     = false
}

variable "synchronous_number" {
  description = "How many standbys must confirm a commit."
  type        = number
  default     = 1
}

variable "data_durability" {
  description = <<-EOT
    `required` holds a write until a standby confirms it; `preferred` falls back
    to asynchronous when no standby is available, and self-heals.

    This is a per-store judgement, not a default to inherit. `rdb` holds the
    audit journal and the control plane, where RPO=0 is the point and a write
    stall is a loud, recoverable failure. `tsdb` takes `preferred`: event ingest
    values continuing, the loss window is bounded by replication lag, and
    JetStream already holds un-persisted events durably upstream so the ingest
    path can replay.

    🔴 What `required` does NOT mean, measured on the A2 spike: a stalled write
    is COMMITTED, not rejected. The commit is local and the wait is for the
    remote flush, so a client that times out and gives up has still written the
    row. "Writes stall" is not "writes are rejected" -- a client that retries
    will double-write unless the operation is idempotent, and a primary lost
    after the local commit but before any standby returns loses a write nobody
    was told about. Synchronous replication protects writes the client WAITED
    for; it cannot protect one the client abandoned.
  EOT
  type        = string
  default     = "required"

  validation {
    condition     = contains(["required", "preferred"], var.data_durability)
    error_message = "data_durability must be \"required\" or \"preferred\"."
  }
}

variable "database" {
  type = string
}

variable "username" {
  type = string
}

variable "password" {
  type      = string
  sensitive = true
}

variable "storage" {
  type    = string
  default = "8Gi"
}

variable "storage_class" {
  description = "StorageClass for the data volume. Empty uses the cluster default. For production durability use a class whose reclaimPolicy is Retain."
  type        = string
  default     = ""
}

variable "wal_storage" {
  description = "Size of a dedicated WAL volume, or empty for none. Worth setting once WAL archiving is on: archiving that stalls accumulates WAL until the volume fills and stops Postgres, and a separate volume bounds that to the WAL."
  type        = string
  default     = ""
}

variable "post_init_template_sql" {
  description = "SQL run against template1 at initdb, so every database the app later creates inherits it. This is where the TimescaleDB extension goes -- postInitApplicationSQL would reach only the bootstrap database. 🔴 Confined to initdb, so it does NOT re-run on a restore."
  type        = list(string)
  default     = []
}

variable "shared_preload_libraries" {
  description = "🔴 A library named here and missing from the image makes Postgres refuse to start, and CNPG cannot self-heal out of that state."
  type        = list(string)
  default     = []
}

variable "parameters" {
  description = "Postgres GUCs."
  type        = map(string)
  default     = {}
}

variable "resources" {
  description = "Resource requests/limits for the Postgres instances."
  type        = any
  default     = {}
}

locals {
  # The credentials Secret, in the shape CNPG's bootstrap expects
  # (kubernetes.io/basic-auth with username/password). Left unset, CNPG mints a
  # password of its own -- and the DSN every service ships would then be unable
  # to log in. So the caller's configured credentials are preserved by handing
  # CNPG a Secret rather than by reading one back.
  credentials_secret = "${var.name}-app-credentials"
}

resource "kubernetes_secret_v1" "app" {
  metadata {
    name      = local.credentials_secret
    namespace = var.namespace
    labels = {
      "app.kubernetes.io/name"       = var.name
      "app.kubernetes.io/component"  = "database"
      "app.kubernetes.io/managed-by" = "opentofu"
      # CNPG watches Secrets carrying this label. Without it the operator does
      # not reload on a credential change.
      "cnpg.io/reload" = "true"
    }
  }

  type = "kubernetes.io/basic-auth"

  data = {
    username = var.username
    password = var.password
  }
}

resource "helm_release" "cluster" {
  name      = var.name
  namespace = var.namespace
  chart     = "${path.module}/chart"

  # The Secret must exist before initdb reads it. This is an explicit edge
  # rather than an inferred one: the values below reference the Secret by NAME
  # (a string), and a string carries no dependency.
  depends_on = [kubernetes_secret_v1.app]

  values = [yamlencode({
    name                   = var.name
    imageName              = var.image
    instances              = var.instances
    aliasServiceName       = var.alias_service_name
    resources              = var.resources
    parameters             = var.parameters
    sharedPreloadLibraries = var.shared_preload_libraries

    storage = {
      size         = var.storage
      storageClass = var.storage_class
    }

    walStorage = {
      enabled      = var.wal_storage != ""
      size         = var.wal_storage != "" ? var.wal_storage : null
      storageClass = var.storage_class
    }

    # 🔴 DERIVED, not passed through. Synchronous replication at fewer than
    # three instances turns any standby loss into a total write outage, so a
    # single-instance install must never get it -- and "the caller remembers to
    # turn it off" is exactly the coupling that produced the false-HA trap A0
    # closed. The chart refuses to render the unsafe combination as well, so
    # this is belt and braces on the one setting whose failure mode is a
    # permanently wedged database.
    synchronous = {
      enabled        = var.synchronous && var.instances >= var.synchronous_number + 2
      method         = "any"
      number         = var.synchronous_number
      dataDurability = var.data_durability
    }

    bootstrap = {
      database            = var.database
      owner               = var.username
      secretName          = local.credentials_secret
      encoding            = "UTF8"
      localeCollate       = "C.utf8"
      localeCType         = "C.utf8"
      postInitTemplateSQL = var.post_init_template_sql
    }
  })]

  # Waits for the Cluster's pods to be Ready. That matters more here than for a
  # normal release: the Helm step that follows this apply starts eleven services
  # which all immediately CREATE DATABASE, and a database that is not yet
  # accepting connections turns into eleven crashlooping pods.
  timeout = 900
}

output "host" {
  description = "In-cluster host:port for clients -- the alias Service, which CNPG keeps pointed at the primary."
  value       = "${var.alias_service_name}.${var.namespace}:5432"
}

output "service" {
  description = "In-cluster service host."
  value       = "${var.alias_service_name}.${var.namespace}"
}

output "cluster_name" {
  description = "The CNPG Cluster object name. Pods carry it as cnpg.io/cluster=<name>; the DR/HA rigs resolve through the alias Service instead, which is topology-independent."
  value       = var.name
}

output "synchronous_enforced" {
  description = "Whether synchronous replication is ACTUALLY in force, after the instance-count derivation. Read this rather than re-deriving it from the flags: an install that asked for synchronous and did not get enough instances runs asynchronously, and the difference is invisible from the Cluster resources alone -- which is the false-HA shape A0 closed for the broker."
  value       = var.synchronous && var.instances >= var.synchronous_number + 2
}
