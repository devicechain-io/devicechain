# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# Database placement: the node selector and tolerations reach the chart only when
# they are asked for, and a placement the chosen nodes cannot hold is refused
# before the store is created or changed.
#
# The nodes the placement counts are read from the cluster, so each run that sets
# a selector supplies them with override_data: they are the input the counting
# rule decides on, and a mocked provider would otherwise invent them.
#
# Runs against mocked providers, so no cluster is needed. Run it with
# hack/check-tofu-module-tests.sh, which tests a COPY of the module.

mock_provider "helm" {}
mock_provider "kubernetes" {}

variables {
  namespace          = "dc-system"
  name               = "dc-rdb"
  alias_service_name = "dc-postgresql"
  image              = "ghcr.io/example/postgres:17"
  instances          = 3
  database           = "dc"
  username           = "devicechain"
}

# Nothing asked for: neither value reaches the chart as anything but empty, no
# node is read, and the outputs report nothing.
run "default_places_nothing" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.cluster.values[0]).nodeSelector == {}
    error_message = "an unplaced store was handed a node selector"
  }
  assert {
    condition     = length(yamldecode(helm_release.cluster.values[0]).tolerations) == 0
    error_message = "an unplaced store was handed tolerations"
  }
  assert {
    condition     = length(data.kubernetes_resources.placement_nodes) == 0
    error_message = "an unplaced store read the cluster's nodes"
  }
  assert {
    condition     = length(output.node_selector) == 0 && length(output.tolerations) == 0
    error_message = "an unplaced store reports a placement"
  }
}

# Asked for, and three labelled nodes carrying the taint the toleration covers:
# both values reach the chart exactly, and the outputs read them back.
run "placement_reaches_the_chart" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
    tolerations   = [{ key = "dedicated", value = "database", effect = "NoSchedule" }]
  }

  override_data {
    target = data.kubernetes_resources.placement_nodes
    values = {
      objects = [
        { metadata = { name = "db-a" }, spec = { taints = [{ key = "dedicated", value = "database", effect = "NoSchedule" }] } },
        { metadata = { name = "db-b" }, spec = { taints = [{ key = "dedicated", value = "database", effect = "NoSchedule" }] } },
        { metadata = { name = "db-c" }, spec = { taints = [{ key = "dedicated", value = "database", effect = "NoSchedule" }] } },
      ]
    }
  }

  assert {
    condition     = yamldecode(helm_release.cluster.values[0]).nodeSelector == { "devicechain.io/pool" = "database" }
    error_message = "the node selector did not reach the chart as asked"
  }
  assert {
    condition = jsonencode(yamldecode(helm_release.cluster.values[0]).tolerations) == jsonencode([
      { effect = "NoSchedule", key = "dedicated", operator = "Equal", value = "database" }
    ])
    error_message = "the toleration did not reach the chart as asked"
  }
  assert {
    condition     = length(output.node_selector) == 1 && output.node_selector["devicechain.io/pool"] == "database"
    error_message = "the node_selector output does not report what the chart was handed"
  }
  assert {
    condition     = length(output.tolerations) == 1 && output.tolerations[0].key == "dedicated" && output.tolerations[0].value == "database"
    error_message = "the tolerations output does not report what the chart was handed"
  }
  assert {
    condition     = length(local.placement_usable) == 3
    error_message = "three tolerated, schedulable nodes did not all count as usable"
  }
}

# Three instances, two usable nodes: refused.
run "too_few_usable_nodes" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
  }

  override_data {
    target = data.kubernetes_resources.placement_nodes
    values = {
      objects = [
        { metadata = { name = "db-a" }, spec = {} },
        { metadata = { name = "db-b" }, spec = {} },
      ]
    }
  }

  expect_failures = [helm_release.cluster]
}

# The nodes carry a NoSchedule taint and nothing tolerates it: refused.
run "untolerated_taint" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
  }

  override_data {
    target = data.kubernetes_resources.placement_nodes
    values = {
      objects = [
        { metadata = { name = "db-a" }, spec = { taints = [{ key = "dedicated", value = "database", effect = "NoSchedule" }] } },
        { metadata = { name = "db-b" }, spec = { taints = [{ key = "dedicated", value = "database", effect = "NoSchedule" }] } },
        { metadata = { name = "db-c" }, spec = { taints = [{ key = "dedicated", value = "database", effect = "NoSchedule" }] } },
      ]
    }
  }

  expect_failures = [helm_release.cluster]
}

# An Equal toleration for another value does not cover the taint: refused.
run "wrong_value_is_not_tolerated" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
    tolerations   = [{ key = "dedicated", value = "other", effect = "NoSchedule" }]
  }

  override_data {
    target = data.kubernetes_resources.placement_nodes
    values = {
      objects = [
        { metadata = { name = "db-a" }, spec = { taints = [{ key = "dedicated", value = "database", effect = "NoSchedule" }] } },
        { metadata = { name = "db-b" }, spec = { taints = [{ key = "dedicated", value = "database", effect = "NoSchedule" }] } },
        { metadata = { name = "db-c" }, spec = { taints = [{ key = "dedicated", value = "database", effect = "NoSchedule" }] } },
      ]
    }
  }

  expect_failures = [helm_release.cluster]
}

# Exists tolerates the taint whatever its value, and an empty effect matches every
# effect: three usable nodes.
run "exists_toleration_any_value" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
    tolerations   = [{ key = "dedicated", operator = "Exists" }]
  }

  override_data {
    target = data.kubernetes_resources.placement_nodes
    values = {
      objects = [
        { metadata = { name = "db-a" }, spec = { taints = [{ key = "dedicated", value = "x", effect = "NoSchedule" }] } },
        { metadata = { name = "db-b" }, spec = { taints = [{ key = "dedicated", value = "y", effect = "NoExecute" }] } },
        { metadata = { name = "db-c" }, spec = { taints = [{ key = "dedicated", effect = "NoSchedule" }] } },
      ]
    }
  }

  assert {
    condition     = length(local.placement_usable) == 3
    error_message = "an Exists toleration with no effect did not cover the taint on every node"
  }
}

# A PreferNoSchedule taint keeps no pod off, so an untolerated one still counts.
run "prefer_no_schedule_does_not_exclude" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
  }

  override_data {
    target = data.kubernetes_resources.placement_nodes
    values = {
      objects = [
        { metadata = { name = "db-a" }, spec = { taints = [{ key = "dedicated", value = "database", effect = "PreferNoSchedule" }] } },
        { metadata = { name = "db-b" }, spec = {} },
        { metadata = { name = "db-c" }, spec = {} },
      ]
    }
  }

  assert {
    condition     = length(local.placement_usable) == 3
    error_message = "a PreferNoSchedule taint kept a node from counting"
  }
}

# A cordoned node cannot take an instance: three labelled, one cordoned, refused.
run "cordoned_node_does_not_count" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
  }

  override_data {
    target = data.kubernetes_resources.placement_nodes
    values = {
      objects = [
        { metadata = { name = "db-a" }, spec = { unschedulable = true } },
        { metadata = { name = "db-b" }, spec = {} },
        { metadata = { name = "db-c" }, spec = {} },
      ]
    }
  }

  expect_failures = [helm_release.cluster]
}

# One instance needs one node.
run "single_instance_one_node" {
  command = plan

  variables {
    instances     = 1
    node_selector = { "devicechain.io/pool" = "database" }
  }

  override_data {
    target = data.kubernetes_resources.placement_nodes
    values = {
      objects = [{ metadata = { name = "db-a" }, spec = {} }]
    }
  }

  assert {
    condition     = yamldecode(helm_release.cluster.values[0]).nodeSelector == { "devicechain.io/pool" = "database" }
    error_message = "a single-instance placement did not reach the chart"
  }
}

# A toleration with nothing to place: refused.
run "toleration_without_selector" {
  command = plan

  variables {
    tolerations = [{ key = "dedicated", value = "database", effect = "NoSchedule" }]
  }

  expect_failures = [helm_release.cluster]
}

run "reserved_toleration_key" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
    tolerations   = [{ key = "node.kubernetes.io/unreachable", operator = "Exists", effect = "NoExecute" }]
  }

  expect_failures = [var.tolerations]
}

run "exists_with_value" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
    tolerations   = [{ key = "dedicated", operator = "Exists", value = "database" }]
  }

  expect_failures = [var.tolerations]
}

# `{ key = "dedicated" }` is Equal with no value, which would tolerate only an
# empty-valued taint; refused, as dcctl refuses `dedicated=`.
run "equal_without_value" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
    tolerations   = [{ key = "dedicated", effect = "NoSchedule" }]
  }

  expect_failures = [var.tolerations]
}

run "bad_effect" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
    tolerations   = [{ key = "dedicated", value = "database", effect = "NoScheduel" }]
  }

  expect_failures = [var.tolerations]
}

run "bad_selector_key" {
  command = plan

  variables {
    node_selector = { "-pool" = "database" }
  }

  expect_failures = [var.node_selector]
}
