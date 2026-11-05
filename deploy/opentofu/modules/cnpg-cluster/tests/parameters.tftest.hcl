# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The Postgres parameters a root passes reach the chart, unchanged.
#
# The event store's wal_compression and the relational store's max_connections
# are set in the roots and travel through `var.parameters` into the values
# document Helm is handed. A Go test in dcctl (walcompression_test.go) reads the
# root's literal and renders the chart with it, so it never crosses this module:
# a `parameters = {}` here, or the key renamed on its way into `base_values`,
# would pass that test while every Cluster rendered without the setting. This
# reads the values document itself.
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

run "parameters_reach_the_chart" {
  command = plan

  variables {
    parameters = {
      "timescaledb.telemetry_level" = "off"
      "wal_compression"             = "lz4"
    }
  }

  assert {
    condition     = yamldecode(helm_release.cluster.values[0]).parameters == { "timescaledb.telemetry_level" = "off", "wal_compression" = "lz4" }
    error_message = "the parameters the root passed did not reach the chart unchanged"
  }
}

# The checkpoint pair is set only when asked for, reaches the chart as parameters, and
# loses to an explicit entry in `parameters`.
run "checkpoint_pair_reaches_the_chart" {
  command = plan

  variables {
    max_wal_size       = "8192MB"
    checkpoint_timeout = "15min"
    parameters = {
      "wal_compression" = "lz4"
    }
  }

  assert {
    condition     = yamldecode(helm_release.cluster.values[0]).parameters == { "max_wal_size" = "8192MB", "checkpoint_timeout" = "15min", "wal_compression" = "lz4" }
    error_message = "max_wal_size and checkpoint_timeout did not reach the chart beside the caller's parameters"
  }
}

run "an_explicit_parameter_wins_over_the_checkpoint_pair" {
  command = plan

  variables {
    max_wal_size = "8192MB"
    parameters = {
      "max_wal_size" = "2048MB"
    }
  }

  assert {
    condition     = yamldecode(helm_release.cluster.values[0]).parameters == { "max_wal_size" = "2048MB" }
    error_message = "the caller's own max_wal_size did not win over the module input"
  }
}
