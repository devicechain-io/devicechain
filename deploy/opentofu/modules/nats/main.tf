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

locals {
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

  # https://github.com/nats-io/k8s — config.* maps onto nats-server config.
  #
  # NOTE: each tls/tlsCA block emits BOTH keys on both toggle states so the two
  # HCL branches have an identical type. A `? {enabled=true, secretName=x} :
  # {enabled=false}` shape unifies to map(string) and would coerce `enabled` to
  # the STRING "true"/"false", which chart 2.14.2's `ternary` on tls.enabled
  # rejects — failing the render in BOTH states. `secretName = ... : null` keeps
  # the value a real bool and drops the reference when off (chart default nil).
  values = [yamlencode({
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
        }
      }
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
      }
      cluster = {
        enabled  = var.ha
        replicas = var.ha ? 3 : 1
      }
    }
  })]
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
