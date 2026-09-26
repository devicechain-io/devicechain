# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# DeviceChain in-cluster infrastructure (ADR-002). OpenTofu owns the data-plane
# dependencies — NATS (JetStream + MQTT), TimescaleDB, and the relational
# Postgres — that the operator and services assume already exist. This root is
# cluster-agnostic: it deploys into an EXISTING cluster via the kubernetes/helm
# providers (kubeconfig-supplied), so it runs the same on kind, k3s, EKS, or GKE.
# A cloud-specific root that provisions the cluster itself can wrap these modules.

terraform {
  required_version = ">= 1.6"

  required_providers {
    # 🔴 EXACT PINS, AND THE kubernetes ONE MAY NOT GO BELOW 3.2.1.
    #
    # Provider 2.38.0 (the last 2.x) stores a resource whose create failed AFTER the
    # object existed -- the object store Deployment timing out on its rollout, or an
    # install interrupted during it -- with a resource identity whose every field is
    # null, and its plugin SDK then rejects that stored identity on the next refresh
    # as an "Unexpected Identity Change". The tainted Deployment is never replaced
    # and every re-run fails identically. 3.2.1 ships the SDK fix
    # (terraform-plugin-sdk 2.38.2): an all-null stored identity reads as absent, and
    # a failed create no longer writes one. Only CLIs that store resource identity
    # show the bug (Terraform >= 1.12); OpenTofu 1.9 cannot.
    #
    # Exact rather than a range because dcctl initialises with -upgrade (see
    # tofuExec.Init): the version written here IS the version every machine runs,
    # existing clusters included, and a range would let a routine re-run move a
    # provider nobody chose. Nothing watches these pins -- Dependabot has no entry
    # for this tree -- so bump them by hand, and run hack/tofu-rerun-rig.sh when the
    # kubernetes one moves. Held by TestShippedRootsPinEveryProviderExactly.
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "3.2.1"
    }
    # 2.17.0 is the last 2.x. 3.x rewrites the provider block (`kubernetes = {}`
    # instead of a nested block, see providers.tf), which is a change of its own.
    helm = {
      source  = "hashicorp/helm"
      version = "2.17.0"
    }
    # No tls provider: nothing in this tree uses one since dcctl mints the NATS CA
    # and leaf itself (see modules/nats), and an unused provider under -upgrade
    # would still be downloaded on every run.
  }
}
