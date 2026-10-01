# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# A GKE cluster to install DeviceChain into. It creates the cluster and nothing
# inside it: DeviceChain itself goes on with `dcctl install --kube-context`, as it
# would on any cluster, so this root and the deploy/opentofu roots never share state.

resource "google_container_cluster" "this" {
  name     = var.cluster_name
  location = var.location

  # The node pools below are managed as their own resources, so the default pool
  # GKE insists on creating is removed as soon as the cluster exists.
  remove_default_node_pool = true
  initial_node_count       = 1

  release_channel {
    channel = var.release_channel
  }

  # `dcctl install` brings its own Prometheus and Grafana. GKE's Managed Service
  # for Prometheus and its kube-state, cAdvisor and kubelet packages would collect
  # the same things a second time, and they bill per sample: on a small cluster
  # under load that can cost more than the nodes. The system metrics are free and
  # stay on.
  monitoring_config {
    enable_components = ["SYSTEM_COMPONENTS"]
    managed_prometheus {
      enabled = false
    }
  }

  # Lets `destroy` delete the cluster. The provider's default (true) makes a
  # destroy fail, which for a cluster meant to be torn down is the wrong default.
  deletion_protection = false

  resource_labels = var.labels
}

# Three pools, one definition. GKE labels every node of a pool with
# cloud.google.com/gke-nodepool=<name>, and that label is what selects a pool: the
# outputs derive the database pool's selector from its name, so there is no second
# label for the same set of nodes to disagree with it.
#
# database: memory, because Postgres keeps a small buffer cache of its own and
#   leans on the node's page cache for the rest. Tainted, so only a pod that
#   tolerates `dedicated=database:NoSchedule` runs there.
# services: CPU, for everything else. The services use little memory.
# loadgen:  optional, tainted, so a load test does not take CPU from what it measures.
locals {
  pools = {
    database = {
      machine_type = var.database_machine_type
      node_count   = var.database_node_count
      disk_type    = var.database_disk_type
      disk_size_gb = var.database_disk_size_gb
      taints       = [{ key = "dedicated", value = "database", effect = "NO_SCHEDULE" }]
    }
    services = {
      machine_type = var.services_machine_type
      node_count   = var.services_node_count
      disk_type    = var.services_disk_type
      disk_size_gb = var.services_disk_size_gb
      taints       = []
    }
    loadgen = {
      machine_type = var.loadgen_machine_type
      node_count   = var.loadgen_node_count
      disk_type    = var.loadgen_disk_type
      disk_size_gb = var.loadgen_disk_size_gb
      taints       = [{ key = "dedicated", value = "loadgen", effect = "NO_SCHEDULE" }]
    }
  }

  # The GKE API and Kubernetes spell a taint's effect differently. No default on
  # the lookup: an effect missing here fails the plan instead of printing a taint
  # no pod can tolerate.
  kubernetes_taint_effect = {
    NO_SCHEDULE        = "NoSchedule"
    PREFER_NO_SCHEDULE = "PreferNoSchedule"
    NO_EXECUTE         = "NoExecute"
  }
}

resource "google_container_node_pool" "this" {
  # database and services are validated to at least one node; loadgen is optional.
  for_each = { for name, pool in local.pools : name => pool if pool.node_count > 0 }

  name       = each.key
  cluster    = google_container_cluster.this.id
  location   = var.location
  node_count = each.value.node_count

  node_config {
    machine_type = each.value.machine_type
    disk_type    = each.value.disk_type
    disk_size_gb = each.value.disk_size_gb
    labels       = var.labels
    oauth_scopes = ["https://www.googleapis.com/auth/cloud-platform"]

    dynamic "taint" {
      for_each = each.value.taints
      content {
        key    = taint.value.key
        value  = taint.value.value
        effect = taint.value.effect
      }
    }
  }

  # A release channel requires node auto-upgrade, so it cannot be turned off here.
  # See the README before running a long measurement.
  management {
    auto_repair  = true
    auto_upgrade = true
  }
}

# The load-generator pool was its own resource before the pools shared one
# definition. Same name, so a cluster that has one keeps it rather than replacing it.
moved {
  from = google_container_node_pool.loadgen[0]
  to   = google_container_node_pool.this["loadgen"]
}
