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
  description = <<-EOT
    Shorthand for the ADR-020 NATS topology: 3 NATS servers in a RAFT cluster,
    spread one-per-node, instead of 1. Defaults to single-node.

    WHAT THIS DOES NOT DO, stated because the previous description promised it and
    it was never true: it does not replicate the databases. Postgres and TimescaleDB
    are single-instance StatefulSets in this root regardless — see ADR-028 for where
    their durability actually comes from. It also does not, on its own, replicate
    the JetStream STREAMS: that is a per-stream replica factor in the services'
    config (instance.config.infrastructure.nats.streamReplicas), rendered by the
    DeviceChain Helm chart, which this root does not install. Both halves must be
    raised together or the instance runs a 3-node broker holding single-replica
    streams — replicated servers, unreplicated data. `dcctl bootstrap --ha` sets
    both from one value and preflights that they agree; a direct tofu user must set
    the Helm value themselves.
  EOT
  type        = bool
  default     = false
}

variable "nats_cluster_replicas" {
  description = "Number of NATS servers. 0 (default) derives it from var.ha — 3 when true, 1 when false. Set explicitly for a topology the toggle cannot express (5). ODD ONLY, at most 5: RAFT commits on a majority, so an even cluster tolerates no more failures than the odd size below it while costing a server and a wider quorum, and JetStream refuses more than 5 replicas per stream. See the nats module variable for the full rationale, including why this is only half the HA toggle."
  type        = number
  default     = 0

  validation {
    condition     = contains([0, 1, 3, 5], var.nats_cluster_replicas)
    error_message = "nats_cluster_replicas must be 0 (derive from var.ha), 1, 3, or 5."
  }
}

variable "nats_prom_exporter" {
  description = "Run the prometheus-nats-exporter sidecar on each NATS server, publishing BROKER-side metrics (routes, RAFT/JetStream cluster health) on :7777. The PodMonitor that scrapes it is rendered by the DeviceChain Helm chart, not here — it is an Operator CRD, and rendering one from this root would fail the apply on a fresh cluster where the monitoring stack is still installing alongside it. Set false to drop the sidecar (dcctl's compact preset does)."
  type        = bool
  default     = true
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
    to whole units of the size's own magnitude — 16Gi yields 14Gi, not 14.4Gi). The
    platform creates 16 such streams — a FIXED set (streams are per-suffix and cover
    every tenant via the wildcard subject), so this does not grow with tenant count.

    THIS IS PER NODE, not per cluster. JetStream reserves on the server that holds a
    replica, so a 3-node HA cluster (var.ha) provisions this volume three times and
    each node stores one copy of the same reservation. Do NOT multiply it by the
    replica factor — that would triple a budget that is already correct, and the
    only thing it would buy is the illusion of headroom. (The account-level quota
    WOULD multiply, which is exactly why the APP account is left on dynamic limits;
    see the landmine note in modules/nats/main.tf.)

    Sizing history, because getting this wrong fails in a confusing way: 8Gi (→7Gi
    ceiling) fit only ~7 streams at the old uniform 1 GiB bound, so a fresh bring-up
    crashlooped its last stream-creating services (device-management,
    event-processing, event-sources) with "insufficient storage resources available".
    That was first fixed by raising the PV to 32Gi, which worked but spent most of it
    on control-plane streams that never hold more than a few MiB.

    The bound is now split hot/cold in backend/core/streams (see streams.All): 7 hot
    streams at 1 GiB, 8 control-plane streams at 128 MiB, and the capture stream at
    256 MiB reserve 8448Mi (8.25Gi). The MQTT gateway's own streams — which
    nats-server creates UNBOUNDED, and which the platform bounds at startup so they
    cannot eat the rest — add 384Mi. The KV buckets are bounded on the same principle
    (see kv.All): 4 State buckets at 128Mi + 6 Cache buckets at 64Mi reserve a
    further 896Mi. Total reserved: exactly 9.5Gi.

    Why 16Gi and not 12Gi, which also "fits": at 12Gi the ceiling is 10Gi, leaving
    512Mi unreserved — which is EXACTLY the headroom floor the budget test asserts,
    i.e. zero margin. At that size the platform could not add one more stream or one
    more KV bucket without moving the PV first, and the failure for doing so is the
    crashloop above rather than a test. 16Gi (→14Gi ceiling) leaves 4.5Gi, which is
    room for the reservation to grow by half again. The extra 4Gi of disk is the
    cheapest part of this deployment; the alternative was a budget where every new
    bucket is a deploy-time landmine.

    NEVER shrink this independently of the stream bounds, and do NOT size the PV as
    (sum of stream ceilings) / 0.9 — the flooring above makes that formula unsafe.
    A 9.5Gi sum / 0.9 rounds to an 11Gi PV, whose ceiling is floor(11 × 0.9) = 9Gi,
    which is BELOW the sum and brings the crashloop back. Pick the smallest whole
    magnitude where floor(magnitude × 0.9) >= the sum, then leave real margin above
    it. Raise it for real ingest volume.

    This value IS linked to a test. dcctl's TestRenderedReservationFitsTheJetStreamStore
    (backend/cli/bootstrap) reads this default out of the embedded infrastructure
    config, applies the flooring rule above, and checks the ceilings the Helm chart
    actually renders against it — so lowering this without lowering the stream bounds
    fails there rather than on someone's fresh install.

    CAVEAT — one copy is still un-linked. backend/core/config/instance_test.go carries
    its own `pvGi` constant mirroring this default, and lowering this value will not
    make those tests fail. They pin the arithmetic on the values core/config computes
    for itself, which is worth keeping; just do not read them as a guard on THIS
    variable. Changing this value means updating that constant by hand.
  EOT
  type        = string
  default     = "16Gi"
}

variable "nats_jetstream_max_file_store" {
  description = "Server-level max_file_store — the hard aggregate JetStream disk ceiling (ADR-023). Empty derives it as 90% of nats_jetstream_storage, FLOORED to a whole unit of that size's own magnitude (16Gi yields 14Gi, not 14.4Gi), leaving filesystem headroom; set explicitly to override. Must be <= nats_jetstream_storage. Note this is a PER-NODE ceiling: an HA cluster applies it on every server, each holding one replica."
  type        = string
  default     = ""
}

variable "nats_mqtt_reject_qos2_publish" {
  description = "Refuse MQTT QoS 2 PUBLISH at the broker (ADR-023). QoS 2 buys nothing over the platform's own event de-duplication and is the one gateway store a device can fill on purpose. Note the rejection tears down the CONNECTION rather than NACKing the message, so a device that publishes QoS 2 in a loop reconnects in a loop; QoS 0/1 are unaffected. See the nats module variable for the full rationale."
  type        = bool
  default     = true
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

variable "nats_mqtt_node_port" {
  description = <<-EOT
    Local-kind only: expose the MQTT gateway as a NodePort on this node port so a
    host device/tool reaches the broker at ssl://127.0.0.1:1883 through the kind
    1883->31883 host map. 0 = ClusterIP only (the cloud default; a NodePort would
    publish MQTT on every node IP). dcctl sets 31883 on a local context, matching the
    kind map; leave it 0 on real clouds. See the nats module var for the full story.
  EOT
  type        = number
  default     = 0
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
