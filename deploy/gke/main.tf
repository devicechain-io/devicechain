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

resource "google_container_node_pool" "platform" {
  name       = "platform"
  cluster    = google_container_cluster.this.id
  location   = var.location
  node_count = var.node_count

  node_config {
    machine_type = var.node_machine_type
    disk_type    = var.node_disk_type
    disk_size_gb = var.node_disk_size_gb
    labels       = var.labels
    oauth_scopes = ["https://www.googleapis.com/auth/cloud-platform"]
  }

  # A release channel requires node auto-upgrade, so it cannot be turned off here.
  # See the README before running a long measurement.
  management {
    auto_repair  = true
    auto_upgrade = true
  }
}

resource "google_container_node_pool" "loadgen" {
  count = var.loadgen_node_count > 0 ? 1 : 0

  name       = "loadgen"
  cluster    = google_container_cluster.this.id
  location   = var.location
  node_count = var.loadgen_node_count

  node_config {
    machine_type = var.loadgen_machine_type
    disk_type    = var.node_disk_type
    disk_size_gb = var.node_disk_size_gb
    labels       = merge(var.labels, { role = "loadgen" })
    oauth_scopes = ["https://www.googleapis.com/auth/cloud-platform"]

    # Only a pod that tolerates this lands here, so no DeviceChain service does.
    taint {
      key    = "dedicated"
      value  = "loadgen"
      effect = "NO_SCHEDULE"
    }
  }

  management {
    auto_repair  = true
    auto_upgrade = true
  }
}
