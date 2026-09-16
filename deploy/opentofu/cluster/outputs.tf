# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# These endpoints are what an instance root and the DeviceChain services point at.
# They are the CLUSTER half of the pair — the shared relational store, the ingress
# class, the operator namespaces and the archive contract below. The per-instance
# half (the broker, the event store) is in the instance root's outputs.

# These endpoints are what the DeviceChain services/Helm values point at. They
# line up with the chart defaults (e.g. dc-nats.dc-system:4222,
# dc-postgresql.dc-system:5432) so `deploy/helm/devicechain` works against this
# infra out of the box.

output "namespace" {
  description = "Namespace the infrastructure was deployed into."
  value       = var.namespace
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

output "postgres_provisioner_role" {
  description = "The base identity dcctl signs in as to create each instance's login and database."
  value       = module.cnpg_rdb.provisioner_role
}

output "postgres_provisioner_credentials_secret" {
  description = "The Secret (kubernetes.io/basic-auth: username, password) holding the base identity's credentials, in the namespace output above."
  value       = module.cnpg_rdb.provisioner_credentials_secret
}

output "postgres_owner_credentials_secret" {
  description = "The Secret (kubernetes.io/basic-auth: username, password) holding the relational store's owner credentials."
  value       = module.cnpg_rdb.credentials_secret
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

output "database_backup_destination" {
  description = <<-EOT
    Where the relational store's backups actually land, or null when it has none.

    Read this rather than re-deriving it from the flags. A store whose backup
    configuration was dropped looks identical to one that has it — it runs, it
    replicates, it passes every health check — and the difference only surfaces
    when someone tries to restore it.

    🔑 SINGULAR, where the pre-split root returned a map of two. The event store
    is another root's resource now, and reporting on it from here would be this
    root restating a belief about a store it cannot see.
  EOT
  value       = module.cnpg_rdb.backup_destination
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

output "database_restored_from" {
  description = <<-EOT
    Whether this apply RECOVERED the relational store from an archive, and from
    which serverName. Null on a normal install.

    Read this rather than re-deriving it from the restore variables. A restore is
    the one operation where "what did I actually ask for" and "what did the
    infrastructure do" are most likely to differ and least likely to be checked:
    CloudNativePG reads `spec.bootstrap` only when it CREATES a Cluster, so a
    restore aimed at a store that already exists is expected to change nothing at
    all — no error, no restore, a green apply.

    🔴 This still reports INTENT, not outcome. It says the recovery bootstrap was
    rendered, not that any data came back. Nothing in an apply can tell you that;
    hack/dr-rig.sh reads a row out of the restored database, which is the only
    form of that answer worth having.
  EOT
  value       = local.rdb_restore == null ? null : local.rdb_restore.source_server_name
}

# 🔴🔴 THE ARCHIVE CONTRACT — the four values an instance root cannot derive.
#
# The object store lives here now, and an instance root archives its event store
# into it while having no way to see it: the endpoint is a Service DNS name this
# root composes, and the key names inside the credentials Secret differ by
# destination (an in-cluster MinIO names them MINIO_ROOT_USER/MINIO_ROOT_PASSWORD,
# an external store ACCESS_KEY_ID/SECRET_ACCESS_KEY).
#
# 🔑 EXPORTED RATHER THAN RESTATED AT THE CALLER, for the reason the object-store
# module already gives for exporting its key names one level down: they are this
# root's contract, and a caller that hard-codes them keeps working right up until
# they change. dcctl reads these between the two applies; anyone applying the roots
# by hand reads them with `tofu output`.
output "backup_endpoint_url" {
  description = "S3 endpoint an instance root should archive to. Empty when backups are off here, which is the same thing as there being nothing to archive into."
  value       = local.backup_endpoint
}

output "backup_credentials_secret" {
  description = "Name of the Secret holding the archiver's credentials, in this root's namespace. Instance roots reference it by name; its VALUE never passes through either root's state."
  value       = local.backups_on ? local.backup_credentials.secret : ""
}

output "backup_access_key_id_key" {
  description = "Key within backup_credentials_secret holding the access key ID. Differs by destination, which is why an instance root is told it rather than assuming it."
  value       = local.backups_on ? local.backup_credentials.access_key : ""
}

output "backup_secret_access_key_key" {
  description = "Key within backup_credentials_secret holding the secret access key."
  value       = local.backups_on ? local.backup_credentials.secret_key : ""
}

output "backup_bucket_tsdb" {
  description = "The bucket this root created for the EVENT store's archive. Exported because the store that writes into it lives in another root: the bucket has to be created here, beside the object store, and named there. Empty when backups are off."
  value       = local.backups_on ? var.backup_bucket_tsdb : ""
}
