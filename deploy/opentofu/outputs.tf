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

output "postgres_host" {
  description = "Host:port of the relational Postgres."
  value       = module.postgres.host
}

output "timescaledb_host" {
  description = "Host:port of TimescaleDB."
  value       = module.timescaledb.host
}
