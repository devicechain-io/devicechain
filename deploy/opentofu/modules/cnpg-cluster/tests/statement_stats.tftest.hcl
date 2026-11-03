# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The run-only statement statistics switch: OFF leaves the values exactly as the caller
# gave them; ON adds pg_stat_statements after what is already preloaded and never
# displaces a caller's own parameter.
#
# Runs against mocked providers, so no cluster is needed. Run it with
# hack/check-tofu-module-tests.sh, which tests a COPY of the module.

mock_provider "helm" {}
mock_provider "kubernetes" {}

variables {
  namespace          = "dc-system"
  name               = "dc-tsdb"
  alias_service_name = "dc-timescaledb-single"
  image              = "ghcr.io/example/postgres:17"
  instances          = 1
  database           = "dc"
  username           = "devicechain"
}

run "statement_stats_off_adds_nothing" {
  command = plan

  variables {
    shared_preload_libraries = ["timescaledb"]
    parameters               = { "wal_compression" = "lz4" }
  }

  assert {
    condition     = yamldecode(helm_release.cluster.values[0]).parameters == { "wal_compression" = "lz4" }
    error_message = "with statement_stats off the parameters must be the caller's alone"
  }

  assert {
    condition     = yamldecode(helm_release.cluster.values[0]).sharedPreloadLibraries == ["timescaledb"]
    error_message = "with statement_stats off the preload list must be the caller's alone"
  }
}

run "statement_stats_on_preloads_after_the_callers_libraries" {
  command = plan

  variables {
    statement_stats          = true
    shared_preload_libraries = ["timescaledb"]
    parameters               = { "wal_compression" = "lz4", "track_io_timing" = "off" }
  }

  assert {
    condition     = yamldecode(helm_release.cluster.values[0]).sharedPreloadLibraries == ["timescaledb", "pg_stat_statements"]
    error_message = "pg_stat_statements must be appended after timescaledb, not placed before it"
  }

  assert {
    condition = yamldecode(helm_release.cluster.values[0]).parameters == {
      "pg_stat_statements.track" = "all"
      "pg_stat_statements.max"   = "10000"
      "track_io_timing"          = "off"
      "wal_compression"          = "lz4"
    }
    error_message = "statement_stats must add its parameters and a caller's own value for the same key must win"
  }
}

run "statement_stats_does_not_duplicate_a_preload" {
  command = plan

  variables {
    statement_stats          = true
    shared_preload_libraries = ["pg_stat_statements"]
  }

  assert {
    condition     = yamldecode(helm_release.cluster.values[0]).sharedPreloadLibraries == ["pg_stat_statements"]
    error_message = "a library the caller already preloads must not be listed twice"
  }
}
