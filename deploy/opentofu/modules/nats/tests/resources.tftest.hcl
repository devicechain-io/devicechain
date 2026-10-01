# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# Each NATS server's requests, memory limit and GOMEMLIMIT reach the chart.
#
# The nats chart has no values schema, so a resources map under the wrong parent
# (podTemplate rather than container) is accepted and renders a server with no
# requests at all: a BestEffort pod, the first evicted under memory pressure. This
# reads the values document Helm is handed, so a key that drifts is a failure here
# rather than a silently unsized broker.
#
# Runs against mocked providers, so no cluster is needed. Run it with
# hack/check-tofu-module-tests.sh, which tests a COPY of the module.

mock_provider "helm" {}
mock_provider "kubernetes" {}

variables {
  namespace   = "dci-test"
  enable_tls  = false
  enable_auth = false
}

run "defaults_reach_the_chart" {
  command = plan

  assert {
    condition = try(yamldecode(helm_release.nats.values[0]).container.resources, null) == {
      requests = { cpu = "500m", memory = "768Mi" }
      limits   = { memory = "2Gi" }
    }
    error_message = "each server must request 500m CPU and 768Mi memory and be limited to 2Gi memory, under container.resources"
  }

  assert {
    condition     = try(yamldecode(helm_release.nats.values[0]).container.env.GOMEMLIMIT, null) == "1638MiB"
    error_message = "GOMEMLIMIT must be 80% of the 2Gi limit, taken in MiB: 1638MiB"
  }

  assert {
    condition     = try(yamldecode(helm_release.nats.values[0]).container.resources.limits.cpu, null) == null
    error_message = "a CPU limit would throttle the process every event crosses; none is set"
  }
}

# The StatefulSet has one pod spec for every server, so ha changes nothing here.
run "ha_does_not_change_them" {
  command = plan

  variables {
    ha = true
  }

  assert {
    condition = try(yamldecode(helm_release.nats.values[0]).container.resources, null) == {
      requests = { cpu = "500m", memory = "768Mi" }
      limits   = { memory = "2Gi" }
    }
    error_message = "ha must leave each server's resources as they are"
  }
}

# What dcctl --compact passes: the requests move, the limit and GOMEMLIMIT do not.
run "compact_requests" {
  command = plan

  variables {
    cpu_request    = "25m"
    memory_request = "64Mi"
  }

  assert {
    condition = try(yamldecode(helm_release.nats.values[0]).container.resources, null) == {
      requests = { cpu = "25m", memory = "64Mi" }
      limits   = { memory = "2Gi" }
    }
    error_message = "lowered requests must reach the chart, and the limit must stay at 2Gi"
  }

  assert {
    condition     = try(yamldecode(helm_release.nats.values[0]).container.env.GOMEMLIMIT, null) == "1638MiB"
    error_message = "GOMEMLIMIT follows the limit, not the request"
  }
}

# A limit in Mi: floor(1536 * 0.8) = 1228.
run "limit_in_mi" {
  command = plan

  variables {
    memory_limit = "1536Mi"
  }

  assert {
    condition     = try(yamldecode(helm_release.nats.values[0]).container.env.GOMEMLIMIT, null) == "1228MiB"
    error_message = "GOMEMLIMIT for a 1536Mi limit must be 1228MiB"
  }
}

# ha_topology is read back out of the values, not re-derived from the variables, so
# what an operator or CI reads from it is what Helm was handed.
run "report_matches_values" {
  command = plan

  variables {
    memory_limit = "1Gi"
  }

  assert {
    condition     = output.ha_topology.resources == yamldecode(helm_release.nats.values[0]).container.resources
    error_message = "ha_topology.resources must be what the chart is handed"
  }

  assert {
    condition     = output.ha_topology.go_mem_limit == "819MiB"
    error_message = "ha_topology.go_mem_limit for a 1Gi limit must be 819MiB"
  }
}

run "request_above_limit_is_refused" {
  command = plan

  variables {
    memory_request = "3Gi"
  }

  expect_failures = [helm_release.nats]
}

run "decimal_unit_is_refused" {
  command = plan

  variables {
    memory_limit = "2G"
  }

  expect_failures = [var.memory_limit]
}

run "zero_request_is_refused" {
  command = plan

  variables {
    cpu_request = "0m"
  }

  expect_failures = [var.cpu_request]
}
