# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The thin root over modules/gke-cluster, against a mocked provider. What is held here is that the
# ROOT's outputs are what they were before the cluster moved into the module, and that the root's
# defaults reach it. The module's own plan (pools, taints, the waiter) is held by the module's tests.

mock_provider "google" {}

variables {
  project_id = "test-project"
}

run "the_default_cluster_keeps_its_outputs" {
  command = plan

  assert {
    condition     = output.get_credentials_command == "gcloud container clusters get-credentials devicechain --location us-east4-b --project test-project"
    error_message = "get_credentials_command should be unchanged by the extraction."
  }
  assert {
    condition     = output.kube_context == "gke_test-project_us-east4-b_devicechain"
    error_message = "kube_context should be unchanged by the extraction."
  }
  assert {
    condition     = output.database_node_selector == "cloud.google.com/gke-nodepool=database"
    error_message = "database_node_selector should be unchanged by the extraction."
  }
  assert {
    condition     = output.database_taint == "dedicated=database:NoSchedule"
    error_message = "database_taint should be unchanged by the extraction."
  }
}

# The root's defaults are the module's inputs: the default cluster has the same two pools it had.
run "the_defaults_reach_the_module" {
  command = plan

  assert {
    condition     = join(",", sort(keys(module.gke_cluster.pool_names))) == "database,services"
    error_message = "The default cluster should have exactly a database and a services pool."
  }
  assert {
    condition     = module.gke_cluster.cluster_name == "devicechain"
    error_message = "The default cluster name should reach the module."
  }
}

# A person's overrides reach the module too: a root that dropped one would silently build the default.
run "an_override_reaches_the_module" {
  command = plan

  variables {
    cluster_name       = "perf-7"
    location           = "us-central1-a"
    loadgen_node_count = 1
  }

  assert {
    condition     = module.gke_cluster.cluster_name == "perf-7"
    error_message = "cluster_name should reach the module."
  }
  assert {
    condition     = join(",", sort(keys(module.gke_cluster.pool_names))) == "database,loadgen,services"
    error_message = "loadgen_node_count should reach the module."
  }
  assert {
    condition     = output.kube_context == "gke_test-project_us-central1-a_perf-7"
    error_message = "the root's outputs should follow the overrides."
  }
}

# The root keeps the validations a person meets first.
run "the_root_refuses_an_empty_database_pool" {
  command = plan

  variables {
    database_node_count = 0
  }

  expect_failures = [var.database_node_count]
}
