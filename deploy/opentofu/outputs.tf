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
  description = "Host:port of the relational Postgres — the managed alias Service, which CNPG keeps pointed at the current primary."
  value       = module.cnpg_rdb.host
}

# The database half of the false-HA guard, and the exact sibling of
# nats_cluster_replicas above. An install that ASKED for synchronous replication
# but did not get enough instances runs asynchronously, with three healthy pods
# and a green apply — indistinguishable from the real thing unless something
# reads it back. So this reports what is ACTUALLY in force, not what was
# requested, and the HA rig asserts against the live Cluster object rather than
# against this value or the YAML that produced it.
output "postgres_synchronous_enforced" {
  description = "Whether the relational database is genuinely replicating synchronously. False on a single-instance install (correct — there is no standby to wait for), and false on any topology below the count synchronous replication requires."
  value       = module.cnpg_rdb.synchronous_enforced
}

output "postgres_cluster_name" {
  description = "The CloudNativePG Cluster object name for the relational store. Pods carry it as cnpg.io/cluster=<name>; prefer resolving through the alias Service, which is topology-independent."
  value       = module.cnpg_rdb.cluster_name
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
  description = "Whether both database stores are actually archiving WAL and taking scheduled base backups. 🔑 This used to mean only that the Barman Cloud plugin was INSTALLED, which was true on installs where nothing was archived anywhere; since A2.5 the plugin and the destination are provisioned together, so it means what it says."
  value       = local.backups_on
}

output "database_backup_destinations" {
  description = <<-EOT
    Where each store's backups actually land, or null when it has none. Two
    separate paths, because core data and event data are two independent restore
    domains.

    Read this rather than re-deriving it from the flags. A store whose backup
    configuration was dropped looks identical to one that has it — it runs, it
    replicates, it passes every health check — and the difference only surfaces
    when someone tries to restore it.
  EOT
  value = {
    rdb  = module.cnpg_rdb.backup_destination
    tsdb = module.cnpg_tsdb.backup_destination
  }
}

output "database_backup_survives_cluster_loss" {
  description = <<-EOT
    🔴 FALSE for the default in-cluster destination, and that is the single most
    important thing to know about this instance's backups.

    An in-cluster object store shares the cluster's failure domain and, on a
    single-node install, the node's disk: it protects against an operator error,
    a bad migration or a bad delete, and not against losing the cluster. Only
    backup_destination = "external" is off-site.

    Exported as a value rather than left to the deployment docs so that dcctl and
    any health check can state it plainly instead of implying recoverability from
    database_backups_enabled alone.
  EOT
  value       = local.backups_on ? var.backup_destination == "external" : null
}
