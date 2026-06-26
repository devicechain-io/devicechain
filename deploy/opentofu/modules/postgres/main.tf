# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# A single-node Postgres-family database as a StatefulSet + headless Service +
# Secret. Generic on the image, so it serves both the relational Postgres and
# TimescaleDB (a Postgres superset) — the only difference is the image. The
# headless Service gives the pod stable DNS at <name>.<namespace>, which is also
# what clients connect to. Single replica for the launch slice; HA/replication is
# the ADR-020 follow-up.

variable "namespace" {
  type = string
}

variable "name" {
  description = "Object name; also the in-cluster host (<name>.<namespace>)."
  type        = string
}

variable "image" {
  type = string
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
  description = "StorageClass for the data volume. Empty uses the cluster default. For production durability point this at a StorageClass whose reclaimPolicy is Retain, so the underlying volume (and its data) survives PVC/PV deletion."
  type        = string
  default     = ""
}

variable "port" {
  type    = number
  default = 5432
}

locals {
  labels = {
    "app.kubernetes.io/name"       = var.name
    "app.kubernetes.io/component"  = "database"
    "app.kubernetes.io/managed-by" = "opentofu"
  }
}

resource "kubernetes_secret_v1" "creds" {
  metadata {
    name      = "${var.name}-credentials"
    namespace = var.namespace
    labels    = local.labels
  }

  # Consumed as env by the postgres entrypoint on first init.
  data = {
    POSTGRES_DB       = var.database
    POSTGRES_USER     = var.username
    POSTGRES_PASSWORD = var.password
  }

  type = "Opaque"
}

resource "kubernetes_stateful_set_v1" "db" {
  metadata {
    name      = var.name
    namespace = var.namespace
    labels    = local.labels
  }

  # Databases outlive the deployment: refuse a naive `tofu destroy`. Intentional
  # teardown requires removing this guard (or a targeted/state-surgery removal).
  lifecycle {
    prevent_destroy = true
  }

  spec {
    service_name = var.name
    replicas     = 1

    # Never reap the data PVCs when the StatefulSet is deleted or scaled down —
    # the volumes (and their data) persist across app upgrades/uninstalls.
    persistent_volume_claim_retention_policy {
      when_deleted = "Retain"
      when_scaled  = "Retain"
    }

    selector {
      match_labels = local.labels
    }

    template {
      metadata {
        labels = local.labels
      }

      spec {
        container {
          name  = "postgres"
          image = var.image

          port {
            container_port = var.port
            name           = "postgres"
          }

          env_from {
            secret_ref {
              name = kubernetes_secret_v1.creds.metadata[0].name
            }
          }

          # Keep PGDATA in a subdirectory so the volume mount root (which may hold
          # a lost+found) is not the data dir — the postgres image requires this.
          env {
            name  = "PGDATA"
            value = "/var/lib/postgresql/data/pgdata"
          }

          volume_mount {
            name       = "data"
            mount_path = "/var/lib/postgresql/data"
          }

          resources {
            requests = {
              cpu    = "100m"
              memory = "256Mi"
            }
          }

          readiness_probe {
            exec {
              command = ["pg_isready", "-U", var.username, "-d", var.database]
            }
            initial_delay_seconds = 10
            period_seconds        = 10
          }

          liveness_probe {
            tcp_socket {
              port = "postgres"
            }
            initial_delay_seconds = 30
            period_seconds        = 20
          }
        }
      }
    }

    volume_claim_template {
      metadata {
        name = "data"
      }
      spec {
        access_modes = ["ReadWriteOnce"]
        # Empty leaves storage_class_name unset → the cluster default applies.
        # Point at a Retain StorageClass in production for volume durability.
        storage_class_name = var.storage_class != "" ? var.storage_class : null
        resources {
          requests = {
            storage = var.storage
          }
        }
      }
    }
  }
}

# Headless service: stable pod DNS for the StatefulSet, and the host clients use.
resource "kubernetes_service_v1" "db" {
  metadata {
    name      = var.name
    namespace = var.namespace
    labels    = local.labels
  }

  spec {
    cluster_ip = "None"
    selector   = local.labels

    port {
      name        = "postgres"
      port        = var.port
      target_port = "postgres"
    }
  }
}

output "host" {
  description = "In-cluster host:port for clients."
  value       = "${var.name}.${var.namespace}:${var.port}"
}

output "service" {
  description = "In-cluster service host."
  value       = "${var.name}.${var.namespace}"
}
