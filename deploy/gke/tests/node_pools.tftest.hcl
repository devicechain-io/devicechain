# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The node pools this configuration plans, against a mocked provider: no
# credentials and no cluster. What is held here is the plan's shape -- which
# pools exist, which one is tainted, and what the outputs tell an install --
# that `validate` cannot see.

mock_provider "google" {}

variables {
  project_id = "test-project"
}

run "the_default_cluster_has_a_database_and_a_services_pool" {
  command = plan

  assert {
    condition     = join(",", sort(keys(google_container_node_pool.this))) == "database,services"
    error_message = "The default cluster should have exactly a database and a services pool."
  }
  assert {
    condition     = google_container_node_pool.this["database"].node_config[0].machine_type == "n2-standard-4"
    error_message = "The database pool should default to n2-standard-4."
  }
  assert {
    condition     = google_container_node_pool.this["database"].node_count == 3
    error_message = "The database pool should default to 3 nodes."
  }
  assert {
    condition     = google_container_node_pool.this["services"].node_config[0].machine_type == "n2-highcpu-4"
    error_message = "The services pool should default to n2-highcpu-4."
  }
  assert {
    condition     = google_container_node_pool.this["services"].node_count == 3
    error_message = "The services pool should default to 3 nodes."
  }
}

run "only_the_database_pool_is_tainted" {
  command = plan

  assert {
    condition = join(",", [
      for t in google_container_node_pool.this["database"].node_config[0].taint : "${t.key}=${t.value}:${t.effect}"
    ]) == "dedicated=database:NO_SCHEDULE"
    error_message = "The database pool should carry exactly the taint dedicated=database:NO_SCHEDULE."
  }
  assert {
    condition     = length(google_container_node_pool.this["services"].node_config[0].taint) == 0
    error_message = "The services pool should carry no taint: everything that is not placed elsewhere runs there."
  }
}

run "the_outputs_name_the_database_placement" {
  command = plan

  assert {
    condition     = output.database_node_selector == "cloud.google.com/gke-nodepool=database"
    error_message = "database_node_selector should be the database pool's GKE node-pool label, as key=value."
  }
  assert {
    condition     = output.database_taint == "dedicated=database:NoSchedule"
    error_message = "database_taint should be the database pool's taint in the Kubernetes spelling, key=value:Effect."
  }
}

run "a_load_generator_pool_is_tainted_for_itself" {
  command = plan

  variables {
    loadgen_node_count = 1
  }

  assert {
    condition     = join(",", sort(keys(google_container_node_pool.this))) == "database,loadgen,services"
    error_message = "loadgen_node_count = 1 should add a loadgen pool beside the other two."
  }
  assert {
    condition = join(",", [
      for t in google_container_node_pool.this["loadgen"].node_config[0].taint : "${t.key}=${t.value}:${t.effect}"
    ]) == "dedicated=loadgen:NO_SCHEDULE"
    error_message = "The loadgen pool should carry exactly the taint dedicated=loadgen:NO_SCHEDULE."
  }
}

# Every value is the default of no pool and no other pool's value, so a pool that
# reads another pool's variable shows up as a wrong value here.
run "each_pool_takes_its_own_settings" {
  command = plan

  variables {
    database_machine_type = "n2-highmem-8"
    database_node_count   = 5
    database_disk_type    = "pd-ssd"
    database_disk_size_gb = 61
    services_machine_type = "n2-highcpu-8"
    services_node_count   = 4
    services_disk_type    = "pd-standard"
    services_disk_size_gb = 41
    loadgen_machine_type  = "e2-standard-2"
    loadgen_node_count    = 2
    loadgen_disk_type     = "pd-balanced"
    loadgen_disk_size_gb  = 31
  }

  assert {
    condition = join("/", [
      google_container_node_pool.this["database"].node_config[0].machine_type,
      google_container_node_pool.this["database"].node_count,
      google_container_node_pool.this["database"].node_config[0].disk_type,
      google_container_node_pool.this["database"].node_config[0].disk_size_gb,
    ]) == "n2-highmem-8/5/pd-ssd/61"
    error_message = "The database pool should take the database_* settings."
  }
  assert {
    condition = join("/", [
      google_container_node_pool.this["services"].node_config[0].machine_type,
      google_container_node_pool.this["services"].node_count,
      google_container_node_pool.this["services"].node_config[0].disk_type,
      google_container_node_pool.this["services"].node_config[0].disk_size_gb,
    ]) == "n2-highcpu-8/4/pd-standard/41"
    error_message = "The services pool should take the services_* settings."
  }
  assert {
    condition = join("/", [
      google_container_node_pool.this["loadgen"].node_config[0].machine_type,
      google_container_node_pool.this["loadgen"].node_count,
      google_container_node_pool.this["loadgen"].node_config[0].disk_type,
      google_container_node_pool.this["loadgen"].node_config[0].disk_size_gb,
    ]) == "e2-standard-2/2/pd-balanced/31"
    error_message = "The loadgen pool should take the loadgen_* settings."
  }
}

run "the_database_pool_cannot_be_empty" {
  command = plan

  variables {
    database_node_count = 0
  }

  expect_failures = [var.database_node_count]
}

run "the_services_pool_cannot_be_empty" {
  command = plan

  variables {
    services_node_count = 0
  }

  expect_failures = [var.services_node_count]
}

# The counterweight to the two refusals above: a one-node pool is accepted.
run "one_node_pools_are_accepted" {
  command = plan

  variables {
    database_node_count = 1
    services_node_count = 1
  }

  assert {
    condition     = google_container_node_pool.this["database"].node_count == 1 && google_container_node_pool.this["services"].node_count == 1
    error_message = "One-node database and services pools should be accepted."
  }
}

# A node count is a whole number: a fraction is refused, not rounded by the provider.
run "the_database_pool_size_is_a_whole_number" {
  command = plan

  variables {
    database_node_count = 2.5
  }

  expect_failures = [var.database_node_count]
}

run "the_services_pool_size_is_a_whole_number" {
  command = plan

  variables {
    services_node_count = 2.5
  }

  expect_failures = [var.services_node_count]
}

run "the_loadgen_pool_size_is_a_whole_number" {
  command = plan

  variables {
    loadgen_node_count = 1.5
  }

  expect_failures = [var.loadgen_node_count]
}
