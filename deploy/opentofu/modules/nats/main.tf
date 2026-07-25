# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# NATS is DeviceChain's single messaging runtime dependency (ADR-003/006/007): it
# carries core messaging, the JetStream KV cache + distributed lock, and the MQTT
# device ingress. Installed from the official chart; JetStream and MQTT are
# enabled, and the server count comes from var.cluster_replicas (or var.ha as its
# shorthand): 1 by default, 3-node RAFT under ADR-020.
#
# SCOPE, because it is the thing most easily misread here: this module sizes the
# SERVER CLUSTER and nothing else. Whether the platform's streams and KV buckets
# are actually replicated across those servers is a per-stream replica factor set
# in the services' own config, rendered by the DeviceChain Helm chart — which is a
# separate release this module does not install and cannot see. A 3-node cluster
# whose streams are all R1 is the default outcome of raising only this half, and
# it presents as healthy. See var.cluster_replicas.
#
# Transport security (ADR-025): when enable_tls is set, this module generates a
# self-signed CA and a NATS-server leaf from it (the tls provider — declarative,
# no cert-manager, so the CA is a plain output the bring-up threads into every
# service's instance config), writes them to a Secret the chart mounts, and turns
# on TLS for BOTH the 4222 client listener and the 1883 MQTT gateway. Server-auth
# only in v1 (no tls.verify) — clients verify the broker; device authentication is
# the separate auth-callout half of ADR-025.
#
# Tradeoff: the tls provider keeps the CA + server PRIVATE KEYS in tofu state
# (plaintext). State is local + gitignored, which is fine for the local/dev
# bring-up; a production deployment should front this with a real PKI (or encrypted
# remote state) rather than TF-generated self-signed material.

variable "namespace" {
  type = string
}

variable "release_name" {
  description = "Helm release name; also the Service name services connect to."
  type        = string
  default     = "dc-nats"
}

variable "chart_version" {
  description = "nats chart version; empty installs latest."
  type        = string
  default     = ""
}

variable "jetstream_storage" {
  description = <<-EOT
    Size of the JetStream PV.

    This default is NOT a free choice — it must hold the platform's whole
    reservation. JetStream reserves each stream's MaxBytes UP FRONT at creation,
    so the disk floor is the SUM of the ceilings, and today that sum is exactly
    9.5Gi: 8.25Gi of platform streams, 384Mi of MQTT gateway stores, and 896Mi of
    KV buckets. The ceiling that has to hold it is max_file_store, which is 90% of
    this value FLOORED to a whole unit of its own magnitude.

    PER NODE, not per cluster: a 3-server cluster provisions this volume three
    times and each server holds one copy of the reservation, so the size does NOT
    scale with cluster_replicas.

    It was 8Gi, which no longer fits: floor(8 × 0.9) = 7Gi, well under the 9.5Gi
    reserved, so a consumer using this module directly with its defaults would hit
    the "insufficient storage resources available" crashloop on the last services
    to create a stream. The root module always passes its own value explicitly, so
    the shipped path was never exposed — but a default that only works because
    every caller overrides it is a trap, not a default.

    Raising this REQUIRES checking the reservation still fits, and lowering it
    REQUIRES lowering the stream bounds first. Do NOT derive the PV as
    (sum of ceilings) / 0.9: the flooring makes that unsafe, since a 9.5Gi sum
    yields an 11Gi PV whose ceiling is floor(11 × 0.9) = 9Gi — below the sum, and
    back to the crashloop. Pick the smallest whole magnitude where
    floor(magnitude × 0.9) >= the sum, and leave margin above it: at 12Gi the
    unreserved remainder is 512Mi, which is exactly the asserted headroom floor,
    so the budget has no room for one more stream. See nats_jetstream_storage in
    the root variables.tf for the full sizing history.
  EOT
  type        = string
  default     = "16Gi"
  validation {
    # Integer magnitude + unit only. The max_file_store headroom default floors 90%
    # of the magnitude, so a fractional value like "1.5Gi" would drop to "1Gi" (a
    # 33% cut, not 10%), and a unitless value would fail to parse. For a fractional
    # volume, set jetstream_max_file_store explicitly.
    condition     = can(regex("^[0-9]+[A-Za-z]+$", var.jetstream_storage))
    error_message = "jetstream_storage must be an integer magnitude with a unit (e.g. \"8Gi\", \"512Mi\"); set jetstream_max_file_store explicitly for a fractional size."
  }
}

variable "jetstream_max_file_store" {
  description = "Server-level max_file_store — the hard aggregate JetStream disk ceiling (ADR-023), applied PER SERVER. Empty (default) derives it as 90% of jetstream_storage, FLOORED to a whole unit of that size's own magnitude (16Gi yields 14Gi, not 14.4Gi), leaving filesystem headroom so JetStream errors cleanly before the volume is 100% full. Set explicitly (e.g. \"6Gi\") to override; must be <= jetstream_storage."
  type        = string
  default     = ""
}

variable "ha" {
  description = "Shorthand for the ADR-020 topology: 3 NATS servers instead of 1. cluster_replicas overrides the count; the two must not contradict each other (see the precondition on helm_release.nats)."
  type        = bool
  default     = false
}

variable "cluster_replicas" {
  description = <<-EOT
    Number of NATS servers in the RAFT cluster. 0 (default) derives it from var.ha
    — 3 when true, 1 when false — so the common case stays a single toggle; set it
    explicitly to represent a topology the toggle cannot, e.g. 5 servers.

    ODD ONLY, and at most 5. RAFT commits on a MAJORITY, so an even cluster
    tolerates no more failures than the odd size below it (4 servers tolerate 1,
    exactly as 3 do) while costing an extra server and widening the quorum every
    write waits on. Above 5 the write-latency cost outruns the durability gain,
    and JetStream refuses more than 5 replicas per stream anyway — which is the
    ceiling that actually binds, since a stream cannot be replicated wider than
    the cluster hosting it.

    THIS IS ONLY HALF THE TOGGLE. It sizes the SERVER cluster; the per-stream
    replica factor is a separate lever in the services' own config
    (instance.config.infrastructure.nats.streamReplicas, rendered by the
    DeviceChain Helm chart, which OpenTofu does not install). Raising this alone
    yields a 3-node broker whose every stream and KV bucket is still single-replica
    — the exact false-HA state ADR-020 A0 exists to close. The services clamp
    streamReplicas down to 1 against an unclustered broker rather than crashloop,
    and export desired-vs-actual replication so the disagreement alerts; dcctl
    --ha sets both levers from one value.
  EOT
  type        = number
  default     = 0

  validation {
    condition     = contains([0, 1, 3, 5], var.cluster_replicas)
    error_message = "cluster_replicas must be 0 (derive from var.ha), 1, 3, or 5. RAFT commits on a majority, so an even cluster tolerates no more failures than the odd size below it while costing an extra server and a wider quorum on every write; JetStream refuses more than 5 replicas per stream."
  }
}

variable "enable_prom_exporter" {
  description = <<-EOT
    Run the prometheus-nats-exporter sidecar alongside each NATS server, exposing
    BROKER-side metrics (routes, RAFT/JetStream cluster health) on :7777.

    Complementary to, not a substitute for, the platform's own replication gauges
    (devicechain_*_jetstream_replicas_desired/actual/peers_current, exported by
    every area that touches JetStream): those say whether the STREAMS an instance
    depends on are replicated, this says whether the SERVERS carrying them can
    still see each other. A cluster that has lost a route reports healthy streams
    right up until it does not.

    The PodMonitor that scrapes this is rendered by the DeviceChain Helm chart
    (metrics.enabled), deliberately NOT here: it is a Prometheus Operator CRD, and
    a chart in this module that renders one would fail the apply on any cluster
    where the operator has not been installed yet — which, since both installs run
    in parallel, includes a fresh bring-up. The chart runs after this apply
    completes, so by then the CRDs exist.
  EOT
  type        = bool
  default     = true
}

variable "enable_tls" {
  description = "Terminate TLS on the NATS client + MQTT listeners (ADR-025). When true, the CA output must be threaded into the services' instance config so they dial over TLS."
  type        = bool
  default     = true
}

variable "enable_auth" {
  description = "Enable broker authentication (ADR-025): an APP account with a shared service login + an auth_callout that delegates device connects to device-management. Requires callout_issuer_public + service_password_bcrypt (minted out-of-band, since nkeys aren't a TF primitive), with the plaintext password threaded into the services' instance config."
  type        = bool
  default     = false
}

variable "reject_qos2_publish" {
  description = <<-EOT
    Refuse MQTT QoS 2 PUBLISH at the broker (nats-server `reject_qos2_publish`).

    QoS 2 buys nothing here: the platform has its own opt-in event de-duplication,
    and the exactly-once handshake costs two extra round trips per message. It also
    carries the one gateway stream a DEVICE can fill on purpose — $MQTT_qos2in
    holds an inbound QoS 2 message until its PUBREL arrives, so firmware that opens
    QoS 2 publishes and never completes the handshake accumulates up to 65,535
    messages per session with nothing reclaiming them. That store is bounded
    (ADR-023), so this is defense in depth rather than the only guard: bounding
    stops the disk-exhaustion crashloop, and this stops the traffic that fills it.

    BE DELIBERATE ABOUT TURNING THIS ON with firmware you do not control. The
    rejection is NOT a graceful per-message NACK: nats-server returns an error from
    its PUBLISH parse path, which tears down the client CONNECTION, so a device
    that publishes QoS 2 in a loop will reconnect in a loop. A QoS 2 Will is
    refused earlier and more cleanly, at CONNECT, with its own return code.

    Devices publishing QoS 0 or 1 — the recommended modes — are unaffected either
    way.
  EOT
  type        = bool
  default     = true
}

variable "callout_issuer_public" {
  description = "Public account nkey (A...) the auth-callout responder signs device user JWTs with — the server's trust anchor. Required when enable_auth is true."
  type        = string
  default     = ""
}

variable "service_password_bcrypt" {
  description = "BCRYPT HASH ($2a$...) of the shared static `dc_service` password. It is what lands in the broker config; nats-server bcrypt-compares the plaintext each service presents. The plaintext is never rendered here. Required when enable_auth is true. Sensitive."
  type        = string
  default     = ""
  sensitive   = true
}

variable "mqtt_node_port" {
  description = <<-EOT
    Local-kind only: expose the MQTT gateway as a NodePort on this node port so a
    device or MQTT tool on the HOST can reach the broker at ssl://127.0.0.1:1883.

    The nats chart ships a ClusterIP Service only. A local kind cluster maps host
    1883 -> node 31883 (deploy/local/kind-cluster.yaml, embedded by dcctl), but with
    no Service claiming node port 31883 that host map forwards to an empty node port
    and resets — a device client sees "network Error: EOF". Setting this to that node
    port creates a dedicated NodePort Service for :1883 so the host map lands on the
    broker, exactly as :80/:443 land on ingress-nginx's NodePort.

    0 = disabled (the CLOUD default): a NodePort would publish MQTT on every node IP,
    which is not wanted off a single-node dev box. dcctl sets 31883 on a local
    context (mirroring ingress_use_host_port), matching the kind host map; leave it 0
    everywhere else. The broker still terminates TLS and runs the auth callout, so
    the port is not an open relay even when exposed.
  EOT
  type        = number
  default     = 0

  validation {
    condition     = var.mqtt_node_port == 0 || (var.mqtt_node_port >= 30000 && var.mqtt_node_port <= 32767)
    error_message = "mqtt_node_port must be 0 (disabled) or in the default NodePort range 30000-32767."
  }
}

locals {
  # Service name the internal services present (see natsauth.ServiceUser). Kept in
  # sync with the Go constant; a mismatch would lock every service out.
  service_user = "dc_service"

  # js_max_file_store resolves the server-level max_file_store (see the
  # jetstream_max_file_store variable and the fileStore.maxSize wiring below). When
  # not overridden, it is 90% of the PVC size: split jetstream_storage into its
  # numeric magnitude and unit (e.g. "8Gi" -> 8 + "Gi"), floor 90% of the magnitude,
  # and reattach the unit ("8Gi" -> "7Gi"). The 10% left over is filesystem headroom
  # so JetStream stops accepting data before the volume is physically full.
  js_size_magnitude = tonumber(regex("^[0-9.]+", var.jetstream_storage))
  js_size_unit      = regex("[A-Za-z]+$", var.jetstream_storage)
  js_max_file_store = var.jetstream_max_file_store != "" ? var.jetstream_max_file_store : "${floor(local.js_size_magnitude * 0.9)}${local.js_size_unit}"

  # The server count. var.cluster_replicas wins when set; var.ha is the shorthand.
  cluster_replicas = var.cluster_replicas > 0 ? var.cluster_replicas : (var.ha ? 3 : 1)
  clustered        = local.cluster_replicas > 1

  # Replication for the MQTT gateway's OWN streams ($MQTT_sess, $MQTT_msgs,
  # $MQTT_qos2in, $MQTT_out) — nats-server's `mqtt { stream_replicas }`.
  #
  # These are easy to forget because nothing in DeviceChain creates them: the
  # broker does, on first MQTT connect. They are also the streams that decide
  # whether an MQTT device SURVIVES a node loss, since they hold persistent-session
  # state and inflight QoS 1 messages. Left at the default 1 on a 3-node cluster,
  # losing the node that happened to host them drops every persistent session and
  # every un-acked message — an outage that looks like a device-side reconnect
  # storm and points nowhere near the broker.
  #
  # Capped at 3 rather than tracking a 5-server cluster: session state is
  # recoverable by a device reconnecting, so it does not warrant a 5-way quorum on
  # every write the way the platform's own streams might. A 5-node operator who
  # wants R5 here should say so in nats_jetstream_* territory, not inherit it.
  mqtt_stream_replicas = min(local.cluster_replicas, 3)

  # Spread the servers across NODES, and do it as a HARD constraint.
  #
  # Without this the chart's default is {} — no constraint at all — and the
  # scheduler is free to place all three servers on one node. That cluster passes
  # every check A0 adds: three peers, streams reporting Replicas:3, all peers
  # current. It survives zero node failures. Three replicas on one node is the most
  # expensive way to run one replica.
  #
  # whenUnsatisfiable is DoNotSchedule, not ScheduleAnyway, precisely because the
  # soft form permits exactly that co-location — silently, and only under the node
  # pressure that makes it matter. The cost is that a 3-server cluster stays Pending
  # on a cluster with fewer than 3 schedulable nodes, which is the correct failure:
  # an unschedulable pod is a question the operator answers in minutes, whereas a
  # co-located "HA" cluster is a question nobody asks until a node dies.
  #
  # Built with a for-expression rather than a ternary so both branches have the
  # same type — `cond ? {k = {...}} : {}` gives HCL two object types to unify and
  # is the same class of trap as the tls blocks below.
  spread_constraints = {
    for key in(local.clustered ? ["kubernetes.io/hostname"] : []) :
    key => {
      maxSkew           = 1
      whenUnsatisfiable = "DoNotSchedule"
    }
  }

  # Broker-auth config merged into nats-server.conf (config.merge) when enabled: a
  # single APP account holds services (static dc_service login, exempt) and devices
  # (callout-placed via aud=APP with tenant-scoped perms). JetStream is enabled on
  # APP because the MQTT gateway stores sessions there. SYS is the system account.
  # Always the full map (no ternary) — it is only referenced on the enable_auth
  # branch of chart_values_encoded, so var.service_password_bcrypt /
  # callout_issuer_public being "" when disabled is harmless.
  #
  # The dc_service password is stored as a BCRYPT HASH ($2a$...), not plaintext: the
  # chart renders config.merge into a ConfigMap, so a plaintext password would let
  # anyone with `configmap get` read the full-privilege credential. nats-server
  # detects the $2a$ prefix and bcrypt-compares the plaintext each service presents
  # (that plaintext lives only in the services' instance-config Secret). The chart
  # renders the value as a JSON double-quoted string, which nats-server treats
  # literally — env-var expansion applies only to bare (unquoted) config tokens, so
  # the hash's `$` is not expanded.
  #
  # LANDMINE, and the reason the APP account carries `jetstream = "enabled"` and
  # nothing else. That bare string resolves to nats-server's DYNAMIC account
  # limits (unlimited), which is what makes replication affordable here: JetStream
  # charges an account's file-store quota the RESERVATION TIMES THE REPLICA COUNT,
  # so the platform's ~9.5Gi of stream and bucket ceilings bills as ~28.5Gi at R3.
  # Against unlimited that multiply is inert and only the server-level
  # max_file_store binds — which is per-node, and therefore already correct: each
  # of the 3 servers stores one copy.
  #
  # If anyone ever adds an explicit `jetstream { max_file_store = ... }` to this
  # account, R3 stream creation starts failing at roughly a THIRD of the ceiling
  # they wrote, and the error is "insufficient storage resources available" — the
  # same message a genuinely undersized PV produces. It reads as a disk-sizing
  # problem, so the instinct is to raise the number, which moves the cliff without
  # removing it. Set account limits only in units of (reservation × replicas), or
  # leave them dynamic and let max_file_store be the only ceiling.
  #
  # Remaining tradeoff, acceptable pre-GA: enabling auth on a cluster that
  # previously ran without accounts abandons the old default-account ($G) JetStream
  # state (streams/KV/MQTT sessions) — no crash, but a decisive cutover that assumes
  # FRESH JetStream state. A real migration would export/import streams across
  # accounts.
  auth_merge_content = {
    accounts = {
      APP = {
        jetstream = "enabled"
        users     = [{ user = local.service_user, password = var.service_password_bcrypt }]
      }
      SYS = {}
    }
    system_account = "SYS"
    authorization = {
      # NUMBER of seconds, not a duration string: nats-server's authorization
      # timeout parser only accepts int/float and silently ignores a string,
      # leaving the 1s default — too tight for the TLS handshake + callout
      # round-trip (DB + secret check) and a source of flaky connects under load.
      timeout = 5
      auth_callout = {
        issuer     = var.callout_issuer_public
        auth_users = [local.service_user]
        account    = "APP"
      }
    }
  }
  tls_secret_name       = "${var.release_name}-tls"
  tls_ca_configmap_name = "${var.release_name}-ca"
  # SANs cover every in-cluster name a client dials the broker by: the short
  # Service name, the namespaced name (what services + the MQTT source use), and
  # the fully-qualified forms. localhost covers in-pod tooling (nats-box).
  server_dns_names = [
    var.release_name,
    "${var.release_name}.${var.namespace}",
    "${var.release_name}.${var.namespace}.svc",
    "${var.release_name}.${var.namespace}.svc.cluster.local",
    "localhost",
  ]

  # Base chart values (TLS material + listeners). config.merge (broker-auth) is
  # added conditionally below.
  chart_values = {
    # Reference the CA-only ConfigMap in every tls block + the nats-box contexts
    # so in-cluster tooling verifies the broker without mounting the server key.
    tlsCA = {
      enabled       = var.enable_tls
      configMapName = var.enable_tls ? local.tls_ca_configmap_name : null
    }
    config = {
      jetstream = {
        enabled = true
        fileStore = {
          pvc = {
            enabled = true
            size    = var.jetstream_storage
          }
          # max_file_store — the server-level HARD AGGREGATE JetStream disk ceiling
          # (ADR-023): the total file store across every account/stream/KV/MQTT
          # session cannot exceed this, so a flood is bounded even before the
          # per-stream MaxBytes ceilings, and JetStream returns clean publish errors
          # instead of driving the volume to a wedged 100%-full state. The chart
          # otherwise derives this from the PVC size (== the raw volume, no
          # headroom); we set it explicitly BELOW the PVC (default 90%, see locals)
          # so the filesystem keeps overhead room. The per-stream MaxBytes ceilings
          # (backend config) sub-divide this budget across streams.
          maxSize = local.js_max_file_store
        }
      }
      # Each tls block emits BOTH keys on both toggle states so the HCL branches
      # unify: a `? {enabled=true,secretName=x} : {enabled=false}` shape would
      # coerce enabled to the STRING "true"/"false", which chart 2.14.2's ternary
      # on tls.enabled rejects. `secretName = ... : null` keeps enabled a real bool.
      nats = {
        tls = {
          enabled    = var.enable_tls
          secretName = var.enable_tls ? local.tls_secret_name : null
        }
      }
      mqtt = {
        enabled = true
        tls = {
          enabled    = var.enable_tls
          secretName = var.enable_tls ? local.tls_secret_name : null
        }
        # `merge` is the chart's escape hatch for mqtt{} keys it exposes no field
        # for. Safe to set unconditionally: the value is already a bool, so there
        # is no HCL ternary here to coerce it to a string the way the tls blocks
        # above have to work around.
        merge = {
          reject_qos2_publish = var.reject_qos2_publish
          stream_replicas     = local.mqtt_stream_replicas
        }
      }
      cluster = {
        enabled  = local.clustered
        replicas = local.cluster_replicas
      }
    }
    podTemplate = {
      topologySpreadConstraints = local.spread_constraints
    }
    promExporter = {
      enabled = var.enable_prom_exporter
    }
  }

  # The rendered Helm values. The auth toggle is applied at the STRING level
  # (yamlencode output), not the map level: `{config.merge = {...}}` and the
  # base config are different object types that an HCL ternary cannot unify, but
  # two yamlencode() results are both strings and unify cleanly.
  chart_values_encoded = var.enable_auth ? yamlencode(merge(local.chart_values, {
    config = merge(local.chart_values.config, { merge = local.auth_merge_content })
  })) : yamlencode(local.chart_values)
}

# --- TLS material (ADR-025) --------------------------------------------------
# A self-signed CA → a NATS-server leaf signed by it. A CA→leaf chain (rather than
# a bare self-signed server cert) lets the leaf rotate on apply without clients
# re-trusting a new root, and gives each node of an HA cluster its own leaf from
# the shared CA (ADR-020).

resource "tls_private_key" "ca" {
  count     = var.enable_tls ? 1 : 0
  algorithm = "RSA"
  rsa_bits  = 2048
}

resource "tls_self_signed_cert" "ca" {
  count           = var.enable_tls ? 1 : 0
  private_key_pem = tls_private_key.ca[0].private_key_pem

  is_ca_certificate     = true
  validity_period_hours = 87600 # 10 years — the CA is long-lived; the leaf rotates.
  early_renewal_hours   = 720

  subject {
    common_name  = "DeviceChain NATS CA"
    organization = "The DeviceChain Authors"
  }

  allowed_uses = [
    "cert_signing",
    "crl_signing",
    "digital_signature",
  ]
}

resource "tls_private_key" "server" {
  count     = var.enable_tls ? 1 : 0
  algorithm = "RSA"
  rsa_bits  = 2048
}

resource "tls_cert_request" "server" {
  count           = var.enable_tls ? 1 : 0
  private_key_pem = tls_private_key.server[0].private_key_pem

  subject {
    common_name  = "${var.release_name}.${var.namespace}"
    organization = "The DeviceChain Authors"
  }

  dns_names = local.server_dns_names
}

resource "tls_locally_signed_cert" "server" {
  count              = var.enable_tls ? 1 : 0
  cert_request_pem   = tls_cert_request.server[0].cert_request_pem
  ca_private_key_pem = tls_private_key.ca[0].private_key_pem
  ca_cert_pem        = tls_self_signed_cert.ca[0].cert_pem

  validity_period_hours = 8760 # 1 year — re-issued on apply as it nears expiry.
  early_renewal_hours   = 720

  allowed_uses = [
    "server_auth",
    "digital_signature",
    "key_encipherment",
  ]
}

# The chart mounts this Secret into the server pods (config.*.tls.secretName). It
# carries the leaf + its key; the leaf cert bundles the CA so a client that only
# trusts the CA still gets a full chain.
resource "kubernetes_secret_v1" "nats_tls" {
  count = var.enable_tls ? 1 : 0

  metadata {
    name      = local.tls_secret_name
    namespace = var.namespace
  }

  type = "kubernetes.io/tls"

  data = {
    "tls.crt" = "${tls_locally_signed_cert.server[0].cert_pem}${tls_self_signed_cert.ca[0].cert_pem}"
    "tls.key" = tls_private_key.server[0].private_key_pem
    "ca.crt"  = tls_self_signed_cert.ca[0].cert_pem
  }
}

# CA-only ConfigMap for the chart's tlsCA reference (nats-box contexts + the CA in
# every tls block). Deliberately a ConfigMap holding ONLY the public CA — pointing
# tlsCA at the server Secret would mount the private key into the nats-box debug
# pod, which has no need for it.
resource "kubernetes_config_map_v1" "nats_ca" {
  count = var.enable_tls ? 1 : 0

  metadata {
    name      = local.tls_ca_configmap_name
    namespace = var.namespace
  }

  data = {
    "ca.crt" = tls_self_signed_cert.ca[0].cert_pem
  }
}

resource "helm_release" "nats" {
  name       = var.release_name
  namespace  = var.namespace
  repository = "https://nats-io.github.io/k8s/helm/charts/"
  chart      = "nats"
  version    = var.chart_version != "" ? var.chart_version : null

  # The server pods mount the TLS material at startup; create it first so the STS
  # is not stuck ContainerCreating on a missing secret (helm wait would time out).
  depends_on = [
    kubernetes_secret_v1.nats_tls,
    kubernetes_config_map_v1.nats_ca,
  ]

  # https://github.com/nats-io/k8s — config.* maps onto nats-server config. The
  # rendered values (TLS listeners + optional broker-auth) are built in locals
  # above; see local.chart_values_encoded for the auth toggle.
  values = [local.chart_values_encoded]

  lifecycle {
    # Fail closed on a manual apply that turns auth on without minting the
    # credentials (the bring-up always provides them together): an empty issuer /
    # password would render a broker that rejects every device and every service.
    precondition {
      condition     = !var.enable_auth || (var.callout_issuer_public != "" && var.service_password_bcrypt != "")
      error_message = "nats enable_auth=true requires non-empty callout_issuer_public and service_password_bcrypt (mint them via dcctl / the genauth helper)."
    }

    # Refuse the contradiction rather than silently picking a winner. ha=true with
    # cluster_replicas=1 is someone asking for the ADR-020 topology and a single
    # server in the same breath; whichever side won quietly, the operator would be
    # told they have HA and have something else. Setting cluster_replicas>1 with
    # ha=false is NOT a contradiction — it is the explicit-topology path, and the
    # cluster is enabled on the count, not the flag.
    precondition {
      condition     = !(var.ha && var.cluster_replicas == 1)
      error_message = "nats ha=true with cluster_replicas=1 is contradictory: ha asks for the ADR-020 3-node RAFT topology and cluster_replicas pins a single server. Drop cluster_replicas to let ha choose (0 = derive), or set ha=false if a single server is what you want."
    }
  }
}

# Optional NodePort so a LOCAL kind cluster's host-port map (host 1883 -> node
# 31883) actually lands on the MQTT gateway. See var.mqtt_node_port. Disabled (count
# 0) on any cloud deploy, where a NodePort would publish MQTT on every node IP. It is
# a SEPARATE Service from the chart's ClusterIP one — only :1883 is exposed, never
# the 4222 client listener — and selects the same server pods.
#
# Two couplings to know, both of which fail as empty endpoints (a reset -> paho EOF,
# the pre-fix dead-route symptom), so verify host:1883 after a chart bump:
#   - The selector + target_port name below mirror the nats chart's own pod labels
#     (component=nats) and container-port name ("mqtt"). var.chart_version defaults to
#     latest; a future chart that relabels the pods or renames the port would leave
#     this Service with no endpoints. Pinning chart_version is the durable guard.
#   - node_port is FIXED (it must equal the kind host map). ingress-nginx also takes
#     NodePorts but lets the API server assign them dynamically from 30000-32767, so
#     there is a small chance it grabs this port first on a fresh cluster and this
#     apply fails "already allocated". Rare, but if it recurs, pin ingress's NodePorts.
resource "kubernetes_service_v1" "mqtt_nodeport" {
  count = var.mqtt_node_port > 0 ? 1 : 0

  metadata {
    name      = "${var.release_name}-mqtt-nodeport"
    namespace = var.namespace
    labels = {
      "app.kubernetes.io/name"      = "nats"
      "app.kubernetes.io/instance"  = var.release_name
      "app.kubernetes.io/component" = "mqtt-nodeport"
    }
  }

  spec {
    type = "NodePort"
    # The chart's own pod selector, so this routes to the same nats-server pods the
    # ClusterIP Service does. target_port names the "mqtt" container port (1883).
    selector = {
      "app.kubernetes.io/name"      = "nats"
      "app.kubernetes.io/instance"  = var.release_name
      "app.kubernetes.io/component" = "nats"
    }
    port {
      name        = "mqtt"
      port        = 1883
      target_port = "mqtt"
      node_port   = var.mqtt_node_port
      protocol    = "TCP"
    }
  }

  depends_on = [helm_release.nats]
}

output "client_url" {
  description = "NATS client URL."
  value       = "nats://${var.release_name}.${var.namespace}:4222"
}

output "mqtt_url" {
  description = "NATS MQTT ingress URL."
  value       = "tcp://${var.release_name}.${var.namespace}:1883"
}

output "service" {
  description = "In-cluster NATS service host."
  value       = "${var.release_name}.${var.namespace}"
}

output "ca_pem" {
  description = "PEM-encoded CA that signed the NATS server cert. Empty when TLS is off. Threaded into each service's instance config so clients verify the broker (ADR-025)."
  value       = var.enable_tls ? tls_self_signed_cert.ca[0].cert_pem : ""
}

output "cluster_replicas" {
  description = "Number of NATS servers provisioned. This is the CEILING on the per-stream replica factor the services may ask for (instance.config.infrastructure.nats.streamReplicas, a Helm value this module does not set): a stream cannot be replicated wider than the cluster hosting it. dcctl's preflight reads this to refuse an install whose two halves disagree, instead of letting it come up looking replicated."
  value       = local.cluster_replicas
}

output "tls_enabled" {
  description = "Whether the broker terminates TLS (drives the matching client-side flag in the instance config)."
  value       = var.enable_tls
}
