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
