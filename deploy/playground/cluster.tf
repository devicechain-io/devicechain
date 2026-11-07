# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# One small GKE cluster for the hosted playground, in a project of its own, with a
# budget alert. It creates the cluster and nothing inside it, and nothing DeviceChain
# serves is public: no address, no DNS record, no load balancer. DeviceChain goes on with
# dcctl, and what the internet can reach is decided afterwards by hand (README.md, "Going
# public").
#
# Two things are public the way every GKE Standard cluster's are, and are said here rather
# than discovered: the Kubernetes API endpoint (authenticated; restrict who may even reach
# it with control_plane_authorized_cidrs) and the nodes' external addresses (a private
# cluster needs Cloud NAT to pull images, which is a cost and a decision of its own).

locals {
  cluster_enabled = var.create_cluster ? 1 : 0
}

# Everything below hangs off this. The cost bound IS the budget alert, so a cluster
# planned without a project, a billing account or an address for the alert is refused
# before any of it is planned, rather than created with nothing watching its bill.
resource "terraform_data" "cluster_inputs" {
  count = local.cluster_enabled

  lifecycle {
    precondition {
      condition     = trimspace(var.project_id) != ""
      error_message = "create_cluster needs project_id: the playground lives in a Google Cloud project of its own."
    }
    precondition {
      condition     = trimspace(var.billing_account) != ""
      error_message = "create_cluster needs billing_account: the budget alert is what bounds the playground's cost."
    }
    precondition {
      condition     = trimspace(var.budget_alert_email) != ""
      error_message = "create_cluster needs budget_alert_email: a budget alert that mails nobody is not an alert."
    }
  }
}

resource "google_project_service" "this" {
  for_each = var.create_cluster ? toset(["container.googleapis.com", "compute.googleapis.com", "billingbudgets.googleapis.com", "monitoring.googleapis.com"]) : toset([])

  project = var.project_id
  service = each.key

  # Leave the APIs on when this root is destroyed: other things in the project may
  # have come to depend on them since.
  disable_on_destroy = false

  depends_on = [terraform_data.cluster_inputs]
}

resource "google_container_cluster" "this" {
  count = local.cluster_enabled

  name     = var.cluster_name
  location = var.location

  # The node pool is its own resource, so the default pool GKE insists on is removed.
  remove_default_node_pool = true
  initial_node_count       = 1

  release_channel {
    channel = var.release_channel
  }

  # 🔴 NetworkPolicy objects are enforced only by a dataplane that implements them. The
  # provider's default (LEGACY_DATAPATH with the policy add-on off) accepts and ignores
  # every policy this root creates, so the driver's confinement would be on paper only.
  # Dataplane V2 enforces them with nothing further to switch on, and needs a VPC-native
  # cluster, which is stated rather than assumed.
  datapath_provider = "ADVANCED_DATAPATH"
  ip_allocation_policy {}

  # With workload identity on the cluster and GKE_METADATA on the pool, a pod cannot read
  # the node service account's token from the metadata server.
  workload_identity_config {
    workload_pool = "${var.project_id}.svc.id.goog"
  }

  # Who may reach the Kubernetes API endpoint at all. Empty leaves Google's default.
  dynamic "master_authorized_networks_config" {
    for_each = length(var.control_plane_authorized_cidrs) > 0 ? [1] : []
    content {
      dynamic "cidr_blocks" {
        for_each = var.control_plane_authorized_cidrs
        content {
          cidr_block   = cidr_blocks.value
          display_name = "maintainer"
        }
      }
    }
  }

  # dcctl install brings its own Prometheus; a second managed one would bill per sample.
  monitoring_config {
    enable_components = ["SYSTEM_COMPONENTS"]
    managed_prometheus {
      enabled = false
    }
  }

  deletion_protection = var.deletion_protection
  resource_labels     = var.labels

  depends_on = [google_project_service.this]
}

resource "google_container_node_pool" "this" {
  count = local.cluster_enabled

  name       = "playground"
  cluster    = google_container_cluster.this[0].id
  location   = var.location
  node_count = var.node_count

  node_config {
    machine_type = var.machine_type
    disk_type    = "pd-balanced"
    disk_size_gb = var.disk_size_gb
    spot         = var.spot
    labels       = var.labels

    # Logging and monitoring only. cloud-platform would hand every pod on the node the
    # whole of the node service account's IAM; the images come from a public registry, so
    # no storage scope is needed either.
    oauth_scopes = [
      "https://www.googleapis.com/auth/logging.write",
      "https://www.googleapis.com/auth/monitoring",
    ]

    workload_metadata_config {
      mode = "GKE_METADATA"
    }
  }

  management {
    auto_repair  = true
    auto_upgrade = true
  }
}

data "google_project" "this" {
  count      = local.cluster_enabled
  project_id = var.project_id

  depends_on = [terraform_data.cluster_inputs]
}

resource "google_monitoring_notification_channel" "budget" {
  count = local.cluster_enabled

  project      = var.project_id
  display_name = "playground budget alert"
  type         = "email"
  labels = {
    email_address = var.budget_alert_email
  }

  depends_on = [google_project_service.this]
}

# A Google budget ALERTS; it never stops spending. It is the hard line only in the sense
# that the maintainer is told at half, nine tenths and the whole of it, and again when
# the month is forecast to pass it.
resource "google_billing_budget" "this" {
  count = local.cluster_enabled

  billing_account = var.billing_account
  display_name    = "${var.cluster_name} monthly budget"

  budget_filter {
    projects = ["projects/${data.google_project.this[0].number}"]
  }

  amount {
    specified_amount {
      currency_code = "USD"
      units         = tostring(floor(var.budget_usd))
    }
  }

  dynamic "threshold_rules" {
    for_each = [
      { percent = 0.5, basis = "CURRENT_SPEND" },
      { percent = 0.9, basis = "CURRENT_SPEND" },
      { percent = 1.0, basis = "CURRENT_SPEND" },
      { percent = 1.0, basis = "FORECASTED_SPEND" },
    ]
    content {
      threshold_percent = threshold_rules.value.percent
      spend_basis       = threshold_rules.value.basis
    }
  }

  all_updates_rule {
    monitoring_notification_channels = [google_monitoring_notification_channel.budget[0].id]
    disable_default_iam_recipients   = false
  }
}
