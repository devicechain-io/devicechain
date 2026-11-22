# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The fleet root's wiring, against a mocked provider: no credentials, no cluster. The module's own
# plan is held by its tests; what is held here is that THIS root hands the module what a fleet
# cluster needs. Root-level tests read module outputs, not the resources inside it, which is why the
# module exposes `shape`.

mock_provider "google" {}

variables {
  project_id      = "example-project"
  location        = "us-central1-a"
  cluster_name    = "dcf-aaaaaaaaaaaaaaaaaaaaaaaaaa"
  cluster_uid     = "11111111-1111-1111-1111-111111111111"
  ownership_nonce = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
  data_class      = "retained"
}

run "the_cluster_carries_its_ownership_labels_and_the_pools_do_not" {
  command = plan

  assert {
    condition = module.gke_cluster.shape.cluster_labels == tomap({
      app                 = "devicechain-fleet"
      dc-fleet-cluster    = "11111111-1111-1111-1111-111111111111"
      dc-fleet-owner      = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
      dc-fleet-data-class = "retained"
    })
    error_message = "The cluster should carry the uid, nonce and data-class labels."
  }
  assert {
    condition     = alltrue([for labels in values(module.gke_cluster.shape.pool_node_labels) : labels == tomap({ app = "devicechain-fleet" })])
    error_message = "No pool's node labels may carry an ownership label."
  }
}

run "the_nodes_run_as_a_dedicated_identity_with_workload_identity_on" {
  command = plan

  assert {
    condition     = alltrue([for sa in values(module.gke_cluster.shape.pool_service_account) : sa == "dcf-node@example-project.iam.gserviceaccount.com"])
    error_message = "Every pool should run as the dedicated node identity."
  }
  assert {
    condition     = module.gke_cluster.shape.workload_pool == "example-project.svc.id.goog"
    error_message = "Workload identity should be on, in the project's own pool."
  }
}

run "the_waiter_is_off" {
  command = plan

  assert {
    condition     = !module.gke_cluster.shape.waiter_enabled
    error_message = "The runner has no gcloud: the wait must be off."
  }
}

run "pools_are_not_spot_by_default" {
  command = plan

  assert {
    condition     = !anytrue(values(module.gke_cluster.shape.pool_spot))
    error_message = "A fleet cluster is not spot unless asked."
  }
}

# The counterweight to the closed surface: a legitimate one-node, spot rig plan is accepted.
run "a_one_node_spot_rig_is_accepted" {
  command = plan

  variables {
    spot                = true
    database_node_count = 1
    services_node_count = 1
  }

  assert {
    condition     = alltrue(values(module.gke_cluster.shape.pool_spot)) && join(",", sort(keys(module.gke_cluster.pool_names))) == "database,services"
    error_message = "spot should reach both pools, and a one-node rig should plan."
  }
}

run "the_cluster_name_must_be_the_derived_one" {
  command = plan

  variables {
    cluster_name = "my-cluster"
  }

  expect_failures = [var.cluster_name]
}

run "the_nonce_must_be_26_base32_characters" {
  command = plan

  variables {
    ownership_nonce = "short"
  }

  expect_failures = [var.ownership_nonce]
}

run "data_class_is_a_closed_vocabulary" {
  command = plan

  variables {
    data_class = "forever"
  }

  expect_failures = [var.data_class]
}

run "an_empty_pool_is_refused" {
  command = plan

  variables {
    database_node_count = 0
  }

  expect_failures = [var.database_node_count]
}
