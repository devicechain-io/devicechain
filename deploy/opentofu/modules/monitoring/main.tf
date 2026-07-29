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

variable "cnpg_cluster_metrics" {
  description = <<-EOT
    Teach kube-state-metrics to export the CloudNativePG Cluster's own status
    conditions, as `kube_cnpg_cluster_condition{cluster,namespace,type,reason}`.

    🔑 WHY THIS IS WORTH A CUSTOM RESOURCE READER. A Cluster the operator cannot
    reconcile is NEVER PROMOTED — losing the primary leaves healthy, fully
    caught-up replicas that nothing elects — and that state is invisible to every
    other series this platform collects. The instance pods stay up, so
    `cnpg_collector_up` stays 1; replication and WAL archiving stay clean. Being
    un-promotable is a property of the CONTROLLER, and the only place it is
    written down is the Cluster resource's own `Ready` condition.

    The obvious alternative — counting the operator's
    `controller_runtime_reconcile_errors_total` — was built, drilled on the HA rig
    and REJECTED, because a counter is the wrong shape for the question twice
    over. controller-runtime retries a failing item on an exponential backoff
    capped at 1000s, so a sustained outage produces roughly 3.6 errors an hour and
    goes quiet between them; while REPAIRING the control plane emits a burst of
    its own (measured: 4 errors in 11 seconds as the operator caught up). The two
    states overlap in count, so no threshold separates them, and any window long
    enough to span the backoff also holds a benign burst true for that whole
    window. Worse, those series exist only on the operator pod holding the leader
    lease, so a leadership change — exactly what a node loss causes — either
    resets the alert's timer or splits it in two.

    A condition is LEVEL-triggered: true while the thing is true, per Cluster,
    with no `pod` in its identity and nothing to time out. It says what an
    operator actually wants to know instead of a proxy for it.

    Cardinality is negligible: one series per Cluster per condition type, five
    conditions, two Clusters on a stock instance.

    🔴 SET THIS FROM WHETHER CLOUDNATIVEPG IS INSTALLED, not from taste — the root
    passes enable_cnpg. With no CNPG CRDs in the cluster, kube-state-metrics has
    nothing to watch: it does not fail, but it exports no series, and the alerting
    rules that read them would be rules with no series, which is the one failure
    mode those rules cannot self-report.

    Ordering note: this only tells kube-state-metrics which GVK to WATCH, so
    unlike a PodMonitor it creates no apply-time dependency on the CRD existing.
    kube-state-metrics discovers CRDs dynamically (the subchart grants it
    customresourcedefinitions list/watch as soon as this is on), so the usual
    install race — monitoring and CloudNativePG applying in parallel — resolves
    itself once the CRD lands rather than failing the apply.
  EOT
  type        = bool
  default     = false
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
  description = "Grafana admin password — the break-glass native login that stays available alongside OAuth SSO. Sensitive."
  type        = string
  default     = "devicechain"
  sensitive   = true
}

# --- Grafana SSO (ADR-047): OAuth against user-management, operator-tier only ------

variable "grafana_oauth_enabled" {
  description = <<-EOT
    Turn on Grafana OAuth SSO against DeviceChain's user-management OAuth 2.1 AS
    (ADR-047). When true, Grafana is served under a subpath (grafana_root_url +
    serve_from_sub_path) with its own ingress, and login is gated to the
    operator/superuser tier ONLY via role_attribute_path — metrics are instance-level
    + cross-tenant, so a tenant user must never reach them. Requires the OAuth vars
    below. Native admin login stays available as break-glass.
  EOT
  type        = bool
  default     = false
}

variable "grafana_oauth_client_id" {
  description = "OAuth client_id Grafana authenticates as (the confidential client seeded in user-management)."
  type        = string
  default     = "grafana"
}

variable "grafana_oauth_client_secret" {
  description = "Cleartext OAuth client secret for Grafana (its bcrypt hash is what user-management stores). Minted by the bring-up. Sensitive."
  type        = string
  default     = ""
  sensitive   = true
}

variable "grafana_oauth_auth_url" {
  description = "Browser-facing authorize endpoint, e.g. https://<host>/api/user-management/oauth/authorize."
  type        = string
  default     = ""
}

variable "grafana_oauth_token_url" {
  description = "Server-side token endpoint Grafana's pod calls — an IN-CLUSTER service URL (Grafana cannot reach the public ingress host), e.g. http://user-management.<instance>:8080/oauth/token."
  type        = string
  default     = ""
}

variable "grafana_oauth_api_url" {
  description = "Server-side userinfo endpoint Grafana's pod calls — an in-cluster service URL, e.g. http://user-management.<instance>:8080/oauth/userinfo."
  type        = string
  default     = ""
}

variable "grafana_root_url" {
  description = "External root URL Grafana is served at, e.g. https://<host>/grafana. Sets grafana.ini server.root_url + serve_from_sub_path and the OAuth redirect base."
  type        = string
  default     = ""
}

variable "grafana_ingress_host" {
  description = "Host the /grafana ingress answers on (the same host as the app ingress). Empty leaves Grafana ClusterIP + port-forward only."
  type        = string
  default     = ""
}

variable "ingress_class" {
  description = "IngressClass for the /grafana ingress (match the app's, e.g. nginx)."
  type        = string
  default     = "nginx"
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

  # Grafana SSO (ADR-047): when enabled, serve Grafana under a subpath and gate login
  # to the operator/superuser tier via the `sudo` userinfo claim. role_attribute_strict
  # DENIES any user the path maps to an empty role, so a non-operator who completes the
  # OAuth flow is refused (never silently defaulted to Viewer on cross-tenant metrics).
  grafana_ini = var.grafana_oauth_enabled ? {
    server = {
      root_url            = var.grafana_root_url
      serve_from_sub_path = true
    }
    "auth.generic_oauth" = {
      enabled   = true
      name      = "DeviceChain"
      client_id = var.grafana_oauth_client_id
      # client_secret is NOT set here: the grafana subchart's assertNoLeakedSecrets
      # rejects a literal secret in grafana.ini (which renders into a ConfigMap), so
      # it is injected as GF_AUTH_GENERIC_OAUTH_CLIENT_SECRET via envRenderSecret (a
      # real k8s Secret) below — Grafana env overrides the ini.
      scopes    = "read-only"
      auth_url  = var.grafana_oauth_auth_url
      token_url = var.grafana_oauth_token_url
      api_url   = var.grafana_oauth_api_url
      use_pkce  = true
      # `sudo` is the operator/superuser-tier claim userinfo returns. Map it to Admin;
      # everyone else to an empty role which, with strict mode, is denied login.
      role_attribute_path   = "sudo && 'Admin' || ''"
      role_attribute_strict = true
      login_attribute_path  = "preferred_username"
      email_attribute_path  = "email"
    }
  } : {}

  # The /grafana ingress (grafana subchart) lives in the monitoring namespace — the
  # app ingress is in the instance namespace and cannot cross-namespace route to
  # Grafana's Service. serve_from_sub_path (above) means the path is NOT stripped.
  # Built unconditionally (consistent HCL type) and included at the merge below only
  # when enabled + a host is set.
  # The /grafana ingress (grafana subchart) lives in the monitoring namespace — the
  # app ingress is in the instance namespace and cannot cross-namespace route to
  # Grafana's Service. serve_from_sub_path (above) means the path is NOT stripped.
  # No `tls` block: the app ingress already terminates TLS for this shared host, and
  # nginx serves every ingress on the host over that cert (SNI). Declaring a second
  # tls entry here that names a non-existent secret would make host-cert resolution
  # order-dependent and could degrade the console's TLS. (A dedicated monitoring-ns
  # cert-manager Issuer is a possible future refinement.) Built unconditionally
  # (consistent HCL type); included at the merge only when enabled + a host is set.
  grafana_ingress_enabled = var.grafana_oauth_enabled && var.grafana_ingress_host != ""
  grafana_ingress = {
    enabled          = true
    ingressClassName = var.ingress_class
    hosts            = [var.grafana_ingress_host]
    path             = "/grafana"
    pathType         = "Prefix"
  }

  grafana_values = merge({
    # Native admin login stays available as break-glass alongside OAuth SSO.
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
    },
    var.grafana_oauth_enabled ? { "grafana.ini" = local.grafana_ini } : {},
    # The OAuth client secret as a rendered k8s Secret → env (not grafana.ini). Grafana
    # maps GF_AUTH_GENERIC_OAUTH_CLIENT_SECRET onto auth.generic_oauth.client_secret.
    var.grafana_oauth_enabled ? {
      envRenderSecret = { GF_AUTH_GENERIC_OAUTH_CLIENT_SECRET = var.grafana_oauth_client_secret }
    } : {},
    local.grafana_ingress_enabled ? { ingress = local.grafana_ingress } : {},
  )

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

    grafana = local.grafana_values
  }, local.kind_component_overrides, local.cnpg_cluster_metrics_values)
}

locals {
  # kube-state-metrics' CustomResourceState reader, pointed at the CloudNativePG
  # Cluster's status conditions. See var.cnpg_cluster_metrics for WHY this is read
  # from the resource rather than inferred from the operator's own counters.
  #
  # The key is quoted because it contains hyphens, and it is the SUBCHART's values
  # block: kube-prometheus-stack does not enumerate `customResourceState` in its
  # own values.yaml, but Helm passes any subchart key straight through. Verified by
  # rendering chart 65.1.1 with exactly this document — it produces the ConfigMap,
  # the --custom-resource-state-config-file argument, the volume mount and the
  # ClusterRole rule.
  #
  # 🔑 ONLY the postgresql.cnpg.io rule is listed. The subchart adds
  # customresourcedefinitions list/watch BY ITSELF whenever customResourceState is
  # enabled, and adding it here as well renders a duplicate rule. That is easy to
  # get wrong in the other direction: kube-state-metrics needs CRD discovery to
  # resolve the GVK at all, and without it the only symptom is a `forbidden` line
  # in its log and an endpoint that silently exports nothing.
  cnpg_cluster_metrics_values = var.cnpg_cluster_metrics ? {
    "kube-state-metrics" = {
      rbac = {
        extraRules = [{
          apiGroups = ["postgresql.cnpg.io"]
          resources = ["clusters"]
          verbs     = ["get", "list", "watch"]
        }]
      }
      customResourceState = {
        enabled = true
        config = {
          kind = "CustomResourceStateMetrics"
          spec = {
            resources = [{
              groupVersionKind = {
                group   = "postgresql.cnpg.io"
                kind    = "Cluster"
                version = "v1"
              }
              # Yields kube_cnpg_cluster_condition. The prefix and the metric name
              # are joined with an underscore by kube-state-metrics.
              metricNamePrefix = "kube_cnpg"
              labelsFromPath = {
                namespace = ["metadata", "namespace"]
                cluster   = ["metadata", "name"]
              }
              metrics = [{
                name = "cluster_condition"
                help = "CloudNativePG Cluster status conditions, one series per condition type."
                each = {
                  type = "Gauge"
                  gauge = {
                    path = ["status", "conditions"]
                    labelsFromPath = {
                      type   = ["type"]
                      reason = ["reason"]
                    }
                    # The conditions' `status` is the string "True"/"False";
                    # kube-state-metrics maps those onto 1/0. Confirmed live
                    # against both a healthy Cluster (Ready=1) and one the
                    # operator could not reconcile (Ready=0, reason
                    # ClusterIsNotReady).
                    valueFrom = ["status"]
                  }
                }
              }]
            }]
          }
        }
      }
    }
  } : {}
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
