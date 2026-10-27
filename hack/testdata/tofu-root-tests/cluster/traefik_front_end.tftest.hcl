# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# What the cluster root hands the Traefik module: the replica count derived from HA, and the
# NLB switch. Runs against mocked providers; no cluster is needed. Copied into a throwaway copy of
# the root by hack/check-tofu-module-tests.sh, never run in place.

mock_provider "kubernetes" {
  mock_data "kubernetes_resources" {
    defaults = { objects = [] }
  }
}
mock_provider "helm" {}

variables {
  enable_gateway_api_crds = false
  enable_traefik          = true
  enable_ingress_nginx    = false
}

run "one_replica_by_default" {
  command = plan

  assert {
    condition     = module.traefik[0].replicas == 1
    error_message = "a non-HA cluster runs one Traefik replica"
  }
}

run "ha_runs_two_replicas" {
  command = plan

  variables {
    ha = true
  }

  assert {
    condition     = module.traefik[0].replicas == 2
    error_message = "with a Local Service a cloud balancer targets only nodes running a pod: an HA cluster needs two"
  }
}

run "an_explicit_count_wins_over_ha" {
  command = plan

  variables {
    ha               = true
    traefik_replicas = 3
  }

  assert {
    condition     = module.traefik[0].replicas == 3
    error_message = "traefik_replicas, when set, is the count"
  }
}

run "host_ports_force_one_replica_even_under_ha" {
  command = plan

  variables {
    ha                    = true
    traefik_use_host_port = true
    traefik_service_type  = "NodePort"
  }

  assert {
    condition     = module.traefik[0].replicas == 1
    error_message = "two pods cannot share a node's host ports"
  }
}

run "a_negative_count_is_refused" {
  command = plan

  variables {
    traefik_replicas = -1
  }

  expect_failures = [var.traefik_replicas]
}
