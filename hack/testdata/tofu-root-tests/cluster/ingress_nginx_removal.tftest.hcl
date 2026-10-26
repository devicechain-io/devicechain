# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The cluster root's refusal to remove ingress-nginx while an Ingress of its class remains.
#
# Dropping the module DESTROYS the controller and leaves every Ingress it served in place and
# unserved, so each host they carry answers nothing. The guard lives in the root so a raw
# `tofu apply` cannot bypass it; this suite is what shows it can fail, and that it fails only
# for the reason it names. Runs against mocked providers; no cluster is needed. It is copied
# into a throwaway copy of the root by hack/check-tofu-module-tests.sh, never run in place.

mock_provider "kubernetes" {
  mock_data "kubernetes_resources" {
    defaults = { objects = [] }
  }
}
mock_provider "helm" {}

variables {
  enable_ingress_nginx = false
}

# A cluster whose ingress-nginx release record is in its namespace, with one Ingress of its class.
override_data {
  target = data.kubernetes_resources.ingress_nginx_release
  values = { objects = [{ metadata = { name = "sh.helm.release.v1.ingress-nginx.v1" } }] }
}

override_data {
  target = data.kubernetes_resources.ingresses_of_the_nginx_class
  values = {
    objects = [
      { metadata = { name = "devicechain-api", namespace = "dci-x", annotations = {} }, spec = { ingressClassName = "nginx" } },
    ]
  }
}

run "removal_is_refused_while_an_nginx_ingress_remains" {
  command         = plan
  expect_failures = [terraform_data.ingress_nginx_removal_guard]
}

run "removal_is_allowed_when_acknowledged" {
  command = plan
  variables {
    allow_ingress_nginx_removal = true
  }
}

run "keeping_the_controller_is_not_a_removal" {
  command = plan
  variables {
    enable_ingress_nginx = true
  }
}

run "the_legacy_class_annotation_counts_as_the_class" {
  command = plan
  override_data {
    target = data.kubernetes_resources.ingresses_of_the_nginx_class
    values = {
      objects = [
        { metadata = { name = "old", namespace = "dci-y", annotations = { "kubernetes.io/ingress.class" = "nginx" } }, spec = {} },
      ]
    }
  }
  expect_failures = [terraform_data.ingress_nginx_removal_guard]
}

run "an_ingress_of_another_class_does_not_block" {
  command = plan
  override_data {
    target = data.kubernetes_resources.ingresses_of_the_nginx_class
    values = {
      objects = [
        { metadata = { name = "x", namespace = "y", annotations = {} }, spec = { ingressClassName = "traefik" } },
      ]
    }
  }
}

run "a_cluster_that_never_had_the_controller_is_not_blocked" {
  command = plan
  override_data {
    target = data.kubernetes_resources.ingress_nginx_release
    values = { objects = [] }
  }
}

run "grafanas_own_ingress_does_not_block_while_this_root_manages_monitoring" {
  command = plan
  override_data {
    target = data.kubernetes_resources.ingresses_of_the_nginx_class
    values = {
      objects = [
        {
          metadata = {
            name        = "grafana"
            namespace   = "monitoring"
            annotations = {}
            labels      = { "app.kubernetes.io/name" = "grafana", "app.kubernetes.io/instance" = "kube-prometheus-stack" }
          }
          spec = { ingressClassName = "nginx" }
        },
      ]
    }
  }
}

run "grafanas_ingress_blocks_when_this_root_does_not_manage_monitoring" {
  command = plan
  variables {
    enable_monitoring = false
  }
  override_data {
    target = data.kubernetes_resources.ingresses_of_the_nginx_class
    values = {
      objects = [
        {
          metadata = {
            name        = "grafana"
            namespace   = "monitoring"
            annotations = {}
            labels      = { "app.kubernetes.io/name" = "grafana", "app.kubernetes.io/instance" = "kube-prometheus-stack" }
          }
          spec = { ingressClassName = "nginx" }
        },
      ]
    }
  }
  expect_failures = [terraform_data.ingress_nginx_removal_guard]
}

run "someone_elses_grafana_named_ingress_blocks" {
  command = plan
  override_data {
    target = data.kubernetes_resources.ingresses_of_the_nginx_class
    values = {
      objects = [
        {
          metadata = {
            name        = "grafana"
            namespace   = "monitoring"
            annotations = {}
            labels      = { "app.kubernetes.io/name" = "grafana", "app.kubernetes.io/instance" = "somebody-elses" }
          }
          spec = { ingressClassName = "nginx" }
        },
      ]
    }
  }
  expect_failures = [terraform_data.ingress_nginx_removal_guard]
}
