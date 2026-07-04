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
