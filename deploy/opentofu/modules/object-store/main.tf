# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The object store that holds the database backups (ADR-028, ADR-020 A2.5).
#
# WHAT THIS IS FOR, STATED PLAINLY BECAUSE THE NAME OVERSELLS IT
#
# The Barman Cloud plugin archives WAL and writes base backups to an S3-compatible
# endpoint. It needs one to exist. On a managed cloud that endpoint is S3 or GCS
# and this module is not used at all (`backup_destination = "external"`). This
# module is the answer for everyone else: a single-replica MinIO in the same
# cluster, so that a plain `dcctl bootstrap` produces an instance whose WAL is
# genuinely being archived rather than one carrying a backup plugin with nowhere
# to put anything.
#
# 🔴 AN IN-CLUSTER BUCKET IS NOT OFF-SITE BACKUP, and nothing downstream of here
# should imply that it is. It shares the cluster's failure domain and, on a
# single-node install, the node's disk. Lose the cluster and you lose the backups
# with it. What it genuinely buys is the recovery people actually need most often:
#
#   - point-in-time recovery from an operator error, a bad migration, a bad
#     delete — the cases where the cluster is fine and the DATA is wrong;
#   - a restore rehearsal that exercises the same code path production uses;
#   - a real archive-lag signal, which cannot exist without a real destination.
#
# What it does NOT buy is survival of the cluster. `dcctl ha verify` says so, the
# deployment docs say so, and `backup_destination = "external"` is the one-line
# change that fixes it.
#
# WHY THE DEFAULT IS THIS RATHER THAN "CONFIGURE A BUCKET FIRST"
#
# The alternative was considered and is worse. Requiring an external bucket before
# anything is archived means the default install has the plugin, the ObjectStore
# CRDs, a `database_backups_enabled = true` output — and no backups. That is the
# same shape as the false-HA trap ADR-020 A0 closed for the broker: configuration
# that reads as a guarantee while the guarantee is absent. Better to archive
# somewhere real and be honest about that somewhere's limits.
#
# WHY MINIO, AND THE LICENSE NOTE
#
# It is the reference object store in the Barman Cloud plugin's own documentation,
# so it is the best-tested target for the exact code path that matters here. Note
# the MinIO server is AGPL-3.0 while DeviceChain is Apache-2.0: we REFERENCE an
# upstream image and never build, modify or redistribute it, so nothing here
# affects DeviceChain's own licensing. Anyone who would rather not run an AGPL
# component at all sets `backup_destination = "external"` and points at their own
# storage — which is the recommended production configuration regardless.

terraform {
  required_providers {
    kubernetes = {
      source = "hashicorp/kubernetes"
    }
  }
}

variable "namespace" {
  description = "Namespace to run the object store in. Deliberately the same namespace as the database Clusters -- it is infrastructure for them, and the plugin's sidecar reaches it by in-cluster DNS."
  type        = string
}

variable "name" {
  description = "Object name prefix, and the Service name the plugin's endpoint URL resolves to."
  type        = string
  default     = "dc-object-store"
}

variable "image" {
  description = <<-EOT
    MinIO server image, PINNED.

    Pinned for the same reason the barman plugin is (see modules/cnpg): this
    component stands between the platform and its backups. An unpinned tag means
    a routine `tofu apply` can move the storage layer under an archive that has
    to stay readable, and the failure mode of a bad object-store upgrade is not
    an outage -- it is backups that stop being written while everything reports
    healthy.
  EOT
  type        = string
  default     = "quay.io/minio/minio:RELEASE.2025-04-22T22-12-26Z"
}

variable "buckets" {
  description = <<-EOT
    Buckets to create. One per database store, never one bucket with two
    prefixes: two Clusters with two ObjectStores are two independent
    backup/restore domains, which is exactly the core-data vs event-data split
    the DR track calls for. A shared bucket would make "restore the core
    database" and "restore the event database" one operation with one retention
    policy, and they are neither.
  EOT
  type        = list(string)

  validation {
    condition     = length(var.buckets) > 0
    error_message = "at least one bucket is required; an object store with no buckets has nowhere to archive to."
  }

  validation {
    # Bucket names become path segments in an s3:// URL and are created here as
    # directories on the data volume. Anything outside this grammar either fails
    # at bucket creation or, worse, creates something the plugin cannot address.
    condition     = alltrue([for b in var.buckets : can(regex("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$", b))])
    error_message = "bucket names must be 3-63 characters of lowercase letters, digits, dots and hyphens, starting and ending alphanumerically."
  }

  validation {
    condition     = length(distinct(var.buckets)) == length(var.buckets)
    error_message = "bucket names must be unique; two stores sharing a bucket are not two backup domains."
  }
}

variable "access_key" {
  description = "Object store access key. Stable across applies rather than minted per run, matching postgres_password -- see the credentials Secret below for why that is load-bearing and not just convenient."
  type        = string
  default     = "devicechain"
}

variable "secret_key" {
  description = "Object store secret key. Override for any deploy that is not a local one."
  type        = string
  default     = "devicechain"
  sensitive   = true

  validation {
    # MinIO refuses to start below 8 characters. Catching it here names the
    # variable; catching it at runtime is a CrashLoopBackOff whose logs nobody
    # is reading because the databases came up fine.
    condition     = length(var.secret_key) >= 8
    error_message = "secret_key must be at least 8 characters -- MinIO refuses to start with a shorter one."
  }
}

variable "storage" {
  description = <<-EOT
    Size of the object store's data volume.

    🔴 Sizing this is not the same question as sizing a database volume, and the
    default is deliberately larger than it looks like it needs to be. This holds
    a base backup PLUS every WAL segment since it, for the whole retention
    window, for BOTH stores. When it fills, archiving fails -- and archiving that
    fails does not stall commits, it accumulates WAL on the database's own volume
    until THAT fills and Postgres stops. So a too-small bucket takes the database
    down by a route that points nowhere near the bucket.
  EOT
  type        = string
  default     = "20Gi"
}

variable "storage_class" {
  description = "StorageClass for the data volume. Empty uses the cluster default. For anything but a scratch install, a class whose reclaimPolicy is Retain."
  type        = string
  default     = ""
}

variable "resources" {
  description = "Requests for the object store pod. Requests only, matching the databases and the operator: a memory limit here turns a large base backup upload into an OOMKill of the thing receiving the backup."
  type = object({
    cpu    = string
    memory = string
  })
  default = {
    cpu    = "50m"
    memory = "256Mi"
  }
}

locals {
  # The port MinIO's S3 API listens on. Not a variable: it appears in the Service,
  # the container args, the probes and the endpoint URL this module outputs, and
  # every consumer of the output takes it from there. A variable would let four of
  # those five agree.
  api_port = 9000

  labels = {
    "app.kubernetes.io/name"       = var.name
    "app.kubernetes.io/component"  = "object-store"
    "app.kubernetes.io/managed-by" = "opentofu"
  }
}

# 🔴 STABLE, NOT MINTED, and this is the A8 lesson applied rather than re-learned.
#
# A bootstrap re-run that rotates a generated credential leaves the two sides of
# it disagreeing until everything that holds a copy has been restarted. A8 found
# exactly that across five credentials and fixed it by making every generated
# value reuse what the cluster is already running. The cheapest way not to have
# that problem at all is not to generate the value: these come from OpenTofu
# variables whose defaults do not change between applies, the same posture
# postgres_password already has.
#
# It matters more here than it looks. If these rotated, MinIO would come back
# with new credentials while the plugin sidecars kept presenting the old ones, so
# WAL archiving would start failing SILENTLY -- the database keeps accepting
# writes, nothing crashes, and the only signal is the archive-lag alert this
# slice also ships. That is a credential rotation whose symptom is "backups
# quietly stopped three weeks ago".
resource "kubernetes_secret_v1" "credentials" {
  metadata {
    name      = "${var.name}-credentials"
    namespace = var.namespace
    labels    = local.labels
  }

  data = {
    # The key names are what the ObjectStore's s3Credentials reference by
    # `key:`, and what MinIO's own container reads via envFrom-equivalent
    # value_from below. One Secret, two readers, so the names are part of this
    # module's contract -- see the outputs.
    MINIO_ROOT_USER     = var.access_key
    MINIO_ROOT_PASSWORD = var.secret_key
  }
}

resource "kubernetes_persistent_volume_claim_v1" "data" {
  metadata {
    name      = "${var.name}-data"
    namespace = var.namespace
    labels    = local.labels
  }

  spec {
    access_modes = ["ReadWriteOnce"]
    resources {
      requests = {
        storage = var.storage
      }
    }
    storage_class_name = var.storage_class != "" ? var.storage_class : null
  }

  # 🔴 The default StorageClass on most clusters -- and on kind -- is
  # WaitForFirstConsumer, so this PVC stays Pending until the Deployment below
  # schedules a pod onto a node. That is correct behaviour and NOT a failure, but
  # Terraform's default is to block until the claim is Bound, which never happens
  # because the consumer is created afterwards. Measured: a fresh apply hangs
  # here forever with the PVC Pending and the pod never created.
  wait_until_bound = false
}

resource "kubernetes_deployment_v1" "this" {
  metadata {
    name      = var.name
    namespace = var.namespace
    labels    = local.labels
  }

  spec {
    replicas = 1

    selector {
      match_labels = {
        "app.kubernetes.io/name" = var.name
      }
    }

    # 🔴 Recreate, and it is REQUIRED rather than a preference. The data volume is
    # ReadWriteOnce, so a RollingUpdate would try to start a second pod that
    # cannot mount the volume the first one holds; the new pod sits in
    # ContainerCreating on a FailedAttachVolume and the rollout blocks until it
    # times out. The old pod does keep serving throughout, which is precisely why
    # this is easy to ship and only discover during an upgrade.
    strategy {
      type = "Recreate"
    }

    template {
      metadata {
        labels = local.labels
      }

      spec {
        security_context {
          # The upstream image runs as uid 1000. Naming it makes the volume's
          # ownership deterministic rather than inherited from whatever the image
          # currently does.
          run_as_user = 1000
          fs_group    = 1000
        }

        # 🔑 BUCKET CREATION, without a second image and without a Job.
        #
        # MinIO's filesystem backend treats each top-level directory under the
        # data root as a bucket, so creating the directories IS creating the
        # buckets. The alternative -- a Job running `mc mb` against the API --
        # needs a second image, credentials in a second place, and a retry loop
        # for the window before MinIO is listening, and it can fail after the
        # apply reports success.
        #
        # 🔴 This runs on EVERY pod start, which is what makes it correct rather
        # than merely convenient: `mkdir -p` is idempotent, so a bucket deleted
        # by hand comes back on the next restart, whereas a one-shot Job's
        # completion is remembered forever and would not.
        init_container {
          name    = "create-buckets"
          image   = var.image
          command = ["/bin/sh", "-c"]
          args = [
            # Quoted so a bucket name can never be word-split into two mkdirs.
            # The variable's own validation already refuses whitespace; this is
            # the second of the two, because the cost of being wrong is a bucket
            # that silently does not exist and an archive that silently fails.
            join(" && ", [for b in var.buckets : "mkdir -p '/data/${b}'"])
          ]

          volume_mount {
            name       = "data"
            mount_path = "/data"
          }
        }

        container {
          name  = "minio"
          image = var.image
          args  = ["server", "/data", "--address", ":${local.api_port}"]

          # 🔴 No --console-address, so no web console. It would be a second
          # listener with the same root credentials on an unauthenticated-by-
          # default port, in a namespace holding both databases, to serve a UI
          # nothing in the platform uses. Recent MinIO community builds have
          # removed most of the console anyway; not depending on it means that
          # removal is not our problem.

          env {
            name = "MINIO_ROOT_USER"
            value_from {
              secret_key_ref {
                name = kubernetes_secret_v1.credentials.metadata[0].name
                key  = "MINIO_ROOT_USER"
              }
            }
          }

          env {
            name = "MINIO_ROOT_PASSWORD"
            value_from {
              secret_key_ref {
                name = kubernetes_secret_v1.credentials.metadata[0].name
                key  = "MINIO_ROOT_PASSWORD"
              }
            }
          }

          # Browser off: same argument as the console address above.
          env {
            name  = "MINIO_BROWSER"
            value = "off"
          }

          port {
            name           = "s3"
            container_port = local.api_port
          }

          # 🔴 The readiness probe is what stands between "the apply finished" and
          # "the databases can archive". /minio/health/ready reports whether the
          # backend can actually serve object operations, not merely whether the
          # process is up -- and the Cluster resources are applied in the same
          # run, so a pod that is Running but not yet serving would take the first
          # WAL archive attempts with it.
          readiness_probe {
            http_get {
              path = "/minio/health/ready"
              port = local.api_port
            }
            initial_delay_seconds = 5
            period_seconds        = 5
            failure_threshold     = 6
          }

          # Liveness on /minio/health/live, which unlike the readiness path does
          # NOT consult the backend. That asymmetry is deliberate: a store whose
          # disk has filled should go UNREADY (stop taking archives, raise the
          # alert) rather than be killed and restarted into the same full disk.
          liveness_probe {
            http_get {
              path = "/minio/health/live"
              port = local.api_port
            }
            initial_delay_seconds = 15
            period_seconds        = 20
            failure_threshold     = 3
          }

          resources {
            requests = {
              cpu    = var.resources.cpu
              memory = var.resources.memory
            }
          }

          volume_mount {
            name       = "data"
            mount_path = "/data"
          }
        }

        volume {
          name = "data"
          persistent_volume_claim {
            claim_name = kubernetes_persistent_volume_claim_v1.data.metadata[0].name
          }
        }
      }
    }
  }

  # Bound to the pod becoming Ready, not merely created. Everything that follows
  # in the apply -- the two Clusters, each with an ObjectStore pointing here --
  # assumes there is something at the endpoint URL.
  wait_for_rollout = true

  timeouts {
    create = "10m"
    update = "10m"
  }
}

resource "kubernetes_service_v1" "this" {
  metadata {
    name      = var.name
    namespace = var.namespace
    labels    = local.labels
  }

  spec {
    selector = {
      "app.kubernetes.io/name" = var.name
    }

    port {
      name        = "s3"
      port        = local.api_port
      target_port = local.api_port
    }
  }
}

output "endpoint_url" {
  description = "The S3 endpoint the Barman Cloud plugin should be pointed at. Namespace-qualified rather than bare, because the plugin's sidecar runs in the database pod and the ObjectStore is resolved from there."
  value       = "http://${kubernetes_service_v1.this.metadata[0].name}.${var.namespace}:${local.api_port}"
}

output "credentials_secret" {
  description = "Name of the Secret holding the access key and secret key. The ObjectStore resources reference this by name, with the key names below."
  value       = kubernetes_secret_v1.credentials.metadata[0].name
}

output "access_key_id_key" {
  description = "Key within credentials_secret holding the access key ID. Exported rather than restated at the caller: the Secret's key names are this module's contract, and a caller that hard-codes them keeps working right up until they change."
  value       = "MINIO_ROOT_USER"
}

output "secret_access_key_key" {
  description = "Key within credentials_secret holding the secret access key."
  value       = "MINIO_ROOT_PASSWORD"
}

output "buckets" {
  description = "The buckets created, echoed back so a caller composing s3:// destinations uses the list that was actually provisioned rather than a second copy of it."
  value       = var.buckets
}
