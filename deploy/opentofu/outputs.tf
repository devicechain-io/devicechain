# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# These endpoints are what the DeviceChain services/Helm values point at. They
# line up with the chart defaults (e.g. dc-nats.dc-system:4222,
# dc-postgresql.dc-system:5432) so `deploy/helm/devicechain` works against this
# infra out of the box.

output "namespace" {
  description = "Namespace the infrastructure was deployed into."
  value       = var.namespace
}

output "nats_client_url" {
  description = "NATS client URL (JetStream KV + core messaging)."
  value       = module.nats.client_url
}

output "nats_mqtt_url" {
  description = "NATS MQTT ingress URL for device connections."
  value       = module.nats.mqtt_url
}

output "nats_ca" {
  description = "PEM-encoded CA that signed the NATS server cert (ADR-025). Empty when TLS is off. The bring-up threads this into every service's instance config (infrastructure.nats.tls.ca) so clients verify the broker over TLS."
  value       = module.nats.ca_pem
}

output "nats_cluster_replicas" {
  description = "Number of NATS servers provisioned. The CEILING on the per-stream replica factor the Helm chart may ask for (instance.config.infrastructure.nats.streamReplicas) — a stream cannot be replicated wider than its cluster. dcctl reads this between the infra apply and the Helm install so an instance whose two HA levers disagree is refused with a sentence, rather than coming up looking replicated and not being."
  value       = module.nats.cluster_replicas
}

output "nats_tls_enabled" {
  description = "Whether the broker terminates TLS. Drives the matching client-side flag (infrastructure.nats.tls.enabled) — the two MUST agree or clients cannot connect."
  value       = module.nats.tls_enabled
}

output "postgres_host" {
  description = "Host:port of the relational Postgres."
  value       = module.postgres.host
}

output "timescaledb_host" {
  description = "Host:port of TimescaleDB."
  value       = module.timescaledb.host
}

output "ingress_class" {
  description = "IngressClass name to set on the Helm chart's ingress.className (null if the controller was not installed here)."
  value       = var.enable_ingress_nginx ? module.ingress_nginx[0].ingress_class : null
}

output "cert_manager_namespace" {
  description = "Namespace cert-manager was installed into (null if not installed here)."
  value       = var.enable_cert_manager ? module.cert_manager[0].namespace : null
}

output "grafana_namespace" {
  description = "Namespace the monitoring stack (Grafana) was installed into (null if not installed here)."
  value       = var.enable_monitoring ? module.monitoring[0].namespace : null
}

output "grafana_service" {
  description = "Grafana ClusterIP Service name (null if monitoring not installed here). The bring-up prints a port-forward hint to it."
  value       = var.enable_monitoring ? module.monitoring[0].grafana_service : null
}

output "cnpg_namespace" {
  description = "Namespace the CloudNativePG operator was installed into (null if not installed here)."
  value       = var.enable_cnpg ? module.cnpg[0].namespace : null
}

# Read this rather than re-deriving it from the flags. An install with the operator
# and no backup plugin has database HA and NO point-in-time recovery, and the two are
# indistinguishable from the Cluster resources alone — which is exactly why this has
# to be a value a caller can READ. The module exposed it from the start; for a while
# nothing at the root did, so the safeguard existed only as a description.
output "database_backups_enabled" {
  description = "Whether the Barman Cloud plugin is installed, i.e. whether point-in-time recovery is possible at all (null if CNPG was not installed here)."
  value       = var.enable_cnpg ? module.cnpg[0].backup_plugin_enabled : null
}
