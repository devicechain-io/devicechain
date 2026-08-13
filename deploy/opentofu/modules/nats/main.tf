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
  description = <<-EOT
    nats chart version. Empty installs latest, which this module does NOT do by
    default and should not be asked to.

    🔴 PINNED BECAUSE THIS MODULE'S BEHAVIOUR IS A PROPERTY OF THE CHART, not just
    of the values passed to it. podTemplate.configChecksumAnnotation below relies on
    the chart hashing the rendered ConfigMap into the pod template; the reloader
    sidecar's cert watch-list is derived by the chart's own helpers; the lame-duck
    and terminationGracePeriod floors the timeout on helm_release.nats is sized
    against are chart defaults. All of those were read and verified at 2.14.4. On
    "latest" they are whatever upstream last shipped, and nothing here would notice
    them moving.

    There is a second, sharper reason. The hashed manifest includes the chart's
    standard labels — `helm.sh/chart` and `app.kubernetes.io/version` — so under
    "latest" a new upstream chart release changes the checksum and ROLLS THE BROKER
    on the next otherwise-unrelated apply. An unpinned chart turns a config-adoption
    mechanism into an unscheduled outage trigger.

    Bumping this is a deliberate act: re-check the checksum template
    (files/stateful-set/pod-template.yaml), the reloader's watched paths, and the
    lame-duck defaults before raising it.
  EOT
  type        = string
  default     = "2.14.4"
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
    (metrics.natsPodMonitor), deliberately NOT here: it is a Prometheus Operator
    CRD, and a chart in this module that renders one would fail the apply on any
    cluster where the operator has not been installed yet — which, since both
    installs run in parallel, includes a fresh bring-up. The chart runs after this
    apply completes, so by then the CRDs exist.

    🔴 The upstream nats chart CAN render that PodMonitor itself, behind
    promExporter.podMonitor.enabled, and this module deliberately leaves it off for
    the ordering reason above. Do not turn it on as well: the two select the same
    pods on the same port, so both enabled means every broker series is scraped
    twice. If you ever want the upstream one instead, turn OFF
    metrics.natsPodMonitor in the chart and accept that the tofu apply now depends
    on the monitoring stack being installed first.
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

variable "sys_password_bcrypt" {
  description = "BCRYPT HASH ($2a$...) of the `dc_sys` system-account password, read by the event-sources broker-presence tap. A SEPARATE credential from service_password_bcrypt because the system account sees every account's connection lifecycle. EMPTY (the default) renders SYS with no users, which turns the tap off — the behaviour of every instance before it existed. Sensitive."
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

  # System-account login (see natsauth.SysUser), kept in sync with the Go constant
  # the same way. Rendered only when a hash was supplied: an account block with an
  # empty user list is what SYS looked like before the presence tap, and is what an
  # instance that has not minted the credential must keep looking like.
  sys_user  = "dc_sys"
  sys_users = var.sys_password_bcrypt == "" ? [] : [{ user = local.sys_user, password = var.sys_password_bcrypt }]

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

  # 🔴 ONE DEFINITION, READ BY BOTH THE CHART VALUES AND ha_topology. These two
  # are not conveniences — they are the difference between an assertion and a
  # decoration.
  #
  # The rule the hard way: an output that RESTATES an expression is a second
  # copy, and a CI check reading that output is checking the copy against itself.
  # Both of these used to be written out twice — `var.enable_tls &&
  # local.clustered` at the cluster tls block and again in the output; `var.ha &&
  # var.cluster_replicas == 1` in the precondition and again in the output. A
  # measured mutation of the values side (route TLS off on a clustered broker,
  # leaving port 6222 open to unverified peers, which is the exact hole A0
  # closed) left the output reading `route_tls_verified = true` and every
  # hack/check-tofu-validations.sh assertion green. The check could not fail,
  # because nothing downstream of the mutation was being read.
  #
  # Consumed by reference below, so the same mutation now moves the output too.
  # spread_constraints and server_dns_names were already built this way; these
  # are the two that were not.
  route_tls_verified = var.enable_tls && local.clustered
  contradictory      = var.ha && var.cluster_replicas == 1

  # Replication for the MQTT gateway's OWN streams ($MQTT_sess, $MQTT_msgs,
  # $MQTT_qos2in, $MQTT_out) — nats-server's `mqtt { stream_replicas }`.
  #
  # These are easy to forget because nothing in DeviceChain creates them: the
  # broker does, on first MQTT connect. They are also the streams that decide
  # whether an MQTT device SURVIVES a node loss, since they hold persistent-session
  # state and inflight QoS 1 messages.
  #
  # WHY SET IT AT ALL, given the default is not simply 1. Left unset on a clustered
  # broker, nats-server derives the count in mqttDetermineReplicas: it walks the
  # configured ROUTE URLs, DNS-RESOLVES each one, sums the addresses, and caps the
  # result at 3. On a settled 3-server cluster that yields 3 — the same number set
  # here, which is why this is not a correction of the default so much as a
  # replacement of a derivation with a constant.
  #
  # The derivation is the problem. It runs ONCE, when the first MQTT client
  # connects and the streams are created, and it reads DNS at that instant. In this
  # chart the routes are the headless Service's per-pod names, so a first connect
  # that lands while peers are still starting resolves fewer addresses and creates
  # the streams at R1 or R2 — permanently, because the mismatch path below only
  # WARNS. So the failure is not a wrong default, it is a NON-DETERMINISTIC one
  # that depends on pod startup order, and produces an unreplicated MQTT session
  # store on a cluster where everything else replicated correctly.
  #
  # Setting it explicitly also turns the warning ON: nats-server compares the
  # configured value against the live stream only `if opts.MQTT.StreamReplicas != 0`,
  # so an unset broker never says a word about a mismatch it is living with.
  #
  # 🔴 IT DOES NOT LIFT AN EXISTING STREAM. The platform's own streams and buckets
  # ARE reconciled upward on start (core/messaging reconcileStreamReplicas); the
  # $MQTT_* ones are not ours to reconcile, and nats-server only logs the
  # difference. An instance whose MQTT streams already exist at R1 keeps them there
  # after this is raised — they need a deliberate `nats stream update` (or a delete
  # on an instance with no sessions worth keeping).
  #
  # Capped at 3 rather than tracking a 5-server cluster, matching the cap the
  # derivation itself applies: session state is recoverable by a device
  # reconnecting, so it does not warrant a 5-way quorum on every write.
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
  # Built with a for-expression rather than a ternary purely for readability of the
  # empty case. NOT because a ternary would break: `cond ? {k = {...}} : {}` was
  # tested against this module and unifies cleanly to a map, with no coercion of the
  # inner values. That is a DIFFERENT shape from the tls trap below, where the two
  # branches share an attribute (`enabled`, a bool) and differ in another
  # (`secretName`, a string), forcing the fallback to map(string) and stringifying
  # the bool. Here the empty branch contributes no attributes, so there is nothing
  # to unify against. The earlier version of this comment drew that analogy and it
  # was wrong.
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
      # SYS carries a user only once the presence tap's credential has been minted.
      # The system account needs no `jetstream` and no exports: the advisories the
      # tap reads and the CONNZ request it makes are both served INSIDE SYS, and a
      # subscriber there is a plain core-NATS client.
      SYS = { users = local.sys_users }
    }
    system_account = "SYS"
    authorization = {
      # NUMBER of seconds, not a duration string: nats-server's authorization
      # timeout parser only accepts int/float and silently ignores a string,
      # leaving the 1s default — too tight for the TLS handshake + callout
      # round-trip (DB + secret check) and a source of flaky connects under load.
      timeout = 5
      # 🔴 EVERY STATIC LOGIN MUST BE LISTED HERE, INCLUDING THE ONE IN ANOTHER
      # ACCOUNT. auth_callout carries no `allowed_accounts`, and nats-server reads an
      # empty list as "every account is in scope" (server/auth.go: `delegated :=
      # len(opts.AuthCallout.AllowedAccounts) == 0`) — the system account included. So a
      # user absent from auth_users is handed to the DEVICE callout responder, which
      # expects a `tenant:credentialId` username and denies anything else.
      #
      # The failure is silent in the worst way: the broker is healthy, every device
      # works, and only the one service presenting the unlisted login is refused. It
      # then logs once and disables its feature, so the symptom is a capability quietly
      # missing rather than anything failing.
      auth_callout = {
        issuer     = var.callout_issuer_public
        auth_users = [local.service_user, local.sys_user]
        account    = "APP"
      }
    }
  }
  tls_secret_name       = "${var.release_name}-tls"
  tls_ca_configmap_name = "${var.release_name}-ca"
  # 🔴 THE ROUTE LISTENER DOES NOT USE THE CLIENT NAMES, and getting this wrong
  # does not degrade the cluster — it prevents one from forming at all.
  #
  # Clients reach the broker through the ClusterIP Service (dc-nats.dc-system).
  # The servers reach EACH OTHER by POD name through the chart's headless
  # Service — dc-nats-1.dc-nats-headless — because a route has to address one
  # specific peer, which a load-balanced Service name cannot do. With route TLS
  # verification on (which is the point: without it any pod that can reach 6222
  # joins the cluster as a peer and reads every account), each server verifies the
  # name it dialled against its peer's certificate. A leaf carrying only the
  # client names fails EVERY route handshake:
  #
  #   TLS route handshake error: x509: certificate is valid for dc-nats,
  #   dc-nats.dc-system, ... not dc-nats-1.dc-nats-headless
  #
  # and the result is three isolated servers that never elect a JetStream meta
  # leader. Not a degraded HA cluster — no cluster. Every stream creation then
  # fails or falls back to a single replica on whichever server got it.
  #
  # This shipped once. It is invisible to `tofu validate`, to helm lint, to the
  # rendered ha_topology output (which correctly reported route_tls_verified =
  # true — the configuration WAS right, the certificate was not), and to every
  # single-node install, which never opens a route at all. The 3-node rig
  # (hack/ha-rig.sh) is what caught it, which is the argument for the rig.
  route_dns_names = local.clustered ? flatten([
    for i in range(local.cluster_replicas) : [
      # All four resolution depths: a server dials the peer address the chart
      # wrote into its routes, and which of these that is depends on the chart's
      # own templating, not on us. Naming all four costs nothing and removes the
      # dependency.
      "${var.release_name}-${i}.${var.release_name}-headless",
      "${var.release_name}-${i}.${var.release_name}-headless.${var.namespace}",
      "${var.release_name}-${i}.${var.release_name}-headless.${var.namespace}.svc",
      "${var.release_name}-${i}.${var.release_name}-headless.${var.namespace}.svc.cluster.local",
    ]
  ]) : []

  # SANs cover every in-cluster name a client dials the broker by: the short
  # Service name, the namespaced name (what services + the MQTT source use), and
  # the fully-qualified forms. localhost covers in-pod tooling (nats-box). Plus,
  # when clustered, the per-pod route names above.
  server_dns_names = concat([
    var.release_name,
    "${var.release_name}.${var.namespace}",
    "${var.release_name}.${var.namespace}.svc",
    "${var.release_name}.${var.namespace}.svc.cluster.local",
    "localhost",
  ], local.route_dns_names)

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
        # SECURE THE WIRE THIS BLOCK CREATES (ADR-025 + ADR-020 A0).
        #
        # Turning on clustering opens route port 6222, and it is the port every
        # replicated write crosses: device events, commands, alarms, and the
        # dc_leases fence. Left at the chart default it is plaintext AND
        # unauthenticated, on a module whose 4222 and 1883 listeners both terminate
        # TLS — so enabling HA would have opened a hole that single-node deploys do
        # not have, which is the wrong direction for a durability feature.
        #
        # verify = true is the half that matters most. TLS alone encrypts the wire
        # but still accepts ANY inbound route connection, and a NATS route peer is
        # not a client: it joins the cluster and can read and write every account,
        # bypassing the auth callout entirely. With verify, a peer must present a
        # certificate signed by this module's CA — which only the server pods have,
        # since it is mounted from the same Secret. That gives mutual authentication
        # with no new credential to mint, rotate or thread through the bring-up.
        #
        # `merge` rather than a first-class field because the chart exposes no
        # `verify` key on the cluster tls block; it passes merge straight into the
        # generated nats-server tls{} stanza.
        tls = {
          enabled    = local.route_tls_verified
          secretName = local.route_tls_verified ? local.tls_secret_name : null
          merge = {
            verify = true
          }
        }
      }
    }
    podTemplate = {
      topologySpreadConstraints = local.spread_constraints

      # 🔴 WITHOUT THIS, A CONFIG CHANGE CAN BE APPLIED AND NEVER ADOPTED, AND
      # EVERY LAYER REPORTS SUCCESS. Measured on a live cluster, not reasoned about.
      #
      # The chart mounts this config and a reloader sidecar SIGHUPs the server when
      # the projected file changes. But nats-server refuses to hot-reload a whole
      # class of options: `diffOptions` (server/reload.go) switches on each changed
      # field and its `default:` arm returns "config reload not supported for %s".
      # `authcallout` has NO case, so ANY change to the auth_callout block lands
      # there — verified live with the issuer byte-identical on both sides and only
      # `auth_users` differing. JetStream's max memory / max store are refused the
      # same way, outside the switch. So this is not an auth quirk: it is a class.
      #
      # And the refusal is WHOLESALE. diffOptions returns an error and the caller
      # abandons the entire reload, so every other change in the same apply — the
      # account passwords, MQTT settings, JetStream ceilings — is discarded with it.
      # What the operator sees: tofu reports applied, the reloader reports it sent
      # the hangup, the ConfigMap shows the new values, and the running server is on
      # the config it booted with. The ONLY evidence is one [ERR] line in the
      # broker's own log. Every service then fails `nats: Authorization Violation`
      # against a ConfigMap that proves the credentials are right.
      #
      # Two things that breaks in the field, both observed:
      #   - a released fix to the auth block never reaches an EXISTING instance; the
      #     upgrade applies green and the broker keeps the old behaviour.
      #   - a bootstrap that dies between the infra step and the chart step leaves a
      #     live broker holding credentials no re-run will reuse (the reuse guard
      #     reads the instance chart's config, which does not exist yet) AND cannot
      #     adopt the new ones. Permanently wedged short of a manual restart.
      #
      # Setting this makes the chart stamp `checksum/config: sha256(<the rendered
      # ConfigMap>)` onto the pod template, so a config change rolls the StatefulSet
      # and the server always comes up on the file. Correctness by construction
      # rather than by remembering which fields this nats-server version can reload
      # — a set defined by someone else's `default:` arm, which moves between
      # releases and whose drift is invisible until it bites in production.
      #
      # THE RELOADER STAYS ENABLED, deliberately: the checksum covers only the
      # ConfigMap, and TLS material arrives in a separate Secret with only its PATHS
      # in the config. Certificates therefore still hot-reload without a broker
      # outage. (That path is broken today on any instance already carrying a
      # refused change, because each HUP re-diffs the whole config and bails again,
      # discarding the cert change with it — this repairs that too.)
      #
      # THE COST, stated plainly: config changes now roll the broker. Shutdown alone
      # is lameDuckGracePeriod + lameDuckDuration = 40s, and terminationGracePeriod
      # is 60s — the chart's own floor for it. Budget ~50-70s per pod: a full outage
      # at one server, a rolling restart at three. The change is not the duration but
      # the FREQUENCY: previously only a chart or image bump rolled these pods.
      configChecksumAnnotation = true

      # NO `tolerations` HERE, DELIBERATELY — which is not the same as having
      # forgotten them, and the difference is worth the paragraphs.
      #
      # Kubernetes' DefaultTolerationSeconds admission plugin injects
      # `tolerationSeconds: 300` for not-ready/unreachable onto every pod that does
      # not already carry one. So these servers run at 300s while the database tier
      # and every instance Deployment run at 30s (ADR-020 A1.4). That inconsistency
      # is real and it looks like an oversight. It was measured before it was left
      # alone; do not "fix" it for consistency.
      #
      # (Note also that this map has no `tolerations` KEY. The upstream chart's
      # podTemplate exposes configChecksumAnnotation, topologySpreadConstraints,
      # merge, patch and name — nothing else. Adding `tolerations` here is silently
      # ignored; it would have to go through `podTemplate.merge`, the same escape
      # hatch the cluster tls block above uses for `verify`.)
      #
      # THE DRILL. A node holding one of three servers was SIGKILLed on the HA rig,
      # with the JetStream META leader on it. What a client actually saw:
      #
      #   t0      → t0+8.8s   no acknowledged JetStream write. The JetStream API
      #                       itself was unanswered for the first 4.0s ("no
      #                       responders"); the meta leader was observed moved at
      #                       t0+6.23s and the stream's leader at t0+7.42s. The node
      #                       was still Ready and untainted for the whole of it —
      #                       Kubernetes had not yet noticed anything was wrong.
      #   t0+8.8s → t0+44.9s  writable again, but 16 of 37 new connections through
      #                       the ClusterIP still failed: the dead pod was STILL in
      #                       the dc-nats Service endpoints, so kube-proxy kept
      #                       picking it. No connection INITIATED after t0+44.9s
      #                       ever failed.
      #   t0+46.5s            node → Ready=Unknown, taint applied, pod Ready=False,
      #                       endpoints 3 → 2. Zero messages lost across the whole
      #                       drill: 1717 acknowledged publishes over a sequence span
      #                       of 1717, and the stream itself read back contiguous.
      #
      # WHY A SHORTER FUSE CANNOT HELP — and this is a mechanism, not the fact that
      # those events happened to land close together. `tolerationSeconds` is
      # evaluated by the taint manager FROM THE MOMENT THE NoExecute TAINT IS ADDED,
      # so t=0 for every possible value is t0+46.5s: 37.7s after JetStream was
      # writable again. The same node-lifecycle evaluation that applies that taint
      # also marks the pods on the node NotReady, which is what withdraws the
      # endpoint — so the readiness path cannot lag the taint. And even
      # `tolerationSeconds: 0` would only set a deletionTimestamp, and a terminating
      # pod is already out of the ready endpoints that readiness had just emptied.
      # There is nothing left for an earlier eviction to remove.
      #
      # AND NOTHING IS WAITING ON THE OTHER SIDE. This is a StatefulSet. At 300s the
      # pod was marked for deletion, went Terminating with its ORIGINAL UID, and NO
      # replacement was ever created — Kubernetes will not create a second dc-nats-1
      # while the first cannot be confirmed gone, because its kubelet is unreachable.
      # The StatefulSet sat at ready=2/3 until the node came back. Control from the
      # same drill, same instant, same 300s: the nats-box Deployment pod on that same
      # dead node WAS replaced and Running within the tick — so this is StatefulSet
      # semantics, not a broken cluster.
      #
      # WHAT THE DRILL DID NOT TEST, stated plainly because the honest reason to
      # leave this alone is "no measured benefit", not "measured harm":
      #   - The node was down 6m47s, longer than BOTH candidate fuses, so 30s and
      #     300s produce the identical outcome here — the pod was recreated (new UID)
      #     32s after the node returned either way. A fuse shorter than the outage
      #     but longer than nothing is only distinguishable when the node comes back
      #     between taint+30s and taint+300s, which was not exercised.
      #   - kind's local-path PV pinned this pod to the dead node. This module sets
      #     no storage class, so on a managed cloud the volume is network-attached
      #     and movable, AND the cloud-controller-manager DELETES the Node object on
      #     a real instance termination — which force-deletes its pods and lets the
      #     StatefulSet recreate elsewhere. `docker stop` cannot reproduce that path
      #     (the Node object persists). The conclusion still holds there, but by node
      #     deletion rather than by eviction; either way not by tolerationSeconds.
      #   - N=1, and the cheapest case: the killed server led 1 of 30 streams and
      #     held 0 client connections. 8.8s is a best-case election figure.
      #
      # IF THIS IS REVISITED, the lever that would move the 36s is the ENDPOINT, not
      # the eviction — either the control plane's node-monitor grace period (a
      # kube-controller-manager flag, not reachable from a workload and not settable
      # on most managed clusters), or giving clients the peer list so a reconnect
      # does not go back through kube-proxy. The chart defaults
      # `cluster.no_advertise` to true, so today they never learn it, and
      # core/messaging hands them a single ClusterIP URL. The tradeoff is that the
      # advertised URLs would be pod IPs, useless to an external MQTT client. Both
      # are separate decisions with their own costs.
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

  # --- What ha_topology reports -----------------------------------------------
  #
  # 🔴 READ BACK OUT OF chart_values, NOT RE-DERIVED FROM THE VARIABLES. This
  # direction is the whole point, and getting it backwards produced a check that
  # could not fail.
  #
  # ha_topology used to compute each of these a second time from var.ha /
  # var.enable_tls / local.clustered. That made it a COPY of the values above,
  # not a report of them, and hack/check-tofu-validations.sh asserts against the
  # output. Measured: setting `cluster.tls.enabled = false` in chart_values — a
  # clustered broker with route port 6222 open in plaintext to any unverified
  # peer, which is precisely the hole ADR-020 A0 closed — left
  # route_tls_verified reading `true` and the whole harness green. Same for
  # dropping stream_replicas to 1 and for cluster.replicas.
  #
  # Reading the map back means the assertions are downstream of the mutation:
  # edit the value Helm is handed and the output moves with it. The values feed
  # this local; this local feeds nothing back, so there is no cycle.
  #
  # route_tls_verified is a conjunction of THREE fields because all three are
  # required for the property it names, and each has failed independently in
  # review: clustering on (else there is no route port and the claim is vacuous),
  # TLS on the route listener (encryption), and verify (mutual authentication —
  # without it the listener accepts any peer, and a route peer bypasses the auth
  # callout entirely).
  reported = {
    cluster_replicas     = local.chart_values.config.cluster.replicas
    clustered            = local.chart_values.config.cluster.enabled
    mqtt_stream_replicas = local.chart_values.config.mqtt.merge.stream_replicas
    spread_constraints   = local.chart_values.podTemplate.topologySpreadConstraints
    # Read back for the same reason as the rest, and with a sharper edge: this one
    # is the difference between a config change reaching the running broker and
    # being silently discarded (see podTemplate above). A grep over this file can
    # confirm the line EXISTS but not that it is still nested under podTemplate,
    # and the chart ships no values.schema.json — so a key that drifts to the wrong
    # parent during a refactor is accepted in silence and simply does nothing.
    # Reading the nested path here turns that into a `tofu validate` failure
    # ("This object does not have an attribute named ..."), with no cluster, no
    # network and no plan.
    config_checksum_annotation = local.chart_values.podTemplate.configChecksumAnnotation
    route_tls_verified = (local.chart_values.config.cluster.enabled
      && local.chart_values.config.cluster.tls.enabled
    && local.chart_values.config.cluster.tls.merge.verify)
  }
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

  # client_auth as well as server_auth, because this ONE leaf plays both roles.
  # On the client and MQTT listeners the server presents it as a server; on the
  # 6222 route listener, with verify on, each server also presents it as a CLIENT
  # to its peers. Without the clientAuth EKU that handshake is rejected and the
  # cluster silently never forms — routes retry, JetStream never gets a meta
  # leader, and the symptom looks like a broken cluster rather than a bad cert.
  allowed_uses = [
    "server_auth",
    "client_auth",
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

  # 🔴 SIZED FOR A ROLLING RESTART, because podTemplate.configChecksumAnnotation
  # makes one a routine outcome of a config change rather than a rare event.
  #
  # The provider's default wait is 300s. One server costs ~50-70s to cycle
  # (lameDuckGracePeriod 10s + lameDuckDuration 30s on shutdown, then startup and a
  # readiness probe that waits 10s before its first check), and a StatefulSet rolls
  # them ONE AT A TIME, each new pod also waiting to rejoin the JetStream RAFT group
  # before it reports ready. Three servers therefore land close enough to 300s that a
  # slow meta-leader election decides it — and the failure mode is the nastiest kind:
  # the apply reports a timeout WHILE THE ROLL COMPLETES ANYWAY, so the operator is
  # told it failed and finds a healthy cluster. That is the exact mirror of the
  # false-green this annotation exists to remove, and it would be introduced BY the
  # fix. 900s is not a derivation — five servers at the worst declared per-pod cost
  # is ~350s — it is roughly 2.5x headroom over that, because the cost being
  # bounded is a RAFT rejoin, not a sleep.
  #
  # 🔴 THE COST, because it is not free and it is not confined to rolls: this
  # timeout governs EVERY waited operation on this release, so a genuinely wedged
  # install now takes 15 minutes to report instead of 5. That includes the failure
  # documented a few lines below — TLS material missing, pods stuck
  # ContainerCreating, helm waiting on a readiness that will never come. Slower
  # feedback on real breakage is the price of not fabricating failures on healthy
  # rolls; it is the right trade only because the false timeout would be
  # indistinguishable from the silent-divergence bug this file exists to close.
  #
  # This is a ceiling, not a delay: helm returns as soon as the release is ready.
  timeout = 900

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
      condition     = !local.contradictory
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

# The resolved HA topology, as data.
#
# Two purposes, and the second is why it is an output rather than a comment.
# An operator can `tofu output` it to see what was actually applied without
# reading the derivation. And it makes the values CI-checkable: every one of these
# decides whether HA is real, none of them is reachable from a variable
# validation, and a resource precondition does not run under `tofu validate`
# either — so before this, changing whenUnsatisfiable to ScheduleAnyway (which
# restores exactly the three-servers-on-one-node outcome A0 exists to prevent) or
# dropping mqtt_stream_replicas to 1 produced zero signal in any gate.
# hack/check-tofu-validations.sh asserts against it via `tofu console`.
#
# Deliberately carries no secret material, so it is safe to print.
output "ha_topology" {
  description = "The resolved HA topology: server count, whether clustering is on, the MQTT gateway's replica factor, whether route TLS is mutually verified, and the pod spread constraints. Verified in CI; safe to print."
  value = {
    cluster_replicas     = local.reported.cluster_replicas
    clustered            = local.reported.clustered
    mqtt_stream_replicas = local.reported.mqtt_stream_replicas
    route_tls_verified   = local.reported.route_tls_verified
    spread_constraints   = local.reported.spread_constraints
    # Whether a config change will actually be ADOPTED by the running broker
    # rather than applied, reported successful, and silently ignored. Asserted in
    # CI alongside the rest.
    config_checksum_annotation = local.reported.config_checksum_annotation
    # The ONE field here that is not a read-back, because it has no chart value
    # to read: it reports a contradiction in what was ASKED for, which the
    # precondition below refuses. Shared with that precondition through
    # local.contradictory so the two cannot drift apart.
    contradictory = local.contradictory
    # The names the server certificate is issued for. Exposed because route TLS
    # being ENABLED and route TLS actually WORKING are different facts, and the
    # difference cost a working cluster once: route_tls_verified read true while
    # every route handshake failed on a certificate that did not name the peers.
    # A config-level assertion could not see it; this is what makes the SANs
    # assertable without a cluster.
    server_dns_names = local.server_dns_names
  }
}

output "cluster_replicas" {
  description = "Number of NATS servers provisioned. This is the CEILING on the per-stream replica factor the services may ask for (instance.config.infrastructure.nats.streamReplicas, a Helm value this module does not set): a stream cannot be replicated wider than the cluster hosting it. dcctl's preflight reads this to refuse an install whose two halves disagree, instead of letting it come up looking replicated."
  value       = local.reported.cluster_replicas
}

output "tls_enabled" {
  description = "Whether the broker terminates TLS (drives the matching client-side flag in the instance config)."
  value       = var.enable_tls
}
