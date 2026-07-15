# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

module "namespace" {
  source = "./modules/namespace"

  name   = var.namespace
  create = var.create_namespace
}

# NATS: messaging + MQTT ingress + JetStream KV (ADR-003/006/007).
module "nats" {
  source = "./modules/nats"

  namespace                = var.namespace
  chart_version            = var.nats_chart_version
  jetstream_storage        = var.nats_jetstream_storage
  jetstream_max_file_store = var.nats_jetstream_max_file_store
  ha                       = var.ha
  enable_tls               = var.nats_enable_tls
  enable_auth              = var.nats_enable_auth
  callout_issuer_public    = var.nats_callout_issuer_public
  service_password_bcrypt  = var.nats_service_password_bcrypt

  depends_on = [module.namespace]
}

# Relational Postgres — the control-plane RDB (users, devices, relationships, …).
module "postgres" {
  source = "./modules/postgres"

  namespace     = var.namespace
  name          = "dc-postgresql"
  image         = var.postgres_image
  database      = var.postgres_database
  username      = var.postgres_username
  password      = var.postgres_password
  storage       = var.postgres_storage
  storage_class = var.postgres_storage_class

  depends_on = [module.namespace]
}

# TimescaleDB — event hypertables (ADR-004). Same generic module, different image:
# TimescaleDB is a Postgres superset, so the StatefulSet shape is identical.
module "timescaledb" {
  source = "./modules/postgres"

  namespace     = var.namespace
  name          = "dc-timescaledb-single"
  image         = var.timescale_image
  database      = var.timescale_database
  username      = var.timescale_username
  password      = var.timescale_password
  storage       = var.timescale_storage
  storage_class = var.timescale_storage_class

  depends_on = [module.namespace]
}

# NGINX ingress controller — the L7 entry point fronting the GraphQL/HTTP surface
# (ADR-002). The Ingress resource itself is rendered by the Helm chart.
module "ingress_nginx" {
  source = "./modules/ingress-nginx"
  count  = var.enable_ingress_nginx ? 1 : 0

  namespace     = var.ingress_nginx_namespace
  chart_version = var.ingress_nginx_chart_version
  ingress_class = var.ingress_class
  use_host_port = var.ingress_use_host_port
}

# cert-manager — issues/renews the ingress TLS certificates (ADR-002). The Issuer
# is rendered by the Helm chart once these CRDs exist.
module "cert_manager" {
  source = "./modules/cert-manager"
  count  = var.enable_cert_manager ? 1 : 0

  namespace     = var.cert_manager_namespace
  chart_version = var.cert_manager_chart_version
}

# Observability — kube-prometheus-stack (Prometheus Operator + Prometheus +
# Grafana + Alertmanager). Installs the ServiceMonitor/PrometheusRule CRDs the
# instance chart's metrics.enabled rendering depends on, ahead of the Helm step.
# Default-on (like Postgres/Timescale); set enable_monitoring=false to skip.
module "monitoring" {
  source = "./modules/monitoring"
  count  = var.enable_monitoring ? 1 : 0

  namespace              = var.monitoring_namespace
  chart_version          = var.monitoring_chart_version
  slim                   = var.monitoring_slim
  grafana_admin_password = var.monitoring_grafana_admin_password
  prometheus_retention   = var.monitoring_prometheus_retention
  prometheus_storage     = var.monitoring_prometheus_storage
  storage_class          = var.monitoring_storage_class
}
