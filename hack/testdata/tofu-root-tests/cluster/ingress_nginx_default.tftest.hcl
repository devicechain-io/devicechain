# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# ingress-nginx is deprecated and OFF by default: a cluster root applied with no front-end
# settings installs Traefik and no ingress-nginx, and a cluster that wants to keep ingress-nginx
# has to say so. Runs against mocked providers; no cluster is needed. Copied into a throwaway copy
# of the root by hack/check-tofu-module-tests.sh, never run in place.

mock_provider "kubernetes" {
  mock_data "kubernetes_resources" {
    defaults = { objects = [] }
  }
}
mock_provider "helm" {}

variables {
  enable_gateway_api_crds = false
}

run "a_fresh_cluster_gets_traefik_and_no_ingress_nginx" {
  command = plan

  assert {
    condition     = length(module.ingress_nginx) == 0
    error_message = "ingress-nginx is deprecated: a root with no front-end settings must not install it"
  }
  assert {
    condition     = length(module.traefik) == 1
    error_message = "a root with no front-end settings must install Traefik"
  }
}

run "ingress_nginx_is_kept_only_by_asking" {
  command = plan

  variables {
    enable_ingress_nginx = true
  }

  assert {
    condition     = length(module.ingress_nginx) == 1
    error_message = "enable_ingress_nginx = true must install (or keep) ingress-nginx"
  }
}
