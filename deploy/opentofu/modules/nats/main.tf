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
# Transport security (ADR-025): when enable_tls is set, this module mounts the
# broker's TLS Secret -- a CA and a NATS-server leaf that dcctl mints and writes
# before the apply (see "TLS material" below; no key material passes through this
# module or its state) -- and turns on TLS for BOTH the 4222 client listener and
# the 1883 MQTT gateway. Server-auth only in v1 (no tls.verify) — clients verify
# the broker; device authentication is the separate auth-callout half of ADR-025.

# Named so hack/check-tofu-module-tests.sh pins them to the versions the roots run
# (it pins only the providers a module declares); the roots pin the versions.
terraform {
  required_providers {
    helm = {
      source = "hashicorp/helm"
    }
    kubernetes = {
      source = "hashicorp/kubernetes"
    }
  }
}

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
    nats chart version. An exact version is required; the validation block below
    refuses an empty value or a range, both of which resolve at apply time.

    🔴 PINNED BECAUSE THIS MODULE'S BEHAVIOUR IS A PROPERTY OF THE CHART, not just
    of the values passed to it. podTemplate.configChecksumAnnotation below relies on
    the chart hashing the rendered ConfigMap into the pod template; the reloader
    sidecar's cert watch-list is derived by the chart's own helpers; the lame-duck
    and terminationGracePeriod floors the timeout on helm_release.nats is sized
    against are chart defaults. All of those were read and verified at 2.15.1. On
    "latest" they are whatever upstream last shipped, and nothing here would notice
    them moving.

    2.14.4 -> 2.15.1 was checked that way: the two charts' templates are
    byte-identical (only Chart.yaml and the image tags in values.yaml differ), so
    the checksum template, the reloader's watch-list helpers, the pod labels and port
    names the MQTT NodePort Service and the PodMonitor select on, and the lame-duck
    (10s + 30s) and terminationGracePeriod (60s) defaults are all unchanged. The
    reloader image moves 0.23.0 -> 0.24.0 with dependency and build changes only.

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
  default     = "2.15.1"

  # Fail closed. An empty string used to mean "install latest", which made the chart
  # version resolve at APPLY time — the chart repository became a dependency of
  # planning, and a repo hiccup surfaced as the helm provider's "inconsistent final
  # plan ... .version: was known, but now unknown", naming neither the chart nor the
  # network. The regex also refuses a helm version RANGE ("4.15.1 - 5.0.0"), which is
  # a constraint resolved at apply time wearing a pin's clothes.
  validation {
    condition     = can(regex("^v?[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.]+)?$", var.chart_version))
    error_message = "chart_version must be an exact chart version (e.g. \"1.2.3\" or \"v1.2.3\"); an empty value or a version range is resolved at apply time, which is not a pin."
  }

  # 🔴 A FLOOR, BECAUSE THE VALUES BELOW ARE WRITTEN FOR 2.15. config.jetstream.merge
  # renders `limits { default_max_consumers }`, a key nats-server 2.14 does not know
  # and refuses at startup ("unknown field"), so pinning an older chart under this
  # module would not downgrade the broker, it would crash-loop it. The chart and
  # server versions move together (chart 2.15.1 ships nats-server 2.15.1). try() keeps
  # a malformed value on the clean message of the block above instead of a function
  # error here: OpenTofu before 1.10 evaluates every operand.
  validation {
    condition     = try(tonumber(regex("^v?([0-9]+)\\.([0-9]+)\\.", var.chart_version)[0]) * 1000 + tonumber(regex("^v?([0-9]+)\\.([0-9]+)\\.", var.chart_version)[1]) >= 2015, false)
    error_message = "chart_version must be 2.15.0 or later: this module renders nats-server 2.15 configuration (jetstream.limits.default_max_consumers), which an older server refuses at startup."
  }
}

# --- The broker image ---------------------------------------------------------
#
# 🔑 THE SERVER IS DEVICECHAIN'S OWN BUILD, NOT THE CHART'S DEFAULT IMAGE. It is
# upstream nats-server's main on the unmodified upstream library (the same version as
# the chart, kept in lockstep by hack/check-broker-version-skew.sh), plus one setting
# the config file cannot express: the MQTT listener REQUESTS, but does not require, a
# client certificate, so devices that authenticate by certificate prove their key in
# the TLS handshake while token and password devices are unaffected. The binary
# re-applies that setting on every reload (upstream's reload silently drops it) and
# watches it: dc_broker_mqtt_client_auth_request and the BrokerMqttClientAuthLost
# alert. Source: backend/broker/dc-nats-server.
#
# ROLLBACK, and what it costs. The upstream image still runs this module unchanged:
#   server_image_repository = "nats"
#   server_image_tag        = "2.15.2-alpine"
# The broker then requests no client certificate, so devices that authenticate by
# certificate cannot connect (token and password devices are unaffected), and
# BrokerMqttClientAuthLost fires to say so. It is a rollback, not a supported
# configuration.

variable "server_image_repository" {
  description = "Repository of the broker image, without a tag or digest. DeviceChain's own build by default; see the note above for the upstream rollback."
  type        = string
  default     = "ghcr.io/devicechain-io/dc-nats-server"

  validation {
    condition     = can(regex("^[a-z0-9]+([._-][a-z0-9]+)*(:[0-9]+)?(/[a-z0-9]+([._-][a-z0-9]+)*)*$", var.server_image_repository))
    error_message = "server_image_repository must be an image repository with no tag or digest (for example ghcr.io/devicechain-io/dc-nats-server); the tag goes in server_image_tag and the digest in server_image_digest."
  }
}

variable "server_image_tag" {
  description = <<-EOT
    Tag of the broker image. dcctl passes the DeviceChain release the instance runs,
    the same tag every service image carries. There is no usable default: a module
    that guessed one would install whatever that tag pointed at, so an empty tag with
    no digest is refused at plan time (the precondition on helm_release.nats).

    🔴 A new tag rolls the broker unless `server_image_digest` is set (dcctl sets it
    for its own release); with a digest, only a new digest rolls it. The image is part
    of the pod template, and the chart pulls repository@digest in place of the tag
    when a digest is given.
  EOT
  type        = string
  default     = ""

  validation {
    condition     = var.server_image_tag == "" || can(regex("^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$", var.server_image_tag))
    error_message = "server_image_tag must be a valid image tag: letters, digits, underscore, period and dash, at most 128, not starting with a period or a dash."
  }
}

variable "server_image_digest" {
  description = "Optional digest of the broker image (sha256:<64 hex>). When set, the chart pulls the image by digest and the tag is not used."
  type        = string
  default     = ""

  validation {
    condition     = var.server_image_digest == "" || can(regex("^sha256:[0-9a-f]{64}$", var.server_image_digest))
    error_message = "server_image_digest must be empty or sha256: followed by 64 lowercase hex digits."
  }
}

variable "jetstream_storage" {
  description = <<-EOT
    Size of the JetStream PV.

    This default is NOT a free choice — it must hold the platform's whole
    reservation. JetStream reserves each stream's MaxBytes UP FRONT at creation,
    so the disk floor is the SUM of the ceilings, and today that sum is
    11592Mi (11.32Gi): 9864Mi of platform streams, 384Mi of MQTT gateway stores, and 1344Mi of
    KV buckets. The ceiling that has to hold it is max_file_store, which is 90% of
    this value FLOORED to a whole unit of its own magnitude.

    PER NODE, not per cluster: a 3-server cluster provisions this volume three
    times and each server holds one copy of the reservation, so the size does NOT
    scale with cluster_replicas.

    It was 8Gi, which no longer fits: floor(8 × 0.9) = 7Gi, well under the 11.32Gi
    reserved, so a consumer using this module directly with its defaults would hit
    the "insufficient storage resources available" crashloop on the last services
    to create a stream. The root module always passes its own value explicitly, so
    the shipped path was never exposed — but a default that only works because
    every caller overrides it is a trap, not a default.

    Raising this REQUIRES checking the reservation still fits, and lowering it
    REQUIRES lowering the stream bounds first. Do NOT derive the PV as
    (sum of ceilings) / 0.9: the flooring makes that unsafe, since any sum above 10Gi
    yields a 12Gi PV whose ceiling is floor(12 × 0.9) = 10Gi — below the sum, and
    back to the crashloop. Pick the smallest whole magnitude where
    floor(magnitude × 0.9) >= the sum, and leave margin above it: 12Gi holds today's
    reservation with only 56Mi to spare, and 13Gi (an 11Gi ceiling) would leave 1080Mi,
    little more than the 512Mi headroom floor the budget test asserts. See nats_jetstream_storage in
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

variable "ca_cert_pem" {
  description = "PEM-encoded certificate of the authority that signed the broker's server certificate. Minted by dcctl, which also writes the TLS Secret the chart mounts -- only this PUBLIC half crosses into the infrastructure state. Empty when TLS is off."
  type        = string
  default     = ""
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

variable "callout_xkey_public" {
  description = <<-EOT
    Public curve key (X...) the broker ENCRYPTS every auth-callout request to
    (auth_callout.xkey). The request carries the connecting device's CONNECT username
    and password; without this it crosses $SYS.REQ.USER.AUTH in plaintext, readable by
    any login in the APP account that can subscribe there or capture the subject into a
    stream. Only the responder (device-management) holds the matching seed.

    EMPTY (the default) leaves the callout unencrypted, which is what every instance
    before this variable ran, and is what an upgrade passes for the first apply: the
    responder is rolled onto a build that can open encrypted requests BEFORE the broker is
    told to send them, so the broker roll never meets a responder that cannot read it.

    auth_callout cannot be hot-reloaded: changing this rolls the broker.
  EOT
  type        = string
  default     = ""

  validation {
    condition     = var.callout_xkey_public == "" || can(regex("^X[A-Z2-7]{55}$", var.callout_xkey_public))
    error_message = "callout_xkey_public must be a curve public key (X followed by 55 base32 characters) or empty; the bring-up mints it."
  }
}

variable "service_password_bcrypt" {
  description = "BCRYPT HASH ($2a$...) of the TRANSITIONAL shared static `dc_service` password, present only on an instance upgraded from a release that had one and not yet through the upgrade that retires it. It is what lands in the broker config; nats-server bcrypt-compares the plaintext each service presents. The plaintext is never rendered here. EMPTY (the default) renders no such user, which is every fresh install and every retired instance; then login_password_bcrypts must carry the logins the services present. Sensitive."
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

variable "identity_password_bcrypt" {
  description = <<-EOT
    BCRYPT HASH ($2a$...) of the `dc_identity` password: the one login of the IDENTITY account, which
    holds user-management's sign-in buckets (refresh tokens, OAuth and single sign-on codes, sign-in
    backoff). Only user-management holds the plaintext (a Secret only its Deployment mounts).

    WHY AN ACCOUNT. Those buckets hold bearer secrets. A login that can create JetStream streams can
    source, mirror or subject-capture a bucket into a stream of its own and read the copy, and the
    server writes the copy past every user permission, so a deny on the shared `dc_service` login
    closes nothing. A stream cannot cross an account boundary, and nothing else needs these buckets,
    so the account shares nothing: no import, no export.

    EMPTY (the default) renders no IDENTITY account: every instance before it existed, whose
    sign-in buckets stay in APP. An upgrade passes the hash on its first apply; the services then
    roll onto a user-management that opens the account.

    auth_callout cannot be hot-reloaded and the new login joins its auth_users, so setting this
    rolls the broker. Sensitive.
  EOT
  type        = string
  default     = ""
  sensitive   = true
}

variable "login_password_bcrypts" {
  description = <<-EOT
    BCRYPT HASHES ($2a$...) of the per-area broker logins and the broker administrator's, keyed by
    USERNAME (`dc-<area>-g<generation>`, `dc-broker-admin-g<generation>`). Each area holds only its own
    plaintext, in a Secret only its Deployment mounts; the administrator's is mounted nowhere yet.

    Every login lands in the APP account with the permissions block var.service_users generates for it:
    an area's login is held to the streams, durables, buckets and subjects the catalog declares for that
    area. They are exempt from the device callout like the other static logins.

    EMPTY (the default) renders none: every instance before they existed. An upgrade passes the hashes
    on its first apply, keeping `dc_service` beside them so the services that have not rolled yet still
    connect, and the upgrade after it stops passing service_password_bcrypt, which retires that login.

    auth_callout cannot be hot-reloaded and these join its auth_users, so setting this rolls the broker.
    Sensitive.
  EOT
  type        = map(string)
  default     = {}
  sensitive   = true

  validation {
    condition     = alltrue([for u, h in var.login_password_bcrypts : can(regex("^dc-[a-z0-9]+(-[a-z0-9]+)*-g[0-9]+$", u)) && startswith(h, "$2")])
    error_message = "login_password_bcrypts is keyed by username (dc-<principal>-g<generation>) and holds bcrypt hashes ($2a$...)."
  }
}

variable "service_users" {
  description = <<-EOT
    The GENERATED broker permissions of every static login, as one JSON document:
    `{"APP": {"<username>": <permissions>, ...}, "SYS": {"dc_sys": <permissions>}}`, where each
    <permissions> is a nats-server permissions block (`publish`/`subscribe` allow and deny lists and
    `allow_responses`). dcctl produces it from the same declarations the services are held to
    (`dcctl broker permissions` prints it), and this module renders each block into its user VERBATIM:
    the module holds no subject, so there is no second description of what a login may do.

    Required when enable_auth is true and any static login is rendered: a user rendered without a block
    would be a login with NO restriction (nats-server reads an absent block as allow-all), so a login
    with no entry here is refused at plan time, never defaulted.

    Not sensitive: it names subjects, never a credential.
  EOT
  type        = string
  default     = ""

  validation {
    condition     = var.service_users == "" || can(jsondecode(var.service_users))
    error_message = "service_users must be the JSON document dcctl generates (dcctl broker permissions), or empty."
  }
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

# --- Each server's resources ---------------------------------------------------
#
# The chart's default is no requests and no limits, which makes every server a
# BestEffort pod: the first the kubelet evicts when a node runs short of memory, and
# the lowest CPU weight on a busy node, for the process every event crosses.

variable "cpu_request" {
  description = <<-EOT
    CPU each NATS server requests. 500m by default; dcctl's --compact passes its own
    lowered request. Deliberately BELOW what a server uses under load: on GKE at
    6,000 events/s each of three servers used about 1.1 to 1.65 cores. Under ha on a pool
    of exactly three nodes, every node runs one server (the required spread below)
    whatever it requests, so a larger request would move nothing and take about four
    cores from what the services can be placed on. What 500m buys there is the
    Burstable QoS class instead of BestEffort and a CPU weight beside the services.
    On a larger pool, or without ha (one server, which then carries every event and
    was not measured), the request also steers where the scheduler puts other pods,
    and 500m understates the server's use. No CPU LIMIT is set: a limit would
    throttle the process every event crosses.
  EOT
  type        = string
  default     = "500m"

  validation {
    condition     = can(regex("^([0-9]+m|[0-9]+(\\.[0-9]+)?)$", var.cpu_request)) && !can(regex("^[0.]+m?$", var.cpu_request))
    error_message = "cpu_request must be a nonzero number of millicores (\"500m\") or cores (\"1\", \"1.5\")."
  }
}

variable "memory_request" {
  description = "Memory each NATS server requests, in Mi or Gi. 768Mi by default: the highest resident memory any server reached in any GKE run up to 9,200 events/s was 749 MiB, rounded up. A server using more than it requests is among the first the kubelet evicts under node memory pressure."
  type        = string
  default     = "768Mi"

  validation {
    condition     = can(regex("^[1-9][0-9]*(Mi|Gi)$", var.memory_request))
    error_message = "memory_request must be a nonzero whole number of Mi or Gi (e.g. \"768Mi\")."
  }
}

variable "memory_limit" {
  description = <<-EOT
    Memory limit of each NATS server, in Mi or Gi. GOMEMLIMIT is set to 80% of it, as
    the nats chart recommends, so the Go runtime collects harder before the kernel
    kills the server. 4Gi by default, set by the duplicate-detection budget: every
    server holds the message ids of each deduplicated stream's window, about 1.7 GiB
    at 10,000 events/s sustained, on top of the highest resident memory measured
    (830 MiB), and that total has to stay 15% under the 80% soft limit.
    TestDuplicateWindowsFitTheBrokerMemoryLimit (backend/core/messaging) holds the
    instance root's default to that. Those were steady-ingest runs: a server rejoining
    its cluster or catching up a large backlog after a node loss was not measured, so
    raise this if a server is ever OOM-killed. A limit reserves nothing on the node:
    the request is what places a server. Binary units only: GOMEMLIMIT is derived from
    the number, and a decimal unit would make it off by the factor between G and Gi.
  EOT
  type        = string
  default     = "4Gi"

  validation {
    condition     = can(regex("^[1-9][0-9]*(Mi|Gi)$", var.memory_limit))
    error_message = "memory_limit must be a nonzero whole number of Mi or Gi (e.g. \"2Gi\")."
  }
}

variable "route_compression" {
  description = <<-EOT
    S2 compression of the traffic BETWEEN the servers of a clustered broker (the
    routes on port 6222: stream and consumer replication, and client traffic that
    lands on a server other than the one leading its stream). nats-server's
    `cluster.compression`. Client connections and the JetStream store are not
    affected, and JetStream STREAM compression is deliberately not offered here: it
    rewrites every sealed block on every replica, adding disk writes and CPU.

    UNSET (null, the default) means s2_fast on a clustered broker and off on a
    single server, which has no routes: see local.route_compression. An explicit
    value always wins over that, "off" included.

    "off" renders NO compression key, leaving nats-server's own default for a
    cluster, `accept`: a server never initiates compression and only follows a peer
    that asks. Every server gets the same value from this module, so with "off" no
    route compresses, and the values Helm is handed are byte-identical to a module
    that did not have this variable.

    "s2_fast" is the mode to choose, and the clustered default. "s2_better" and
    "s2_best" trade more CPU for a smaller frame. "s2_auto" picks a level from each
    route's RTT, and with nats-server's default thresholds anything at or under
    10 ms is UNCOMPRESSED, which is every route inside one zone, so it does nothing
    there. Its thresholds are not exposed. Measured on a 3-server GKE cluster with
    route TLS on, at 18,000 events/s offered: s2_fast cut route bytes per event by
    60% and the broker pods' network bytes by 37%, and lowered broker CPU per event
    by about 4%: compression costs each server 2.6-2.9% of its CPU, and the smaller
    TLS writes repay more than that.

    Changing it ROLLS THE BROKER one server at a time (the config checksum
    annotation on podTemplate), although nats-server itself could reload it. A
    cluster part-way through the roll is safe: compression is negotiated per route,
    and a server left on the default `accept` follows a peer that asks, so a route
    compresses as soon as either end has a mode, and stops once both are back on
    "off" (nats-server 2.14.4 and 2.15.1, server.go selectCompressionMode).

    Only meaningful on a clustered broker. A mode SET on a single server, where the
    chart renders no cluster block at all, is refused rather than silently dropped.
  EOT
  type        = string
  default     = null

  validation {
    # A conditional, not `||`: OpenTofu before 1.10 evaluates both operands of
    # `||`, and contains() refuses a null value.
    condition     = var.route_compression == null ? true : contains(["off", "s2_fast", "s2_better", "s2_best", "s2_auto"], var.route_compression)
    error_message = "route_compression must be one of off, s2_fast, s2_better, s2_best, s2_auto (lowercase, as written), or left unset."
  }
}

locals {
  # Service name the internal services present (see natsauth.ServiceUser). Kept in
  # sync with the Go constant; a mismatch would lock every service out.
  service_user = "dc_service"

  # The shared login exists only while its hash is supplied. Fresh installs and retired instances
  # supply none, so the user, its auth_users entry and its permissions entry are all absent together.
  shared_login_enabled = var.service_password_bcrypt != ""
  shared_users         = local.shared_login_enabled ? [{ user = local.service_user, password = var.service_password_bcrypt, permissions = try(local.service_app_permissions[local.service_user], null) }] : []

  # 🔴 THIS MODULE HOLDS NO SUBJECT. Every static login's permissions block comes from
  # var.service_users, which dcctl generates from the stream and bucket declarations the services
  # are held to (natsauth.ServiceUsers), and is rendered into the user unchanged. Before, the
  # device-events deny and dc_sys's allow-lists were written here as literals and a Go test read this
  # file to keep them in step; now there is one description and this renders it.
  #
  # An ABSENT block is the dangerous case (nats-server reads it as allow-all), so a login without an
  # entry is refused by the precondition on helm_release.nats rather than rendered.
  service_permissions     = try(jsondecode(var.service_users), {})
  service_app_permissions = try(local.service_permissions.APP, {})
  service_sys_permissions = try(local.service_permissions.SYS, {})

  # System-account login (see natsauth.SysUser), kept in sync with the Go constant
  # the same way. Rendered only when a hash was supplied: an account block with an
  # empty user list is what SYS looked like before the presence tap, and is what an
  # instance that has not minted the credential must keep looking like. Its allow
  # list (cluster ping, one server's CONNZ and KICK, the connect/disconnect advisories and its
  # reply inboxes) is generated like every other (natsauth.SysPermissions).
  sys_user = "dc_sys"

  # The users the precondition holds to an entry in var.service_users.
  static_app_users = concat(local.shared_login_enabled ? [local.service_user] : [], nonsensitive(keys(var.login_password_bcrypts)))
  static_sys_users = var.sys_password_bcrypt == "" ? [] : [local.sys_user]
  missing_permissions = concat(
    [for u in local.static_app_users : u if !contains(keys(local.service_app_permissions), u)],
    [for u in local.static_sys_users : u if !contains(keys(local.service_sys_permissions), u)],
  )
  # The IDENTITY account (see var.identity_password_bcrypt): its login, and the one account block
  # it adds. Kept in sync with natsauth.IdentityUser / natsauth.IdentityAccount (a Go test reads
  # this file). It is rendered only when a hash was supplied, so an instance that has not minted the
  # credential renders exactly what it did before. `jetstream = "enabled"` and nothing else, for
  # the reason the APP account's comment gives: explicit account limits bill a reservation times its
  # replica count and fail at a third of the number written; the server's max_file_store is the ceiling.
  identity_user    = "dc_identity"
  identity_account = "IDENTITY"
  identity_enabled = var.identity_password_bcrypt != ""
  identity_accounts = local.identity_enabled ? {
    (local.identity_account) = {
      jetstream = "enabled"
      users     = [{ user = local.identity_user, password = var.identity_password_bcrypt }]
    }
  } : {}

  # The per-area logins (see var.login_password_bcrypts), each with its own generated block.
  # Terraform iterates a map in key order, so the rendered users are stable across runs.
  login_users = [for u, h in var.login_password_bcrypts : { user = u, password = h, permissions = try(local.service_app_permissions[u], null) }]

  sys_users = var.sys_password_bcrypt == "" ? [] : [{
    user        = local.sys_user
    password    = var.sys_password_bcrypt
    permissions = try(local.service_sys_permissions[local.sys_user], null)
  }]

  # js_max_file_store resolves the server-level max_file_store (see the
  # jetstream_max_file_store variable and the fileStore.maxSize wiring below). When
  # not overridden, it is 90% of the PVC size: split jetstream_storage into its
  # numeric magnitude and unit (e.g. "8Gi" -> 8 + "Gi"), floor 90% of the magnitude,
  # and reattach the unit ("8Gi" -> "7Gi"). The 10% left over is filesystem headroom
  # so JetStream stops accepting data before the volume is physically full.
  js_size_magnitude = tonumber(regex("^[0-9.]+", var.jetstream_storage))
  js_size_unit      = regex("[A-Za-z]+$", var.jetstream_storage)
  js_max_file_store = var.jetstream_max_file_store != "" ? var.jetstream_max_file_store : "${floor(local.js_size_magnitude * 0.9)}${local.js_size_unit}"

  # Memory in MiB, from values the validations above hold to Mi or Gi. Converted
  # BEFORE the 80% is taken: 80% of the magnitude of "2Gi" floors to 1, a 1 GiB soft
  # limit under a 2 GiB hard one (the magnitude trap js_max_file_store's comment
  # describes, which flooring there costs only headroom).
  memory_request_mib = tonumber(regex("^[0-9]+", var.memory_request)) * (endswith(var.memory_request, "Gi") ? 1024 : 1)
  memory_limit_mib   = tonumber(regex("^[0-9]+", var.memory_limit)) * (endswith(var.memory_limit, "Gi") ? 1024 : 1)
  go_mem_limit       = "${floor(local.memory_limit_mib * 0.8)}MiB"

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

  # Route compression as the chart's cluster `merge`, which the chart lays over the
  # cluster{} stanza it generates. EMPTY when the mode in effect is off, so the
  # cluster map, and with it the values document and the rendered config, are what
  # they were before this knob existed. (A clustered broker that leaves the knob
  # unset is NOT off: it gets s2_fast, so the first apply after that default
  # arrived rolls its servers once.)
  #
  # A filtered for-expression rather than `off ? {} : {merge = ...}`: an HCL
  # conditional only unifies an empty object with the other branch while that
  # branch happens to convert to a map, so adding a second key of another shape
  # later would turn it into a type error. This form has one shape either way.
  route_compression_merge = {
    for k, v in { merge = { compression = local.route_compression } } : k => v
    if local.route_compression != "off"
  }
  # The mode in effect: the one asked for, or, when none was, s2_fast on a
  # clustered broker and off on a single server. An explicit "off" is a value
  # and wins like any other.
  route_compression = var.route_compression != null ? var.route_compression : (local.clustered ? "s2_fast" : "off")
  # A mode asked of a broker that has no routes. The chart renders no cluster
  # block for a single server, so the value would be dropped without a word; the
  # precondition on helm_release.nats refuses it instead. Only an explicit mode
  # can trip it: unset resolves to off on a single server.
  compression_without_routes = local.route_compression != "off" && !local.clustered

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
  # so the platform's ~9.9Gi of stream and bucket ceilings bills as ~29.6Gi at R3.
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
    accounts = merge({
      APP = {
        jetstream = "enabled"
        users     = concat(local.shared_users, local.login_users)
      }
      # SYS carries a user only once the presence tap's credential has been minted.
      # The system account needs no `jetstream` and no exports: the advisories the
      # tap reads and the CONNZ request it makes are both served INSIDE SYS, and a
      # subscriber there is a plain core-NATS client.
      SYS = { users = local.sys_users }
    }, local.identity_accounts)
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
      #
      # xkey is the responder's CURVE public key: with it the broker seals each request
      # to the responder and the device's CONNECT password never crosses the subject in
      # the clear. It is merged in only when set, so an empty value renders the block
      # exactly as before (an `xkey: ""` would be a different config, not an absent one).
      auth_callout = merge({
        issuer = var.callout_issuer_public
        # dc_identity is listed when its account exists, for the reason this list exists at all: a login
        # absent from it is handed to the DEVICE callout, which refuses it.
        auth_users = concat(local.shared_login_enabled ? [local.service_user] : [], [local.sys_user], local.identity_enabled ? [local.identity_user] : [], [for l in local.login_users : l.user])
        account    = "APP"
      }, var.callout_xkey_public == "" ? {} : { xkey = var.callout_xkey_public })
    }
  }
  tls_secret_name       = "${var.release_name}-tls"
  tls_ca_configmap_name = "${var.release_name}-ca"
  # 🔴 THE ROUTE LISTENER DOES NOT USE THE CLIENT NAMES, and getting this wrong
  # does not degrade the cluster — it prevents one from forming at all.
  #
  # Clients reach the broker through the ClusterIP Service (dc-nats.<namespace>).
  # The servers reach EACH OTHER by POD name through the chart's headless
  # Service — dc-nats-1.dc-nats-headless — because a route has to address one
  # specific peer, which a load-balanced Service name cannot do. With route TLS
  # verification on (which is the point: without it any pod that can reach 6222
  # joins the cluster as a peer and reads every account), each server verifies the
  # name it dialled against its peer's certificate. A leaf carrying only the
  # client names fails EVERY route handshake:
  #
  #   TLS route handshake error: x509: certificate is valid for dc-nats,
  #   dc-nats.<namespace>, ... not dc-nats-1.dc-nats-headless
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

  # The broker binary's metrics port and its name, which the instance chart's PodMonitor
  # selects (deploy/helm/devicechain/templates/podmonitor-nats.yaml). 7777 is the
  # exporter sidecar's.
  broker_metrics_port_name = "dc-metrics"
  broker_metrics_port      = 7778

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
        # 🔴 WITHOUT THIS, THE 1,001st MQTT DEVICE CANNOT RECEIVE COMMANDS.
        #
        # nats-server 2.15 caps every stream at 1,000 consumers unless the stream or
        # the account sets its own limit, and the MQTT gateway's $MQTT_msgs stream
        # sets neither (the broker creates it, not DeviceChain, and the APP account's
        # JetStream limits are dynamic). Each QoS 1 or 2 MQTT SUBSCRIBE is a durable
        # consumer on that stream, so a fleet holding more than 1,000 such
        # subscriptions has the next one refused. Measured on 2.15.1: the 1,001st
        # device's QoS 1 subscription is answered with SUBACK return code 0x80 while
        # its CONNECT succeeds, so the device stays connected and simply never gets a
        # command; with -1 all 1,010 subscribed.
        #
        # -1 is the documented way back to the 2.14 behaviour of no per-stream
        # default. A finite value would be a fleet-size ceiling chosen by accident.
        # The platform's own streams are unaffected either way: their consumers are
        # a handful per service, not per device.
        merge = {
          limits = {
            default_max_consumers = -1
          }
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
      # Route compression (local.route_compression) is merged in as the cluster
      # block's own `merge`, and only when a mode is in effect: see route_compression_merge.
      cluster = merge({
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
      }, local.route_compression_merge)
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
      #
      # A RETURNING SERVER IS ITSELF A DISRUPTION, and nothing in this module can
      # change that. NATS RAFT has no pre-vote: a server cut off from its peers keeps
      # campaigning and raising its term, and when it can reach them again, an
      # AppendEntry response or a vote request carrying that higher term makes every
      # healthy leader it touches step down ("Detected another leader with higher
      # term, will stepdown"). Measured on 2.14.4, an 85s network partition of one
      # of three nodes: about 45s after the partition healed, dozens of stream and
      # consumer groups re-elected at once and JetStream answered "no responders" /
      # "temporarily unavailable" for ~6s. The ingress refused 80 publishes, a
      # handful of in-flight deliveries waited out one AckWait (~53s) before
      # redelivery, nothing was lost, and every client re-bound on its own. The
      # upstream 2.15.0 and 2.15.1 sources have the same two step-down paths (the higher-term
      # branch of processAppendEntryResponse, and processVoteRequest), so no
      # release to date removes it. Treat a node's RETURN like its loss when
      # planning maintenance; the operator-facing account is in the bootstrap
      # docs' node-loss section.
    }
    promExporter = {
      enabled = var.enable_prom_exporter
    }
    # The server container's resources and environment. Chart 2.15.1 renders
    # .Values.container.resources and .Values.container.env (a name -> value map)
    # into the nats container (files/stateful-set/nats-container.yaml). Not under
    # podTemplate: that map has no resources key, and the chart has no values schema,
    # so a key under the wrong parent would be accepted and do nothing.
    # ha_topology reads this path back out (see reported below), and
    # tests/resources.tftest.hcl reads it out of the values Helm is handed.
    container = {
      # DeviceChain's broker build (see server_image_repository). The chart's
      # nats.image helper uses the digest in place of the tag when one is set.
      image = {
        repository = var.server_image_repository
        tag        = var.server_image_tag
        digest     = var.server_image_digest == "" ? null : var.server_image_digest
      }
      resources = {
        requests = { cpu = var.cpu_request, memory = var.memory_request }
        limits   = { memory = var.memory_limit }
      }
      env = { GOMEMLIMIT = local.go_mem_limit }
      # The broker binary's own /metrics (dc_broker_info and the client-certificate
      # gauge), on a NAMED container port so the instance chart's PodMonitor selects it
      # the way it selects the exporter. Chart 2.15.1 renders only its own protocol
      # ports and has no extra-ports value, so the port is appended with the container
      # JSON patch, which files/stateful-set/nats-container.yaml applies. It is declared
      # under the upstream rollback image too, where nothing listens on it: the scrape
      # target is then down, which is accurate.
      patch = [{
        op    = "add"
        path  = "/ports/-"
        value = { name = local.broker_metrics_port_name, containerPort = local.broker_metrics_port }
      }]
      # 🔴 THE SERVER RUNS AS ROOT, AS THE UPSTREAM IMAGE DOES. The DeviceChain image is
      # built on the Chainguard static base, whose default user is 65532, and a broker
      # that changed user on its first roll could not open the JetStream store the
      # root-run upstream server wrote: nats-server creates it 0700 and its files 0600,
      # owned by root, and fsGroup does not help, because it only adds group access on
      # the volume types the kubelet relabels (not hostPath, which kind's local-path
      # provisioner uses, nor most NFS). Measured on the upgrade drill: dc-nats-0
      # crash-looped with "storage directory is not writable". The broker's change from
      # upstream is meant to be packaging only, so the user stays upstream's; running it
      # unprivileged is a separate change that has to migrate existing stores first.
      merge = {
        securityContext = {
          runAsUser  = 0
          runAsGroup = 0
        }
      }
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
    # Each server's resources, read back for the same reason: a resources map moved
    # to the wrong parent is accepted by the chart in silence.
    resources    = local.chart_values.container.resources
    go_mem_limit = local.chart_values.container.env.GOMEMLIMIT
    # Read back for the same reason: the chart has no values schema, so a limits
    # block under the wrong parent is accepted and the 1,000-consumer default
    # silently returns.
    default_max_consumers = local.chart_values.config.jetstream.merge.limits.default_max_consumers
    route_tls_verified = (local.chart_values.config.cluster.enabled
      && local.chart_values.config.cluster.tls.enabled
    && local.chart_values.config.cluster.tls.merge.verify)
    # The mode the routes are configured with, read back from the cluster merge.
    # Absent is "off" (nats-server's own `accept`), which is what the module renders
    # for off. A mode that drifts to the wrong parent reads back as "off" here, so a
    # check that asks for s2_fast fails instead of reporting a setting that is not
    # in the values.
    route_compression = try(local.chart_values.config.cluster.merge.compression, "off")
    # The broker image and the user its server runs as, read back for the same reason as
    # the rest: the chart ships no values schema, so an image map or a securityContext
    # under the wrong parent is accepted in silence, and then the chart's own upstream
    # image, or a server that cannot open its JetStream store, is what runs.
    server_image = local.chart_values.container.image
    run_as_user  = local.chart_values.container.merge.securityContext.runAsUser
  }
}

# --- TLS material (ADR-025) --------------------------------------------------
# A self-signed CA → a NATS-server leaf signed by it. A CA→leaf chain (rather than
# a bare self-signed server cert) lets the leaf rotate on apply without clients
# re-trusting a new root, and gives each node of an HA cluster its own leaf from
# the shared CA (ADR-020).

# 🔴 THE CERTIFICATE AUTHORITY IS NOT CREATED HERE ANY MORE, AND THE PRIVATE KEY IS
# THE REASON. This module used to hold `tls_private_key.ca` -- which put the key that
# signs every broker certificate into the infrastructure state IN CLEARTEXT, in a file
# on the operator's machine and in every copy of it. That is the one confirmed
# disclosure the credential cutover exists to close.
#
# dcctl mints the authority and the leaf now and writes the TLS Secret the chart
# mounts (config.*.tls.secretName -> local.tls_secret_name) before this apply runs.
# What crosses into this module is `ca_cert_pem`: the PUBLIC half, which anyone may
# hold, and which the CA-only ConfigMap below and the ca_pem output both serve.
#
# 🔑 THAT ALSO REVERSED AN ARROW. The CA used to be an OUTPUT the bring-up read after
# the apply, which meant the instance configuration document could not be composed
# until the apply had finished. It is an INPUT now, so the document is complete before
# anything is applied.
#
# 🔴 WHAT THIS MODULE LOST WITH THEM IS RENEWAL. `tls_locally_signed_cert` carried
# `early_renewal_hours = 720`, so every apply re-issued the leaf inside its last 30
# days. Nothing here replaces that; dcctl does, in the verb that evolves a live
# instance: `dcctl upgrade` re-issues the leaf inside its last 30 days under the
# authority it kept in the dc-nats-ca-keypair Secret, and restarts the broker
# (backend/cli/bootstrap/natsrenew.go). None of it passes through this module, which
# receives the public CA and nothing else. The case still uncovered is an instance
# nobody upgrades; cert-manager taking over leaf issuance is what would close that.

resource "kubernetes_config_map_v1" "nats_ca" {
  count = var.enable_tls ? 1 : 0

  metadata {
    name      = local.tls_ca_configmap_name
    namespace = var.namespace
  }

  data = {
    "ca.crt" = var.ca_cert_pem
  }
}

resource "helm_release" "nats" {
  max_history = local.helm_max_history

  name       = var.release_name
  namespace  = var.namespace
  repository = "https://nats-io.github.io/k8s/helm/charts/"
  chart      = "nats"
  version    = var.chart_version

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

  # The server pods mount the CA ConfigMap at startup; create it first so the STS
  # is not stuck ContainerCreating on a missing one (helm wait would time out).
  #
  # 🔴 THE TLS SECRET USED TO BE IN THIS LIST AND NO LONGER CAN BE -- dcctl writes it
  # before this apply, so there is no resource here to depend ON. The ordering it
  # guaranteed is now dcctl's: it writes every credential between creating the
  # namespace and running this apply, and fails the run rather than applying without
  # them. A `data` source would restore the edge and reintroduce exactly what moving
  # the material out was for, because a data source stores what it reads.
  depends_on = [
    kubernetes_config_map_v1.nats_ca,
  ]

  # https://github.com/nats-io/k8s — config.* maps onto nats-server config. The
  # rendered values (TLS listeners + optional broker-auth) are built in locals
  # above; see local.chart_values_encoded for the auth toggle.
  values = [local.chart_values_encoded]

  lifecycle {
    # The broker image must name a version. An empty tag would render "repository:"
    # (which Kubernetes reads as :latest), installing whatever that pointed at today.
    precondition {
      condition     = var.server_image_tag != "" || var.server_image_digest != ""
      error_message = "nats server_image_tag (or server_image_digest) is required: it names the broker build to run, which dcctl sets to the DeviceChain release. An empty one would pull whatever the repository's latest image is."
    }

    # Fail closed on a manual apply that turns auth on without minting the
    # credentials (the bring-up always provides them together): an empty issuer /
    # password would render a broker that rejects every device and every service.
    precondition {
      condition     = !var.enable_auth || (var.callout_issuer_public != "" && (var.service_password_bcrypt != "" || length(var.login_password_bcrypts) > 0))
      error_message = "nats enable_auth=true requires a non-empty callout_issuer_public and at least one login for the services to present: login_password_bcrypts, or the transitional service_password_bcrypt (mint them via dcctl / the genauth helper). Without either, every service would be refused."
    }

    # An identity login without the broker auth it belongs to would be a hash in a config that
    # authenticates nobody, and the operator reading it would believe the buckets were isolated.
    precondition {
      condition     = var.identity_password_bcrypt == "" || var.enable_auth
      error_message = "nats identity_password_bcrypt is set but enable_auth is false: there is no broker authentication for the identity account to separate."
    }

    # Logins without the broker auth they belong to would be hashes in a config that authenticates
    # nobody, and the operator reading it would believe each area had a credential of its own.
    precondition {
      condition     = length(var.login_password_bcrypts) == 0 || var.enable_auth
      error_message = "nats login_password_bcrypts is set but enable_auth is false: there is no broker authentication for the per-area logins to belong to."
    }

    # A login rendered without its permissions block would authenticate with NO restriction.
    precondition {
      condition     = !var.enable_auth || length(local.missing_permissions) == 0
      error_message = "nats service_users has no permissions for the static login(s) ${join(", ", local.missing_permissions)}. nats-server reads a user with no permissions block as unrestricted, so the module refuses to render one: pass the generated document (dcctl broker permissions)."
    }

    # An xkey without the callout it encrypts is a config the broker would carry for nothing,
    # and the operator reading it would believe device credentials were protected.
    precondition {
      condition     = var.callout_xkey_public == "" || var.enable_auth
      error_message = "nats callout_xkey_public is set but enable_auth is false: there is no auth callout to encrypt."
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

    # A route compression mode on a broker with no routes. The chart renders no
    # cluster block for a single server, so the mode would be accepted and dropped.
    precondition {
      condition     = !local.compression_without_routes
      error_message = "nats route_compression=${local.route_compression} compresses the traffic between clustered servers, and this broker is a single server with no routes to compress. Set it to off or leave it unset, or cluster the broker (ha=true or cluster_replicas=3)."
    }

    # Kubernetes refuses a pod whose memory request is above its limit, and the
    # StatefulSet would wait out the 900 s timeout before the apply said anything.
    precondition {
      condition     = local.memory_request_mib <= local.memory_limit_mib
      error_message = "nats memory_request (${var.memory_request}) is above memory_limit (${var.memory_limit}); Kubernetes refuses such a pod. Raise the limit or lower the request."
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
  value       = var.enable_tls ? var.ca_cert_pem : ""
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
# The broker image the chart is handed and the user its server runs as, read back
# from the values (local.reported). Asserted by tests/server_image.tftest.hcl.
output "server_image" {
  description = "The broker image (repository, tag, digest) handed to the chart, and the user its server container runs as. Safe to print."
  value = {
    image       = local.reported.server_image
    run_as_user = local.reported.run_as_user
  }
}

output "ha_topology" {
  description = "The resolved HA topology: server count, whether clustering is on, the MQTT gateway's replica factor, whether route TLS is mutually verified, the route compression mode, the pod spread constraints, and each server's resources. Verified in CI; safe to print."
  value = {
    cluster_replicas     = local.reported.cluster_replicas
    clustered            = local.reported.clustered
    mqtt_stream_replicas = local.reported.mqtt_stream_replicas
    route_tls_verified   = local.reported.route_tls_verified
    # The route compression mode the servers are configured with: "off" when the
    # values carry none (nats-server's default, under which no route compresses).
    route_compression  = local.reported.route_compression
    spread_constraints = local.reported.spread_constraints
    # Whether a config change will actually be ADOPTED by the running broker
    # rather than applied, reported successful, and silently ignored. Asserted in
    # CI alongside the rest.
    config_checksum_annotation = local.reported.config_checksum_annotation
    # Each server's requests and limits, and the GOMEMLIMIT derived from the limit.
    resources    = local.reported.resources
    go_mem_limit = local.reported.go_mem_limit
    # The per-stream consumer default handed to every server (-1 = none). See
    # config.jetstream.merge for why it is not left to nats-server.
    default_max_consumers = local.reported.default_max_consumers
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
