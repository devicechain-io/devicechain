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
  description = <<-EOT
    Create the operator's PodMonitor.

    🔴 OFF, and NOT derived from whether the monitoring stack is installed. That
    derivation was tried and is wrong in both directions, which is why this reads
    as a plain opt-in:

      - It does not order anything. `enable_monitoring` is a variable READ, and a
        variable read creates no dependency edge — `terraform graph` puts this
        module in the first wave while kube-prometheus-stack is still installing,
        so a fresh apply fails nondeterministically on `no matches for kind
        "PodMonitor"`. This root already learned that lesson for NATS; see the
        last bullet of "Notes & scope boundaries" in the root README, which is why
        the NATS PodMonitor is rendered by the Helm chart and not here.
      - It gets the other case backwards too. `enable_monitoring=false` documents
        "the cluster ALREADY has the Prometheus Operator", which is exactly a
        cluster where the CRDs exist and scraping would work — so deriving from it
        silently leaves the operator unscraped precisely where it could be
        scraped.

    Turn it on deliberately, against a cluster already carrying the Prometheus
    Operator CRDs.

    🔑 A DEVICECHAIN INSTANCE NO LONGER NEEDS THIS, and it is still not derived.
    The Helm chart renders its own PodMonitor for the operator
    (templates/podmonitor-cnpg-operator.yaml, gated on metrics.cnpgNamespace),
    from the far side of the ordering problem above: the chart installs after
    this apply completes, so the CRDs exist by construction. This flag stays
    because it serves the case the chart cannot — an operator installed by this
    root and monitored by something that is not a DeviceChain instance.

    Note what those series are and are NOT for. Nothing alerts on the operator's
    controller-runtime counters; the control-plane alerts (ADR-020 A1.5) read the
    Cluster's own conditions through kube-state-metrics instead, for reasons set
    out at length in modules/monitoring's cnpg_cluster_metrics. The operator
    scrape is diagnostic — the first thing to look at once one of those alerts
    fires.

    Turning BOTH on is harmless: two PodMonitors select the same pods, so every
    operator series is scraped twice under different `job` labels. Since no rule
    reads them, the duplicate costs storage and makes a confusing graph and
    nothing else. If a rule ever DOES read them, check that first — an
    unaggregated expression would carry `job` into the alert's identity and page
    twice for one event.
  EOT
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

variable "ha" {
  description = <<-EOT
    Run the CNPG OPERATOR at two replicas, spread across nodes.

    🔴 THIS IS A DATABASE-AVAILABILITY SETTING, NOT AN OPERATOR-COMFORT ONE. The
    operator and the Barman plugin both sit in the failover path: CloudNativePG
    cannot reconcile a Cluster whose plugins it cannot reach, and a Cluster that
    cannot reconcile cannot promote a standby. Both ship at replicaCount 1 with
    no tolerations and no anti-affinity.

    Measured, killing the node that held both database primaries AND the plugin:
    failover did not complete for 10m50s, and the operator spent the outage
    logging `connection refused` to barman-cloud.cloudnative-pg.io. Killing a node
    that held both primaries but NOT the plugin, on the same cluster minutes
    later: 1m51s. Same fault, same databases, 5.9x the outage -- the difference
    was whether the control plane happened to be somewhere else.

    The perverse consequence is worth remembering: enabling BACKUPS, a durability
    feature, installs the plugin and thereby lengthens the worst-case failover of
    the database it protects. An instance with backups off has no plugin to lose.

    🔴 THIS DOES NOT REPLICATE THE PLUGIN, AND MUST NOT -- see the plugin release
    below. The plugin's answer to node loss is the TOLERATION, which is applied at
    every posture: reschedule in ~30s instead of ~300s. That is most of the 10m50s
    and it is all that is available without upstream changes.
  EOT
  type        = bool
  default     = false
}

variable "node_loss_toleration_seconds" {
  description = <<-EOT
    How long the operator and plugin pods stay put after their node is tainted
    NotReady or unreachable. null leaves Kubernetes' default, which is 300.

    Unset is not "no toleration" -- the DefaultTolerationSeconds admission plugin
    injects 300 onto any pod that does not tolerate these taints. For the control
    plane that 300 is the window in which no Cluster in the instance can fail
    over, so it is the same knob as on the database pods and wants the same value.
  EOT
  type        = number
  default     = 30

  validation {
    condition     = var.node_loss_toleration_seconds == null || var.node_loss_toleration_seconds >= 0
    error_message = "node_loss_toleration_seconds must be null (Kubernetes' 300s default) or a non-negative number of seconds."
  }
}

locals {
  # 🔴 THE OPERATOR ONLY. The plugin stays at one replica no matter the posture,
  # and that is a correctness constraint rather than a cost decision -- the
  # reasoning is at the plugin release below, where it is actionable.
  operator_replicas = var.ha ? 2 : 1

  control_plane_tolerations = var.node_loss_toleration_seconds == null ? [] : [
    {
      key               = "node.kubernetes.io/not-ready"
      operator          = "Exists"
      effect            = "NoExecute"
      tolerationSeconds = var.node_loss_toleration_seconds
    },
    {
      key               = "node.kubernetes.io/unreachable"
      operator          = "Exists"
      effect            = "NoExecute"
      tolerationSeconds = var.node_loss_toleration_seconds
    },
  ]

  # 🔴 THE labelSelector IS THE LOAD-BEARING PART, AND OMITTING IT IS SILENT.
  #
  # The chart does not inject one -- measured, by rendering with a constraint that
  # had none: it reaches the pod spec exactly as written. And a
  # topologySpreadConstraint with no labelSelector matches NO pods, so the skew is
  # always zero and the constraint never constrains. It is not rejected, it is not
  # warned about, and `kubectl get deploy -o yaml` shows a spread constraint
  # sitting right there in the pod spec.
  #
  # That is the same false-HA shape as a topologyKey no node carries (see
  # modules/cnpg-cluster): the protection is decorative, and every check that
  # counts replicas agrees the operator is spread. Note the asymmetry that makes
  # it easy to get wrong -- for podAffinity an empty selector matches EVERYTHING,
  # so the intuition carried over from there is exactly backwards.
  #
  # 🔑 SELECT ON A LABEL WE OWN, NOT ON THE CHART'S. The first version of this
  # selected on `app.kubernetes.io/name` + `app.kubernetes.io/instance`, which the
  # chart writes -- so the module was selecting on strings it does not control,
  # a chart that renamed either one would silently un-spread the operator, and
  # closing that hole needed a network-dependent CI step rendering the chart to
  # re-check the pairing. The chart exposes `podLabels`, so instead we set our own
  # label and select on that: the two values are now the same expression in the
  # same file, and there is nothing left for an upstream rename to break.
  operator_pod_label = {
    "devicechain.io/cnpg-control-plane" = "operator"
  }

  # 🔴 DoNotSchedule, matching the NATS servers and the database instances. A soft
  # constraint here would be worse than none: two replicas on one node cost twice
  # as much and protect against nothing, while every replica-counting check
  # reports the operator as highly available.
  #
  # Only applied at more than one replica -- at replicaCount 1 there is nothing to
  # spread, and a hard constraint with a single pod can only ever refuse to
  # schedule.
  operator_spread = local.operator_replicas > 1 ? [
    {
      maxSkew           = 1
      topologyKey       = "kubernetes.io/hostname"
      whenUnsatisfiable = "DoNotSchedule"
      labelSelector     = { matchLabels = local.operator_pod_label }
    },
  ] : []

  # 🔴 THE VALUES LIVE IN LOCALS SO THE OUTPUTS CAN READ THEM BACK, exactly as in
  # modules/cnpg-cluster. The failure being guarded is a DELETION: drop
  # `topologySpreadConstraints` from the operator document, or `ha = var.ha` from
  # the root, and both charts fall back to their own defaults — one replica, no
  # spread, no tolerations — with a green apply and no symptom until a node dies.
  # An output that re-derived these from the variables would agree with the
  # request rather than with what Helm was handed, and the assertion would then be
  # checking a copy against itself.
  operator_values = {
    replicaCount              = local.operator_replicas
    tolerations               = local.control_plane_tolerations
    podLabels                 = local.operator_pod_label
    topologySpreadConstraints = local.operator_spread
  }

  plugin_values = {
    replicaCount = 1
    tolerations  = local.control_plane_tolerations
  }
}

resource "helm_release" "cnpg" {
  name             = "cnpg"
  namespace        = var.namespace
  create_namespace = true
  repository       = "https://cloudnative-pg.github.io/charts"
  chart            = "cloudnative-pg"
  version          = var.operator_chart_version != "" ? var.operator_chart_version : null

  # Control-plane availability. A `values` document rather than `set` blocks
  # because tolerations and topologySpreadConstraints are lists of objects, and
  # expressing those through `set`'s index syntax is both unreadable and a
  # well-known source of silently-wrong renders.
  values = [yamlencode(local.operator_values)]

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

  # Default wait=true is doing real work here and should not be turned off: it
  # waits for the operator Deployment to become Ready, which it cannot do without
  # its CRDs, so the Cluster resources in the next slice find their type.
  #
  # 🔴 BUT DEPLOYMENT-READY IS NOT WEBHOOK-SERVING, and an earlier version of this
  # comment claimed the ordering was therefore safe. It is not, and that belief is
  # what produced the bug:
  #
  #   failed calling webhook "mcluster.cnpg.io": dial tcp <clusterIP>:443:
  #   connect: connection refused
  #
  # Helm returns the instant the Deployment reports readyReplicas=1. The webhook's
  # ClusterIP is not routable until the EndpointSlice is written AND every relevant
  # kube-proxy has re-synced — including the one on the path the API SERVER uses,
  # which is the only path that matters and is not a node we schedule onto (it is
  # tainted on a multi-node cluster and does not exist in the customer's cluster at
  # all on EKS/GKE). Measured: operator pod Ready at 02:24:38, both Cluster releases
  # attempted at 02:24:38, refused.
  #
  # 🔑 The ordering LOOKED correct for months only because helm_release.barman_plugin
  # below took ~22s and happened to sit in between. `--compact --no-tls` drops
  # cert-manager, which drops the plugin, which drops the cover — so the guarantee
  # was a side effect of an unrelated neighbour, not of anything stated here.
  #
  # There is no pure-provider way to order this correctly (no Terraform resource
  # retries admission), so the fix is NOT here: `atomic = true` on the Cluster
  # releases makes a lost race leave no residue, and dcctl waits for the API server
  # to actually ADMIT a Cluster before re-attempting. Do not re-add an ordering
  # claim to this comment — depends_on cannot express "the webhook answers".
  timeout = 600
}

resource "helm_release" "barman_plugin" {
  count = var.enable_backup_plugin ? 1 : 0

  name       = "plugin-barman-cloud"
  namespace  = var.namespace
  repository = "https://cloudnative-pg.github.io/charts"
  chart      = "plugin-barman-cloud"
  version    = var.plugin_chart_version != "" ? var.plugin_chart_version : null

  # 🔴 THE PLUGIN IS IN THE FAILOVER PATH, BUT IT MUST NOT BE REPLICATED, AND THE
  # SECOND HALF IS THE SURPRISING ONE.
  #
  # In the path: CloudNativePG cannot reconcile a Cluster whose plugins it cannot
  # reach — it reports `Cluster cannot proceed to reconciliation due to an error
  # while interacting with plugins` and stops — and a Cluster that cannot
  # reconcile cannot promote a standby. Measured: 10m50s to fail over when this
  # pod died with the node, 1m51s when it did not.
  #
  # 🔴 AND YET replicaCount MUST STAY 1. An earlier version of this change set it
  # to 2 under --ha, which would have broken EVERY HA bootstrap:
  #
  #   - The plugin's CNPG-I gRPC server is registered as a plain
  #     controller-runtime Runnable, so it is LEADER-ELECTION-GATED: only the
  #     leader ever opens :9090.
  #   - The chart's readinessProbe is a hard-coded `tcpSocket: 9090` with no value
  #     to override it. So the non-leader replica is never Ready. Ever.
  #   - The chart hard-codes `strategy: Recreate` ("RollingUpdate is not supported
  #     by the operator yet"), and Helm computes maxUnavailable as 0 for any
  #     non-RollingUpdate Deployment — so `wait` requires 2 of 2 Ready.
  #
  # Net: helm_release would block for its full 600s timeout and fail the apply, on
  # the release that stands between the platform and its backups. A warm standby
  # here needs the plugin to declare itself non-leader-election first, upstream.
  #
  # So the plugin's answer to node loss is the TOLERATION, applied at every
  # posture: it is rescheduled in ~30s rather than ~300s. That recovers most of
  # the 10m50s and is all that is available today. No spread constraint either --
  # at one replica there is nothing to spread.
  values = [yamlencode(local.plugin_values)]

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

output "operator_replicas" {
  description = "How many operator replicas are ACTUALLY requested, read out of the values document handed to Helm. Worth an output because a single-replica operator that dies with a node blocks promotion for every Cluster in the instance -- measured at 10m50s against 1m51s -- and nothing else in the apply reports it."
  value       = local.operator_values.replicaCount
}

output "operator_spread_enforced" {
  description = "Whether the operator carries a hard hostname spread constraint WITH a selector. False at one replica, which is correct. A constraint whose selector matched nothing would also spread nothing, so this reports the selector's presence rather than the constraint's -- see the locals."
  value       = length(local.operator_values.topologySpreadConstraints) > 0 && length(local.operator_values.topologySpreadConstraints[0].labelSelector.matchLabels) > 0
}

output "control_plane_toleration_seconds" {
  description = "The node-loss eviction fuse the operator and plugin pods actually carry, as a string, or \"tostring(null)\" when Kubernetes' 300s default is left in force. Reported because an unset toleration is not an absent one, and for the control plane that 300s is the window in which no Cluster in the instance can fail over."
  value       = tostring(length(local.plugin_values.tolerations) == 0 ? null : local.plugin_values.tolerations[0].tolerationSeconds)
}

output "backup_plugin_enabled" {
  description = "Whether PITR is actually available. Read this rather than re-deriving it: an install with the operator but no plugin has HA and NO backups, and the difference is invisible from the Cluster resources alone."
  value       = var.enable_backup_plugin
}
