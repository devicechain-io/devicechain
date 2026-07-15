# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The observability stack (GA hardening, 2026-07): kube-prometheus-stack —
# the Prometheus Operator (+ its ServiceMonitor/PrometheusRule/Probe CRDs),
# Prometheus, Alertmanager, Grafana (with the dashboard/datasource sidecars),
# node-exporter, and kube-state-metrics. OpenTofu installs the CAPABILITY here,
# ahead of the instance chart, so that:
#   - the CRDs exist before the DeviceChain Helm chart renders its ServiceMonitors
#     / PrometheusRule / dashboard ConfigMaps (metrics.enabled), and
#   - Prometheus + Grafana are actually running for load testing and ops.
# Emission is already broadly instrumented (core.ProcessorMetrics on every
# service, plus per-area metrics + JetStream stream metrics); this module is the
# collection + visualization half.

variable "namespace" {
  type    = string
  default = "monitoring"
}

variable "release_name" {
  type    = string
  default = "kube-prometheus-stack"
}

variable "chart_version" {
  description = <<-EOT
    kube-prometheus-stack chart version. PINNED by default (unlike the other infra
    modules): this chart ships the Prometheus Operator CRDs in its crds/ directory,
    which Helm installs once and never upgrades — so an unpinned "latest" that drifts
    across bootstraps silently skews the operator against install-day CRDs. Bump
    deliberately, and apply the new CRDs by hand per the chart's upgrade notes.
  EOT
  type        = string
  default     = "65.1.1"
}

variable "slim" {
  description = <<-EOT
    Reduce the footprint for a local/kind cluster: Prometheus keeps its TSDB on an
    emptyDir (no PVC to provision/wait on) and requests fewer resources. Leave false
    for real clusters, where Prometheus gets a persistent volume so metrics survive a
    pod restart. The bring-up (dcctl / up.sh) sets this true on a local context.
  EOT
  type        = bool
  default     = false
}

variable "grafana_admin_password" {
  description = "Grafana admin password (auth is native admin for now; OIDC via user-management ADR-047 is a follow-up). Sensitive."
  type        = string
  default     = "devicechain"
  sensitive   = true
}

variable "prometheus_retention" {
  description = "How long Prometheus retains samples (e.g. 15d). Lower it for a slim/local cluster on emptyDir."
  type        = string
  default     = "15d"
}

variable "prometheus_storage" {
  description = "PVC size for Prometheus TSDB when NOT slim. Ignored in slim mode (emptyDir)."
  type        = string
  default     = "20Gi"
}

variable "storage_class" {
  description = "StorageClass for the Prometheus PVC; empty uses the cluster default. Ignored in slim mode."
  type        = string
  default     = ""
}

locals {
  # Persist Prometheus' TSDB on a PVC on real clusters; on a slim/kind cluster keep
  # it on the default emptyDir (nothing to provision, nothing to wait on).
  prometheus_storage = var.slim ? {} : {
    storageSpec = {
      volumeClaimTemplate = {
        spec = merge(
          {
            accessModes = ["ReadWriteOnce"]
            resources   = { requests = { storage = var.prometheus_storage } }
          },
          var.storage_class != "" ? { storageClassName = var.storage_class } : {},
        )
      }
    }
  }

  prometheus_resources = var.slim ? {
    requests = { cpu = "100m", memory = "400Mi" }
    } : {
    requests = { cpu = "250m", memory = "750Mi" }
  }

  # On a slim/kind cluster the TSDB is an emptyDir on the node's disk, so cap it by
  # BOTH time and size — a fortnight of kube/cadvisor/kube-state cardinality (which
  # dwarfs DeviceChain's own series) would otherwise fill the node and trip
  # disk-pressure eviction, which wipes the emptyDir anyway. On a real cluster keep
  # the caller's retention and let the PVC bound the size.
  prometheus_retention = var.slim ? {
    retention     = "2d"
    retentionSize = "5GB"
    } : {
    retention = var.prometheus_retention
  }

  # kind's control-plane components (controller-manager, scheduler, etcd, kube-proxy)
  # bind to 127.0.0.1, so kube-prometheus-stack's default scrape targets for them are
  # permanently Down and their bundled *Down rules alert forever — drowning the
  # DeviceChain alerts. Turn those component scrapes off on a slim/kind cluster.
  kind_component_overrides = var.slim ? {
    kubeControllerManager = { enabled = false }
    kubeScheduler         = { enabled = false }
    kubeEtcd              = { enabled = false }
    kubeProxy             = { enabled = false }
  } : {}

  values = merge({
    # The Operator CRDs are what the DeviceChain instance chart's ServiceMonitors /
    # PrometheusRule depend on — install them with the stack.
    crds = { enabled = true }

    prometheus = {
      prometheusSpec = merge({
        # CRUCIAL: by default kube-prometheus-stack's Prometheus only selects
        # ServiceMonitors/PodMonitors/PrometheusRules carrying its own release label.
        # DeviceChain renders those in the per-INSTANCE namespace with its own labels,
        # so without these the app targets are silently never scraped. Select all.
        # (The namespace dimension is already all-namespaces by chart default.)
        serviceMonitorSelectorNilUsesHelmValues = false
        podMonitorSelectorNilUsesHelmValues     = false
        ruleSelectorNilUsesHelmValues           = false
        probeSelectorNilUsesHelmValues          = false
        resources                               = local.prometheus_resources
      }, local.prometheus_retention, local.prometheus_storage)
    }

    # Alertmanager runs but its DEFAULT null receiver drops alerts — routing them to
    # DeviceChain notifications / a real receiver is a follow-up slice.
    alertmanager = {
      alertmanagerSpec = {
        resources = { requests = { cpu = "50m", memory = "100Mi" } }
      }
    }

    grafana = {
      # Native admin auth for now. FOLLOW-UP: auth.generic_oauth against
      # user-management's OAuth 2.1 AS (ADR-047), gated to the operator/superuser
      # tier only (metrics are instance-level + cross-tenant → never tenant users),
      # so the console's superuser-only "Metrics" link is true SSO. Slots in here as
      # additional grafana.* / grafana.ini keys with no module restructure.
      adminPassword = var.grafana_admin_password
      service       = { type = "ClusterIP" }
      resources     = { requests = { cpu = "50m", memory = "150Mi" } }
      sidecar = {
        dashboards = {
          enabled = true
          label   = "grafana_dashboard"
          # The DeviceChain dashboard ConfigMaps live in the per-instance namespace,
          # not Grafana's — search every namespace so they auto-import.
          searchNamespace = "ALL"
        }
        datasources = { enabled = true }
      }
    }
  }, local.kind_component_overrides)
}

resource "helm_release" "kube_prometheus_stack" {
  name             = var.release_name
  namespace        = var.namespace
  create_namespace = true
  repository       = "https://prometheus-community.github.io/helm-charts"
  chart            = "kube-prometheus-stack"
  version          = var.chart_version != "" ? var.chart_version : null

  # The stack installs many CRDs + several workloads; the default 300s is tight on
  # a cold cluster pulling images.
  timeout = 900

  values = [yamlencode(local.values)]
}

output "namespace" {
  value = var.namespace
}

# Grafana's Service name follows the chart's "<release>-grafana" convention. The
# bring-up prints a port-forward hint to it (ClusterIP — no ingress in this slice).
output "grafana_service" {
  description = "Name of the Grafana ClusterIP Service."
  value       = "${var.release_name}-grafana"
}
