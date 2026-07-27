# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# CloudNativePG — the operator that owns BOTH database stores (ADR-020 A2).
#
# This module installs the operator and, optionally, the Barman Cloud backup
# plugin. It deliberately creates NO `Cluster` resources: those arrive in A2.3
# (`rdb`) and A2.4 (`tsdb`), and they must be created by a Helm release rather
# than a `kubernetes_manifest`, because a custom resource in this root would have
# to exist at PLAN time — the same trap the cert-manager module already documents
# for the ingress Issuer. Installing the operator on its own is therefore a whole
# slice: it puts the CRDs in the cluster so a later apply can use them.
#
# WHY IT IS INSTALLED EVERYWHERE, not only for HA (ADR-020 A2, decision D4):
# backup is not an HA feature. A single-instance install needs point-in-time
# recovery MORE than a replicated one, and if CNPG were the HA-only path then
# `hack/dr-rig.sh` would be drilling a restore that non-HA production never
# executes — an instrument measuring a configuration nobody runs. So the storage
# tier has ONE shape, and HA is the `instances` count on it.
#
# At `instances: 1` this costs nothing on the query path: the operator is a
# controller, not a proxy, and with no replica `synchronous_standby_names` is
# empty, so commits are the same local fsync the StatefulSet does today.
#
# VERSIONS ARE PINNED BY DEFAULT, unlike the nats/cert-manager/monitoring modules
# which default to "latest". Two reasons, and both are specific rather than
# stylistic:
#
#   - The plugin is 0.x. It is the component that stands between us and our
#     backups, and a 0.x minor is entitled to break its API.
#   - The operand image question (A2.6) is decided against a KNOWN operator
#     version. CNPG 1.30.0 is the version the A2 spike was run on, and the spike
#     is the only evidence we have that the DNS contract, the failover timing and
#     the synchronous semantics behave as designed. "Latest" would silently
#     invalidate that evidence.
#
# The operator does NOT require cert-manager: it injects its own webhook CA
# bundle, and its chart renders no Issuer or Certificate. The PLUGIN does require
# it — see var.enable_backup_plugin.

variable "namespace" {
  description = "Namespace for the operator and the backup plugin. The plugin's chart expects to sit alongside the operator."
  type        = string
  default     = "cnpg-system"
}

variable "operator_chart_version" {
  description = <<-EOT
    cloudnative-pg chart version. 0.29.0 ships operator appVersion 1.30.0, which
    is the version the A2 spike validated. Empty installs latest, which is
    supported but unpinned — see the module header for why that trades away
    evidence rather than just reproducibility.
  EOT
  type        = string
  default     = "0.29.0"
}

variable "enable_backup_plugin" {
  description = <<-EOT
    Install the Barman Cloud CNPG-I plugin, which is what provides WAL archiving,
    scheduled backups and PITR (ADR-028).

    🔴 REQUIRES cert-manager, and requires it to be READY, not merely installing:
    the plugin's chart renders an Issuer plus two Certificates, so against a
    cluster with no cert-manager CRDs the release fails outright. That is the
    LOUD failure. The quiet one is worse and is why this is a hard prerequisite
    rather than a best-effort: a plugin whose certificates never issue leaves a
    cluster that looks healthy and is not being backed up.

    dcctl keeps the two flags consistent (bootstrap/tofu.go): the one path that
    drops cert-manager — `--compact --no-tls` — also turns this off, so compact
    gets the operator without PITR rather than a failed apply.
  EOT
  type        = bool
  default     = true
}

variable "plugin_chart_version" {
  description = "plugin-barman-cloud chart version. 0.7.0 ships plugin appVersion v0.13.0. The chart supports only the latest point release of the plugin, so this and the plugin move together."
  type        = string
  default     = "0.7.0"
}

variable "enable_pod_monitor" {
  description = "Create the operator's PodMonitor. Requires the Prometheus Operator CRDs, so this is gated on the monitoring stack being installed — turning it on without them fails the release."
  type        = bool
  default     = false
}

variable "operator_resources" {
  description = "Resource requests for the operator pod. Requests only, no limits: a CPU limit on a controller turns reconcile latency into throttling, and a memory limit turns a large-cluster resync into an OOMKill. Both failure modes present as an operator that has stopped noticing things."
  type = object({
    cpu    = string
    memory = string
  })
  default = {
    cpu    = "100m"
    memory = "128Mi"
  }
}

resource "helm_release" "cnpg" {
  name             = "cnpg"
  namespace        = var.namespace
  create_namespace = true
  repository       = "https://cloudnative-pg.github.io/charts"
  chart            = "cloudnative-pg"
  version          = var.operator_chart_version != "" ? var.operator_chart_version : null

  # The CRDs are the deliverable of this slice — a later apply creates Cluster
  # resources against them. The chart installs them by default; state it, so
  # turning them off is a visible edit rather than an inherited default.
  set {
    name  = "crds.create"
    value = "true"
  }

  set {
    name  = "monitoring.podMonitorEnabled"
    value = var.enable_pod_monitor ? "true" : "false"
  }

  set {
    name  = "resources.requests.cpu"
    value = var.operator_resources.cpu
  }

  set {
    name  = "resources.requests.memory"
    value = var.operator_resources.memory
  }

  # Default wait=true is doing real work here and should not be turned off: the
  # next slice creates Cluster resources, and it can only do that once the
  # operator Deployment is Ready — which it cannot become without its CRDs.
  timeout = 600
}

resource "helm_release" "barman_plugin" {
  count = var.enable_backup_plugin ? 1 : 0

  name       = "plugin-barman-cloud"
  namespace  = var.namespace
  repository = "https://cloudnative-pg.github.io/charts"
  chart      = "plugin-barman-cloud"
  version    = var.plugin_chart_version != "" ? var.plugin_chart_version : null

  # The plugin talks to the operator over CNPG-I, so the operator must exist
  # first. This is ordering, not just readiness — the namespace is created by the
  # release above (create_namespace), and this release does not create it.
  depends_on = [helm_release.cnpg]

  timeout = 600
}

output "namespace" {
  description = "Namespace the operator runs in."
  value       = var.namespace
}

output "backup_plugin_enabled" {
  description = "Whether PITR is actually available. Read this rather than re-deriving it: an install with the operator but no plugin has HA and NO backups, and the difference is invisible from the Cluster resources alone."
  value       = var.enable_backup_plugin
}
