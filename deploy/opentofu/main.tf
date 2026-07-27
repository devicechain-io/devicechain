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
  cluster_replicas         = var.nats_cluster_replicas
  enable_prom_exporter     = var.nats_prom_exporter
  enable_tls               = var.nats_enable_tls
  reject_qos2_publish      = var.nats_mqtt_reject_qos2_publish
  enable_auth              = var.nats_enable_auth
  callout_issuer_public    = var.nats_callout_issuer_public
  service_password_bcrypt  = var.nats_service_password_bcrypt
  mqtt_node_port           = var.nats_mqtt_node_port

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

# CloudNativePG — the operator that will own both database stores (ADR-020 A2).
# Installs the operator + CRDs and, when backups are enabled, the Barman Cloud
# plugin. It creates no Cluster resources yet; A2.3/A2.4 move `rdb` and `tsdb`
# onto it. Installed on non-HA installs too (decision D4) — backup is not an HA
# feature, and one storage shape means HA is an `instances` count rather than a
# migration.
module "cnpg" {
  source = "./modules/cnpg"
  count  = var.enable_cnpg ? 1 : 0

  namespace              = var.cnpg_namespace
  operator_chart_version = var.cnpg_chart_version
  plugin_chart_version   = var.cnpg_plugin_chart_version
  enable_backup_plugin   = var.enable_database_backups

  # enable_pod_monitor is deliberately NOT passed, so it stays off. It was briefly
  # wired to var.enable_monitoring; that is wrong twice over — a variable read
  # orders nothing, so the release races kube-prometheus-stack's CRDs on a fresh
  # apply, and enable_monitoring=false means "the cluster already HAS the operator",
  # which is the case where scraping would have worked. See the variable's own docs.

  # The plugin's chart renders an Issuer and two Certificates, so cert-manager's
  # CRDs must be present AND its webhook serving before this runs. Ordering alone
  # is not enough when cert-manager is installed in this same apply, which is why
  # this depends on the module rather than merely being written after it — the
  # cert-manager helm_release waits for its own rollout (including the chart's
  # startupapicheck), so depending on it is depending on readiness.
  #
  # When enable_cert_manager is false the dependency is an empty list and this
  # ordering does nothing. That case is handled at the caller: dcctl turns
  # enable_database_backups off on the one path that drops cert-manager.
  depends_on = [module.cert_manager]
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

  # Grafana SSO (ADR-047): operator-tier-only OAuth against user-management + the
  # /grafana ingress. Off unless the bring-up mints a client secret and supplies URLs.
  grafana_oauth_enabled       = var.monitoring_grafana_oauth_enabled
  grafana_oauth_client_id     = var.monitoring_grafana_oauth_client_id
  grafana_oauth_client_secret = var.monitoring_grafana_oauth_client_secret
  grafana_oauth_auth_url      = var.monitoring_grafana_oauth_auth_url
  grafana_oauth_token_url     = var.monitoring_grafana_oauth_token_url
  grafana_oauth_api_url       = var.monitoring_grafana_oauth_api_url
  grafana_root_url            = var.monitoring_grafana_root_url
  grafana_ingress_host        = var.monitoring_grafana_ingress_host
  ingress_class               = var.ingress_class

  # When SSO is on, the monitoring stack ships a /grafana Ingress, which the
  # ingress-nginx admission webhook must validate. Both installs otherwise run in
  # parallel, so the webhook can still be unreachable (connection refused) when the
  # Grafana ingress is created. Serialize monitoring after ingress-nginx — whose
  # helm_release waits for the controller (and thus its admission endpoint) to be
  # ready. The app services avoid this naturally: their ingresses are created in the
  # later helm step, after this apply completes.
  depends_on = [module.ingress_nginx]
}
