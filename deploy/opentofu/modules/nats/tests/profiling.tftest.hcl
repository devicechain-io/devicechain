# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The broker's run-only profiling port (nats-server prof_port): OFF unless asked for,
# and when on it is a config key only -- never a port the chart or a Service declares.
#
# The nats chart has no values schema, so a key under the wrong parent is accepted and
# does nothing. These read the values document Helm is handed. config.merge is shared
# with broker auth, so the cases cover both together.
#
# Runs against mocked providers, so no cluster is needed. Run it with
# hack/check-tofu-module-tests.sh, which tests a COPY of the module.

mock_provider "helm" {}
mock_provider "kubernetes" {}

variables {
  namespace        = "dci-test"
  server_image_tag = "v0.0.0-test"
  enable_tls       = false
  enable_auth      = false
  ha               = true
}

# OFF BY DEFAULT: no merge at all, so the document is what it was before the variable.
run "off_by_default_renders_no_merge" {
  command = plan

  assert {
    condition     = !can(yamldecode(helm_release.nats.values[0]).config.merge)
    error_message = "with profiling off and auth off, config.merge must be absent"
  }

  assert {
    condition     = !strcontains(helm_release.nats.values[0], "prof_port")
    error_message = "prof_port must not appear in the values when profiling is off"
  }
}

run "on_sets_prof_port_only" {
  command = plan

  variables {
    profiling_port = 6061
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.merge == { prof_port = 6061 }
    error_message = "profiling_port must render config.merge.prof_port and nothing else"
  }
}

run "block_rate_rides_along" {
  command = plan

  variables {
    profiling_port       = 6061
    profiling_block_rate = 10000
  }

  assert {
    condition     = yamldecode(helm_release.nats.values[0]).config.merge == { prof_port = 6061, prof_block_rate = 10000 }
    error_message = "profiling_block_rate must render config.merge.prof_block_rate beside prof_port"
  }
}

# Never a port of the pod: the only declared container port added is the broker metrics one.
run "never_a_declared_port" {
  command = plan

  variables {
    profiling_port = 6061
  }

  assert {
    condition     = !strcontains(jsonencode(yamldecode(helm_release.nats.values[0]).container.patch), "6061")
    error_message = "the profiling port must not be added to the container's declared ports"
  }
}

run "block_rate_without_port_is_refused" {
  command = plan

  variables {
    profiling_block_rate = 10000
  }

  expect_failures = [var.profiling_block_rate]
}

run "port_colliding_with_the_monitor_port_is_refused" {
  command = plan

  variables {
    profiling_port = 8222
  }

  expect_failures = [var.profiling_port]
}
