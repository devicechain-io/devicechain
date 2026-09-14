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
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.27"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.13"
    }
    # Generates the NATS server TLS CA + leaf declaratively (ADR-025). NATS lives
    # in the shared infra namespace while services live in per-instance ones, so a
    # cert-manager Certificate's namespace-local Secret can't reach the services;
    # a TF-generated cert exports its CA as a plain output the bring-up threads
    # into the per-instance config. cert-manager stays scoped to the ingress cert.
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
  }
}
