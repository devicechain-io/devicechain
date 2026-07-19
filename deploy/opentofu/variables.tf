# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# --- Cluster access -------------------------------------------------------------

variable "kubeconfig_path" {
  description = "Path to the kubeconfig for the target cluster."
  type        = string
  default     = "~/.kube/config"
}

variable "kubeconfig_context" {
  description = "kubeconfig context to use. Empty uses the current-context."
  type        = string
  default     = ""
}

variable "namespace" {
  description = "Namespace for the DeviceChain infrastructure dependencies. Services default to reaching them at <name>.<namespace> (e.g. dc-nats.dc-system)."
  type        = string
  default     = "dc-system"
}

variable "create_namespace" {
  description = "Whether this root creates the namespace (set false if it is managed elsewhere)."
  type        = bool
  default     = true
}

# --- High availability ----------------------------------------------------------

variable "ha" {
  description = "Reserved for the ADR-020 HA topology (3-node NATS RAFT, replicated streams, DB replicas). Defaults to single-node for the launch slice; the modules read it so HA becomes a toggle, not a rewrite."
  type        = bool
  default     = false
}

# --- NATS (messaging + MQTT + JetStream KV, ADR-003/006/007) --------------------

variable "nats_chart_version" {
  description = "Version of the nats Helm chart to install. Empty installs the latest; PIN to a tested version for reproducible deploys."
  type        = string
  default     = ""
}

variable "nats_jetstream_storage" {
  description = <<-EOT
    PersistentVolume size for NATS JetStream file storage. The default must fit the
    platform's OWN stream set on a cold start: every per-suffix stream reserves its
    byte ceiling UP FRONT at creation, against max_file_store (90% of this, FLOORED
    to whole units of the size's own magnitude — 12Gi yields 10Gi, not 10.8Gi). The
    platform creates 15 such streams — a FIXED set (streams are per-suffix and cover
    every tenant via the wildcard subject), so this does not grow with tenant count.

    Sizing history, because getting this wrong fails in a confusing way: 8Gi (→7Gi
    ceiling) fit only ~7 streams at the old uniform 1 GiB bound, so a fresh bring-up
    crashlooped its last stream-creating services (device-management,
    event-processing, event-sources) with "insufficient storage resources available".
    That was first fixed by raising the PV to 32Gi, which worked but spent most of it
    on control-plane streams that never hold more than a few MiB.

    The bound is now split hot/cold in backend/core/streams (see streams.All): 7 hot
    streams at 1 GiB + 8 control-plane streams at 128 MiB reserve 8Gi. The MQTT
    gateway's own streams — which nats-server creates UNBOUNDED, and which the
    platform bounds at startup so they cannot eat the rest — add 384Mi, for ~8.4Gi
    total. The KV buckets are bounded on the same principle (see kv.All) and reserve
    a further 768Mi, for ~9.1Gi. That fits 12Gi (→10Gi ceiling) with the ingest
    streams keeping their FULL buffer, and ~896Mi genuinely unreserved — which now
    covers only the two MQTT stores deliberately left unbounded and JetStream's own
    per-consumer state, rather than the open-ended KV growth it used to have to
    absorb.

    NEVER shrink this independently of the stream bounds, and do NOT size the PV as
    (sum of stream ceilings) / 0.9 — the flooring above makes that formula unsafe.
    A 9.5Gi sum / 0.9 rounds to an 11Gi PV, whose ceiling is floor(11 × 0.9) = 9Gi,
    which is BELOW the sum and brings the crashloop back. Pick the smallest whole
    magnitude where floor(magnitude × 0.9) >= the sum instead. Raise it for real
    ingest volume.

    CAVEAT — this 12Gi and the Go-side budget tests are NOT linked. Those tests carry
    their own `pvGi` constant (backend/core/config/instance_test.go), which mirrors
    this default and the flooring rule above, so lowering the default here will not
    make them fail; they guard the stream bounds against a fixed budget, not against
    this variable. Changing this value means updating that constant BY HAND. Wiring
    the two together (or asserting the reservation at bring-up) is the real fix.
  EOT
  type        = string
  default     = "12Gi"
}

variable "nats_jetstream_max_file_store" {
  description = "Server-level max_file_store — the hard aggregate JetStream disk ceiling (ADR-023). Empty derives it as 90% of nats_jetstream_storage, FLOORED to a whole unit of that size's own magnitude (12Gi yields 10Gi, not 10.8Gi), leaving filesystem headroom; set explicitly to override. Must be <= nats_jetstream_storage."
  type        = string
  default     = ""
}

variable "nats_enable_tls" {
  description = "Terminate TLS on the NATS client + MQTT listeners (ADR-025). Keep in sync with the services' instance config: when true the bring-up must thread the nats_ca output into infrastructure.nats.tls so clients dial over TLS. Set false only for plaintext debugging."
  type        = bool
  default     = true
}

variable "nats_enable_auth" {
  description = "Enable broker authentication + the device auth-callout (ADR-025). Defaults false because it needs credentials minted out-of-band (nkeys aren't a TF primitive); the bring-up (dcctl / up.sh) mints them, sets this true, and threads the corresponding plaintext credential into the services' instance config (the broker gets the bcrypt hash). The broker flag and the client flag MUST agree."
  type        = bool
  default     = false
}

variable "nats_callout_issuer_public" {
  description = "Public account nkey (A...) the auth-callout signs device user JWTs with. Minted by the bring-up; required when nats_enable_auth is true."
  type        = string
  default     = ""
}

variable "nats_service_password_bcrypt" {
  description = "BCRYPT HASH ($2a$...) of the shared `dc_service` password, placed in the broker config (the plaintext goes only into the services' instance-config Secret; the broker bcrypt-compares). Minted by the bring-up; required when nats_enable_auth is true. Sensitive."
  type        = string
  default     = ""
  sensitive   = true
}

# --- Relational Postgres (control-plane RDB) ------------------------------------

variable "postgres_image" {
  description = "Container image for the relational Postgres database."
  type        = string
  default     = "postgres:16-alpine"
}

variable "postgres_database" {
  description = "Initial database name for the relational Postgres."
  type        = string
  default     = "devicechain"
}

variable "postgres_username" {
  description = "Superuser/username for the relational Postgres."
  type        = string
  default     = "devicechain"
}

variable "postgres_password" {
  description = "Password for the relational Postgres. Override for any non-local deploy (use a tfvars file or a pre-created Secret)."
  type        = string
  default     = "devicechain"
  sensitive   = true
}

variable "postgres_storage" {
  description = "PersistentVolume size for the relational Postgres."
  type        = string
  default     = "8Gi"
}

variable "postgres_storage_class" {
  description = "StorageClass for the relational Postgres data volume. Empty uses the cluster default (often reclaimPolicy Delete). FOR PRODUCTION DURABILITY set this to a StorageClass whose reclaimPolicy is Retain, so the underlying volume survives PVC/PV deletion and a redeploy can re-attach the data."
  type        = string
  default     = ""
}

# --- Ingress + TLS (ADR-002) ----------------------------------------------------

variable "enable_ingress_nginx" {
  description = "Install the NGINX ingress controller. Set false if the cluster already has one."
  type        = bool
  default     = true
}

variable "ingress_nginx_namespace" {
  description = "Namespace for the ingress-nginx controller."
  type        = string
  default     = "ingress-nginx"
}

variable "ingress_nginx_chart_version" {
  description = "ingress-nginx chart version; empty installs latest. Pin for reproducibility."
  type        = string
  default     = ""
}

variable "ingress_use_host_port" {
  description = <<-EOT
    Local-kind only: bind the ingress controller to the node's host 80/443 and use
    a NodePort Service instead of a LoadBalancer (which stays <pending> on kind and
    times out the apply). Leave false on real clouds. deploy/local/up.sh sets true.
  EOT
  type        = bool
  default     = false
}

variable "ingress_class" {
  description = "IngressClass name the controller registers; set the same on the Helm chart's ingress.className."
  type        = string
  default     = "nginx"
}

variable "enable_cert_manager" {
  description = "Install cert-manager (+ CRDs). Set false if the cluster already has it."
  type        = bool
  default     = true
}

variable "cert_manager_namespace" {
  description = "Namespace for cert-manager."
  type        = string
  default     = "cert-manager"
}

variable "cert_manager_chart_version" {
  description = "cert-manager chart version; empty installs latest. Pin for reproducibility."
  type        = string
  default     = ""
}

# --- TimescaleDB (event hypertables, ADR-004) -----------------------------------

variable "timescale_image" {
  description = "Container image for TimescaleDB (a Postgres superset that preloads the timescaledb extension; the app creates the extension + hypertables). PIN this for reproducible deploys."
  type        = string
  default     = "timescale/timescaledb:latest-pg16"
}

variable "timescale_database" {
  description = "Initial database name for TimescaleDB."
  type        = string
  default     = "devicechain"
}

variable "timescale_username" {
  description = "Superuser/username for TimescaleDB."
  type        = string
  default     = "postgres"
}

variable "timescale_password" {
  description = "Password for TimescaleDB. Override for any non-local deploy."
  type        = string
  default     = "devicechain"
  sensitive   = true
}

variable "timescale_storage" {
  description = "PersistentVolume size for TimescaleDB."
  type        = string
  default     = "8Gi"
}

variable "timescale_storage_class" {
  description = "StorageClass for the TimescaleDB data volume. Empty uses the cluster default (often reclaimPolicy Delete). FOR PRODUCTION DURABILITY set this to a StorageClass whose reclaimPolicy is Retain, so the underlying volume survives PVC/PV deletion and a redeploy can re-attach the data."
  type        = string
  default     = ""
}

# --- Observability (kube-prometheus-stack — Prometheus/Grafana/Alertmanager) -----

variable "enable_monitoring" {
  description = "Install the kube-prometheus-stack observability stack (Prometheus Operator + Prometheus + Grafana + Alertmanager). Default-on like Postgres/Timescale; set false if the cluster already has the Prometheus Operator, or to skip metrics collection entirely."
  type        = bool
  default     = true
}

variable "monitoring_namespace" {
  description = "Namespace for the monitoring stack."
  type        = string
  default     = "monitoring"
}

variable "monitoring_chart_version" {
  description = "kube-prometheus-stack chart version. Pinned by default — this chart's Operator CRDs are installed once and never upgraded by Helm, so an unpinned 'latest' drifting across bootstraps skews the operator against install-day CRDs. Bump deliberately."
  type        = string
  default     = "65.1.1"
}

variable "monitoring_slim" {
  description = "Reduce the footprint for a local/kind cluster: Prometheus keeps its TSDB on emptyDir (no PVC) and requests fewer resources. The bring-up sets this true on a local context."
  type        = bool
  default     = false
}

variable "monitoring_grafana_admin_password" {
  description = "Grafana admin password. Native admin auth for now; OIDC via user-management (ADR-047) is a follow-up. Override for any non-local deploy."
  type        = string
  default     = "devicechain"
  sensitive   = true
}

variable "monitoring_prometheus_retention" {
  description = "How long Prometheus retains samples (e.g. 15d). Lower it for a slim/local cluster on emptyDir."
  type        = string
  default     = "15d"
}

variable "monitoring_prometheus_storage" {
  description = "PVC size for the Prometheus TSDB when NOT slim. Ignored in slim mode (emptyDir)."
  type        = string
  default     = "20Gi"
}

variable "monitoring_storage_class" {
  description = "StorageClass for the Prometheus PVC; empty uses the cluster default. Ignored in slim mode."
  type        = string
  default     = ""
}

# --- Grafana SSO (ADR-047) — set by the bring-up when it provisions the client ----

variable "monitoring_grafana_oauth_enabled" {
  description = "Turn on Grafana OAuth SSO against user-management (ADR-047), operator/superuser-tier only. The bring-up sets this true once it has minted the client secret and computed the URLs below."
  type        = bool
  default     = false
}

variable "monitoring_grafana_oauth_client_id" {
  description = "OAuth client_id Grafana authenticates as (the confidential client seeded in user-management)."
  type        = string
  default     = "grafana"
}

variable "monitoring_grafana_oauth_client_secret" {
  description = "Cleartext Grafana OAuth client secret (user-management stores its bcrypt hash). Minted by the bring-up. Sensitive."
  type        = string
  default     = ""
  sensitive   = true
}

variable "monitoring_grafana_oauth_auth_url" {
  description = "Browser-facing authorize endpoint (public host via the /api/user-management ingress), e.g. https://<host>/api/user-management/oauth/authorize."
  type        = string
  default     = ""
}

variable "monitoring_grafana_oauth_token_url" {
  description = "Server-side token endpoint Grafana's pod calls — an IN-CLUSTER service URL (the pod cannot reach the public host), e.g. http://user-management.<instance>:8080/oauth/token."
  type        = string
  default     = ""
}

variable "monitoring_grafana_oauth_api_url" {
  description = "Server-side userinfo endpoint Grafana's pod calls — an in-cluster service URL, e.g. http://user-management.<instance>:8080/oauth/userinfo."
  type        = string
  default     = ""
}

variable "monitoring_grafana_root_url" {
  description = "External root URL Grafana is served at, e.g. https://<host>/grafana (drives root_url + serve_from_sub_path + the OAuth redirect base)."
  type        = string
  default     = ""
}

variable "monitoring_grafana_ingress_host" {
  description = "Host the /grafana ingress answers on (same host as the app ingress). Empty leaves Grafana ClusterIP + port-forward only."
  type        = string
  default     = ""
}
