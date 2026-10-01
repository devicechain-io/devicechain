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
# 🔴 Every node is written in the shape the provider RETURNS, not the shape a
# manifest is written in. kubernetes_resources types a Node from the API schema,
# so a field the cluster did not set is present and null. Read through the
# pinned kubernetes provider (3.2.1) from a Kubernetes 1.36 node, a plain node's
# spec came back as
#
#   { configSource = {...}, externalID = null, podCIDR = "10.244.0.0/24",
#     podCIDRs = [...], providerID = "kind://...", taints = null, unschedulable = null }
#
# and, once tainted and cordoned, with unschedulable = true and taints such as
#
#   { key = "dedicated", value = "database", effect = "NoSchedule", timeAdded = null }
#   { key = "novalue", value = null, effect = "NoExecute", timeAdded = null }
#
# The nodes below carry the fields the counting rule reads -- unschedulable,
# taints, and each taint's key, value and effect, with timeAdded beside them as
# the provider returns it -- in exactly that shape; the spec fields nothing
# reads are left out. `spec = {}`, with the fields absent, is a shape the
# provider never produces, and the counting rule once passed every run written
# that way and failed the first install on a real cluster. A run that departs
# from the provider's shape says so and why.
#
# One limit of the mock: within a run, every node's taints are either all null
# or all lists. The provider's list is typed, so a real read that mixes a
# tainted node with an untainted one converts cleanly (checked against a live
# cluster), but a mock value is untyped, and an untyped null beside a list in
# the node list fails the module's conversion of it with "Inconsistent
# conditional result types" -- an artefact of the mock, not the module.
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
        { metadata = { name = "db-a" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoSchedule", timeAdded = null }] } },
        { metadata = { name = "db-b" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoSchedule", timeAdded = null }] } },
        { metadata = { name = "db-c" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoSchedule", timeAdded = null }] } },
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
    condition     = jsonencode(local.placement_usable) == jsonencode(["db-a", "db-b", "db-c"])
    error_message = "three tolerated nodes the cluster reports as never cordoned (unschedulable null) did not all count as usable"
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
        { metadata = { name = "db-a" }, spec = { unschedulable = null, taints = null } },
        { metadata = { name = "db-b" }, spec = { unschedulable = null, taints = null } },
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
        { metadata = { name = "db-a" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoSchedule", timeAdded = null }] } },
        { metadata = { name = "db-b" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoSchedule", timeAdded = null }] } },
        { metadata = { name = "db-c" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoSchedule", timeAdded = null }] } },
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
        { metadata = { name = "db-a" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoSchedule", timeAdded = null }] } },
        { metadata = { name = "db-b" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoSchedule", timeAdded = null }] } },
        { metadata = { name = "db-c" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoSchedule", timeAdded = null }] } },
      ]
    }
  }

  expect_failures = [helm_release.cluster]
}

# A toleration for another key does not cover the taint, even when its value and
# effect match: refused.
run "other_key_is_not_tolerated" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
    tolerations   = [{ key = "other", value = "database", effect = "NoSchedule" }]
  }

  override_data {
    target = data.kubernetes_resources.placement_nodes
    values = {
      objects = [
        { metadata = { name = "db-a" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoSchedule", timeAdded = null }] } },
        { metadata = { name = "db-b" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoSchedule", timeAdded = null }] } },
        { metadata = { name = "db-c" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoSchedule", timeAdded = null }] } },
      ]
    }
  }

  expect_failures = [helm_release.cluster]
}

# A toleration that names an effect covers only that effect: one for NoExecute,
# with the right key and value, does not cover a NoSchedule taint. Refused.
run "other_effect_is_not_tolerated" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
    tolerations   = [{ key = "dedicated", value = "database", effect = "NoExecute" }]
  }

  override_data {
    target = data.kubernetes_resources.placement_nodes
    values = {
      objects = [
        { metadata = { name = "db-a" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoSchedule", timeAdded = null }] } },
        { metadata = { name = "db-b" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoSchedule", timeAdded = null }] } },
        { metadata = { name = "db-c" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoSchedule", timeAdded = null }] } },
      ]
    }
  }

  expect_failures = [helm_release.cluster]
}

# A NoExecute taint keeps a pod off as surely as NoSchedule does: the nodes carry
# one and nothing tolerates it. Refused.
run "untolerated_no_execute_taint" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
  }

  override_data {
    target = data.kubernetes_resources.placement_nodes
    values = {
      objects = [
        { metadata = { name = "db-a" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoExecute", timeAdded = null }] } },
        { metadata = { name = "db-b" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoExecute", timeAdded = null }] } },
        { metadata = { name = "db-c" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "NoExecute", timeAdded = null }] } },
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
        { metadata = { name = "db-a" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "x", effect = "NoSchedule", timeAdded = null }] } },
        { metadata = { name = "db-b" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "y", effect = "NoExecute", timeAdded = null }] } },
        { metadata = { name = "db-c" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = null, effect = "NoSchedule", timeAdded = null }] } },
      ]
    }
  }

  assert {
    condition     = jsonencode(local.placement_usable) == jsonencode(["db-a", "db-b", "db-c"])
    error_message = "an Exists toleration with no effect did not cover the taint on every node"
  }
}

# A PreferNoSchedule taint keeps no pod off, so an untolerated one still counts:
# every node carries one and nothing tolerates it, and all three count.
run "prefer_no_schedule_does_not_exclude" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
  }

  override_data {
    target = data.kubernetes_resources.placement_nodes
    values = {
      objects = [
        { metadata = { name = "db-a" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "PreferNoSchedule", timeAdded = null }] } },
        { metadata = { name = "db-b" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = "database", effect = "PreferNoSchedule", timeAdded = null }] } },
        { metadata = { name = "db-c" }, spec = { unschedulable = null, taints = [{ key = "dedicated", value = null, effect = "PreferNoSchedule", timeAdded = null }] } },
      ]
    }
  }

  assert {
    condition     = jsonencode(local.placement_usable) == jsonencode(["db-a", "db-b", "db-c"])
    error_message = "a PreferNoSchedule taint kept a node from counting"
  }
}

# A cordoned node cannot take an instance: three labelled, one cordoned, refused.
#
# The cordoned node's taints are null here although the cluster soon adds a
# node.kubernetes.io/unschedulable taint to a cordoned node: until it does, the
# cordon is the only thing keeping a pod off, and leaving the taint out makes
# this run depend on the unschedulable check alone.
run "cordoned_node_does_not_count" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
  }

  override_data {
    target = data.kubernetes_resources.placement_nodes
    values = {
      objects = [
        { metadata = { name = "db-a" }, spec = { unschedulable = true, taints = null } },
        { metadata = { name = "db-b" }, spec = { unschedulable = null, taints = null } },
        { metadata = { name = "db-c" }, spec = { unschedulable = null, taints = null } },
      ]
    }
  }

  expect_failures = [helm_release.cluster]
}

# The refusal above cannot say which nodes were counted, so this is the same
# shape by value: with two instances the plan goes through, and the cordoned
# node is the one left out.
run "cordoned_node_is_left_out_by_value" {
  command = plan

  variables {
    instances     = 2
    node_selector = { "devicechain.io/pool" = "database" }
  }

  override_data {
    target = data.kubernetes_resources.placement_nodes
    values = {
      objects = [
        { metadata = { name = "db-a" }, spec = { unschedulable = true, taints = null } },
        { metadata = { name = "db-b" }, spec = { unschedulable = null, taints = null } },
        { metadata = { name = "db-c" }, spec = { unschedulable = null, taints = null } },
      ]
    }
  }

  assert {
    condition     = jsonencode(local.placement_usable) == jsonencode(["db-b", "db-c"])
    error_message = "the cordoned node was counted, or a node never cordoned was not"
  }
}

# An untainted node reports spec.taints as null. unschedulable is false here,
# which the cluster never reports (it leaves the field out, so it reads back
# null): that keeps this run about the taints alone. Three usable nodes.
run "untainted_node_reports_taints_as_null" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
  }

  override_data {
    target = data.kubernetes_resources.placement_nodes
    values = {
      objects = [
        { metadata = { name = "db-a" }, spec = { unschedulable = false, taints = null } },
        { metadata = { name = "db-b" }, spec = { unschedulable = false, taints = null } },
        { metadata = { name = "db-c" }, spec = { unschedulable = false, taints = null } },
      ]
    }
  }

  assert {
    condition     = jsonencode(local.placement_usable) == jsonencode(["db-a", "db-b", "db-c"])
    error_message = "untainted nodes (taints null) did not all count as usable"
  }
}

# A label-only placement on an ordinary node pool, nodes as the cluster reports
# them: never cordoned, no taint, both fields null. Three usable nodes, and the
# selector reaches the chart with no toleration beside it.
run "label_only_placement_on_plain_nodes" {
  command = plan

  variables {
    node_selector = { "devicechain.io/pool" = "database" }
  }

  override_data {
    target = data.kubernetes_resources.placement_nodes
    values = {
      objects = [
        { metadata = { name = "db-a" }, spec = { unschedulable = null, taints = null } },
        { metadata = { name = "db-b" }, spec = { unschedulable = null, taints = null } },
        { metadata = { name = "db-c" }, spec = { unschedulable = null, taints = null } },
      ]
    }
  }

  assert {
    condition     = jsonencode(local.placement_usable) == jsonencode(["db-a", "db-b", "db-c"])
    error_message = "plain nodes, as the cluster reports them, did not all count as usable"
  }
  assert {
    condition     = yamldecode(helm_release.cluster.values[0]).nodeSelector == { "devicechain.io/pool" = "database" }
    error_message = "a label-only placement did not reach the chart"
  }
  assert {
    condition     = length(yamldecode(helm_release.cluster.values[0]).tolerations) == 0
    error_message = "a label-only placement handed the chart a toleration"
  }
}

# Defensive, not a shape the provider returns (a Node always has a spec): a
# node with no spec at all still counts, rather than failing the plan. This is
# what keeps the try() around each field read.
run "node_without_spec_still_counts" {
  command = plan

  variables {
    instances     = 1
    node_selector = { "devicechain.io/pool" = "database" }
  }

  override_data {
    target = data.kubernetes_resources.placement_nodes
    values = {
      objects = [{ metadata = { name = "db-a" }, spec = null }]
    }
  }

  assert {
    condition     = jsonencode(local.placement_usable) == jsonencode(["db-a"])
    error_message = "a node with no spec did not count as usable"
  }
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
      objects = [{ metadata = { name = "db-a" }, spec = { unschedulable = null, taints = null } }]
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
