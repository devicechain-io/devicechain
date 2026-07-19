# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# NATS is DeviceChain's single messaging runtime dependency (ADR-003/006/007): it
# carries core messaging, the JetStream KV cache + distributed lock, and the MQTT
# device ingress. Installed from the official chart; JetStream and MQTT are
# enabled, cluster size is driven by the HA toggle (single-node for the launch
# slice, 3-node RAFT under ADR-020).
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
  type    = string
  default = "8Gi"
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
  description = "Server-level max_file_store — the hard aggregate JetStream disk ceiling (ADR-023). Empty (default) derives it as 90% of jetstream_storage, FLOORED to a whole unit of that size's own magnitude (12Gi yields 10Gi, not 10.8Gi), leaving filesystem headroom so JetStream errors cleanly before the volume is 100% full. Set explicitly (e.g. \"6Gi\") to override; must be <= jetstream_storage."
  type        = string
  default     = ""
}

variable "ha" {
  type    = bool
  default = false
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
        }
      }
      cluster = {
        enabled  = var.ha
        replicas = var.ha ? 3 : 1
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
  }
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

output "tls_enabled" {
  description = "Whether the broker terminates TLS (drives the matching client-side flag in the instance config)."
  value       = var.enable_tls
}
