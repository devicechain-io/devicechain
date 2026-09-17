# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# These endpoints are what the DeviceChain services/Helm values point at.
#
# This is the PER-INSTANCE half — the broker and the event store, in the instance's own
# namespace, so services reach them by short in-namespace names (dc-nats,
# dc-timescaledb-single). The shared half (the relational store, ingress, the operator
# namespaces) is in the cluster root's outputs, and dcctl reads both.

output "instance_namespace" {
  description = "The instance's own namespace, where its broker and event store run and export their metrics from."
  value       = var.instance_namespace
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

output "timescaledb_host" {
  description = "Host:port of the event store — the alias Service CloudNativePG keeps pointed at the primary."
  value       = module.cnpg_tsdb.host
}

output "timescaledb_cluster_name" {
  description = "The CloudNativePG Cluster object name for the event store. Pods carry it as cnpg.io/cluster=<name>; prefer resolving through the alias Service, which is topology-independent."
  value       = module.cnpg_tsdb.cluster_name
}

output "timescaledb_synchronous_enforced" {
  description = "Whether the event store is ACTUALLY replicating synchronously, after the instance-count derivation — read this rather than re-deriving it from the flags. Note this store runs `preferred` durability, so even when true a lost standby degrades to asynchronous rather than stalling writes."
  value       = module.cnpg_tsdb.synchronous_enforced
}

output "database_backups_enabled" {
  description = "Whether this instance's event store is actually archiving WAL and taking scheduled base backups. 🔑 It reflects THIS root's flag; that the plugin performing the archiving exists at all is checked against the cluster by backup_prerequisite_guard, not asserted here."
  value       = local.backups_on
}

output "database_backup_destination" {
  description = <<-EOT
    Where the event store's backups actually land, or null when it has none.

    Read this rather than re-deriving it from the flags. A store whose backup
    configuration was dropped looks identical to one that has it — it runs, it
    replicates, it passes every health check — and the difference only surfaces
    when someone tries to restore it.
  EOT
  value       = module.cnpg_tsdb.backup_destination
}

output "database_restored_from" {
  description = <<-EOT
    Whether this apply RECOVERED the event store from an archive, and from which
    serverName. Null on a normal install.

    🔴 This reports INTENT, not outcome. It says the recovery bootstrap was
    rendered, not that any data came back — CloudNativePG reads `spec.bootstrap`
    only when it CREATES a Cluster, so a restore aimed at a store that already
    exists changes nothing at all, with no error and a green apply.
  EOT
  value       = local.tsdb_restore == null ? null : local.tsdb_restore.source_server_name
}
