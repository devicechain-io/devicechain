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
  description = "PersistentVolume size for NATS JetStream file storage."
  type        = string
  default     = "8Gi"
}

variable "nats_enable_tls" {
  description = "Terminate TLS on the NATS client + MQTT listeners (ADR-025). Keep in sync with the services' instance config: when true the bring-up must thread the nats_ca output into infrastructure.nats.tls so clients dial over TLS. Set false only for plaintext debugging."
  type        = bool
  default     = true
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
