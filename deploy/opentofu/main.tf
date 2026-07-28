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

# 🔴 THE CUTOVER GUARD. Read this before touching anything below it.
#
# A2.3 replaced the relational store's StatefulSet with a CloudNativePG Cluster,
# and A2.4 did the same to the event store. On a FRESH cluster that is
# unremarkable. On an EXISTING one it is a silent data-abandonment bug, and the
# reason is a Terraform behaviour that is easy to get exactly backwards:
#
#   `lifecycle { prevent_destroy = true }` protects a resource that is STILL IN
#   THE CONFIGURATION. Delete the module block and the resource becomes an
#   ORPHAN in state — and orphans are destroyed WITHOUT consulting a lifecycle
#   block, because there is no longer a lifecycle block to consult.
#
# Measured against a copy of a real state file: the plan succeeds, exit 0, and
# the StatefulSet is marked for destruction. The guard that exists precisely to
# stop this does not fire.
#
# What the operator would then get is the worst shape available: the StatefulSet
# and its Service are destroyed, the PVC survives (its retention policy is
# Retain, and it was never in Terraform state anyway because the StatefulSet's
# volumeClaimTemplate created it), and a brand-new EMPTY Cluster takes over the
# same hostname. The instance comes up green with the data gone from the running
# system while still sitting on a detached volume. Nothing reports a problem.
#
# There is no in-place upgrade to offer instead: a StatefulSet's PGDATA cannot
# be adopted by CloudNativePG. The migration is a dump and restore, or — pre-GA,
# and for local instances, usually the right answer — a rebuild.
#
# So this refuses, at PLAN time, before the destroy can be planned. It covers
# `tofu apply` run directly as well as `dcctl bootstrap`, which is why it lives
# here rather than only in the CLI.
#
# It is written PER-STORE rather than once, and that is deliberate: this guard is
# the thing a future storage move is most likely to forget, precisely because the
# version that forgets it still applies cleanly and still comes up green. Adding
# a store here is one map entry, and an absent entry is visible in a way a
# missing copy of a 40-line block is not.
locals {
  # Each pre-CloudNativePG StatefulSet this configuration replaces, keyed by the
  # store it belonged to. `dump` is the recovery recipe quoted back at whoever
  # trips the guard — they differ, because pg_dumpall on the event store would
  # pull hypertable data nobody wants in a text dump.
  legacy_db_statefulsets = {
    rdb = {
      statefulset = "dc-postgresql"
      store       = "relational-database"
      holds       = "tenants, users, devices and relationships"
      allow       = var.allow_legacy_rdb_removal
      allow_var   = "allow_legacy_rdb_removal"
      dump        = "pg_dumpall -U <user> > rdb.sql"
    }
    tsdb = {
      statefulset = "dc-timescaledb-single"
      store       = "event-database"
      holds       = "all recorded device event history"
      allow       = var.allow_legacy_tsdb_removal
      allow_var   = "allow_legacy_tsdb_removal"
      dump        = "pg_dumpall -U <user> > events.sql   # NOT pg_dump: every service CREATEs its own database, so this store holds one per instance"
    }
  }
}

# 🔴 A missing namespace returns ZERO objects rather than erroring, which is the
# behaviour this depends on: on a fresh install there is no namespace yet and
# the guard must pass, not explode. Verified.
data "kubernetes_resources" "legacy_db_statefulsets" {
  for_each = local.legacy_db_statefulsets

  api_version    = "apps/v1"
  kind           = "StatefulSet"
  namespace      = var.namespace
  field_selector = "metadata.name=${each.value.statefulset}"
}

resource "terraform_data" "cutover_guard" {
  for_each = local.legacy_db_statefulsets

  input = length(data.kubernetes_resources.legacy_db_statefulsets[each.key].objects)

  lifecycle {
    precondition {
      condition     = length(data.kubernetes_resources.legacy_db_statefulsets[each.key].objects) == 0 || each.value.allow
      error_message = <<-EOT
        This cluster still runs the OLD ${each.value.store} StatefulSet
        (${each.value.statefulset} in ${var.namespace}), and this configuration
        replaces it with a CloudNativePG Cluster.

        That store holds ${each.value.holds}.

        Applying as-is would DESTROY that StatefulSet and leave its data on an
        orphaned PersistentVolumeClaim while a new, EMPTY database takes over the
        same hostname. The instance would come up healthy and empty. Terraform's
        prevent_destroy does NOT stop this, because removing a module block
        orphans its resources and orphans are destroyed without consulting their
        lifecycle rules.

        There is no in-place upgrade: a StatefulSet's PGDATA cannot be adopted by
        CloudNativePG.

          To DISCARD the old data (local/dev instances, the usual case):

            dcctl destroy <instance>     # or delete the cluster entirely
            dcctl bootstrap ...          # rebuild on the new storage tier

          To KEEP it, dump before cutting over:

            kubectl -n ${var.namespace} exec ${each.value.statefulset}-0 -- \
              ${each.value.dump}
            # then remove the old objects, apply this configuration, and restore
            # into the new Cluster through its service.

        Once the data is safe, set ${each.value.allow_var} = true to proceed.
        Setting it is an assertion that you have handled the data — it is not a
        migration, and nothing checks it for you.
      EOT
    }
  }
}

# Relational Postgres — the control-plane RDB (users, devices, relationships, …),
# as a CloudNativePG Cluster (ADR-020 A2.3).
#
# The Cluster object is `dc-rdb`; clients keep reaching it at `dc-postgresql`,
# which CNPG creates as a MANAGED alias Service whose selector it maintains. The
# two names must differ because CNPG reserves `<cluster>-rw`/`-ro`/`-r` for
# itself. Measured: the alias's selector is byte-identical to `dc-rdb-rw`'s, and
# it followed a primary failover in 3 seconds.
#
# 🔴 NOT gated on var.enable_cnpg. That flag means "do not INSTALL the operator",
# and its documented use is a cluster that already runs one — where the CRDs
# exist and this Cluster is exactly as creatable. Gating the store on it would
# turn "the operator is already here" into "the platform has no relational
# database", which is a much larger surprise than the flag advertises.
module "cnpg_rdb" {
  source = "./modules/cnpg-cluster"

  namespace          = var.namespace
  name               = "dc-rdb"
  alias_service_name = "dc-postgresql"
  image              = var.postgres_image
  database           = var.postgres_database
  username           = var.postgres_username
  password           = var.postgres_password
  storage            = var.postgres_storage
  storage_class      = var.postgres_storage_class

  # 3 under --ha, 1 otherwise, unless pinned explicitly. Both halves of the
  # topology come from ONE value; see the postgres_instances variable for why 2
  # is refused rather than merely discouraged.
  instances = var.postgres_instances != 0 ? var.postgres_instances : (var.ha ? 3 : 1)

  # `required` durability for this store specifically: it holds the audit
  # journal and the control plane, RPO=0 is the point, and a write stall is a
  # loud recoverable failure rather than silent loss. The event store takes
  # `preferred` in A2.4 — that asymmetry is a decision, not an oversight.
  #
  # The module turns this off by construction below three instances, so a
  # non-HA install does not inherit a setting that would wedge it.
  synchronous     = true
  data_durability = "required"

  # The operator must exist before a Cluster referencing its CRDs is applied.
  # depends_on on the module (not merely writing it later) is what makes this
  # ordering rather than hope: the cnpg helm_release waits for its own rollout,
  # so depending on it is depending on the CRDs being established.
  #
  # When enable_cnpg is false this is an empty list and orders nothing — correct,
  # because that flag asserts the operator is already present.
  depends_on = [module.namespace, module.cnpg]
}

# TimescaleDB — the event store's hypertables (ADR-004), as a CloudNativePG
# Cluster (ADR-020 A2.4). Same module as the relational store; the stores differ
# in their image, their durability and their instance count, and in nothing else.
#
# The Cluster object is `dc-tsdb`; clients keep reaching it at
# `dc-timescaledb-single`, the name that is baked into the Helm chart defaults,
# the compiled-in instance-config defaults and dcctl's shipped default CR. The
# name is inherited rather than chosen — it reads as "single instance", which
# above one instance it no longer is — but renaming a DNS contract is a separate
# change from replacing a storage tier, and doing both at once would make a
# failure of either indistinguishable from the other.
module "cnpg_tsdb" {
  source = "./modules/cnpg-cluster"

  namespace          = var.namespace
  name               = "dc-tsdb"
  alias_service_name = "dc-timescaledb-single"
  image              = var.timescale_image
  database           = var.timescale_database
  username           = var.timescale_username
  password           = var.timescale_password
  storage            = var.timescale_storage
  storage_class      = var.timescale_storage_class

  instances = var.timescale_instances != 0 ? var.timescale_instances : (var.ha ? 3 : 1)

  # 🔴 `preferred`, NOT `required` — the opposite of the relational store, and
  # the asymmetry is the decision rather than an oversight.
  #
  # This store takes device telemetry at ingest rates. Holding a write until a
  # standby confirms it would convert a lost standby into ingest backpressure,
  # and the events are already held durably UPSTREAM in JetStream until they are
  # persisted, so the ingest path can replay what a failover drops. `preferred`
  # falls back to asynchronous when no standby is available and self-heals when
  # one returns; the exposure is bounded by replication lag rather than being
  # unbounded.
  #
  # The relational store makes the other trade for the other reason: it holds the
  # audit journal, has no upstream replay, and a stall there is a loud recoverable
  # failure rather than silent loss.
  synchronous     = true
  data_durability = "preferred"

  # 🔴 BOTH of these are load-bearing, and each was found by building the operand
  # image rather than by reading anything.
  #
  # shared_preload_libraries: TimescaleDB is not a plain extension — its custom
  # WAL resource manager must be loaded at server start. Recovery bootstrap is
  # also known to drop this (cnpg#10840), which kills a restore during WAL
  # replay, so it is set on the Cluster itself rather than inherited.
  shared_preload_libraries = ["timescaledb"]

  post_init_template_sql = [
    # Coupling (b). Every DeviceChain service creates its own database at startup
    # and NOTHING in the codebase ever issues `CREATE EXTENSION` — grep confirms
    # zero occurrences. The platform has always depended on the extension already
    # being present in whatever a new database is cloned from, which the stock
    # TimescaleDB image happened to provide. Putting it in template1 makes that
    # dependency explicit and keeps it true. Measured: a database created by the
    # app user afterwards inherits timescaledb 2.28.3.
    "CREATE EXTENSION IF NOT EXISTS timescaledb;",

    # 🔴 Remove the telemetry job, which is NOT the same thing as turning
    # telemetry off, and the difference bites the job-health oracle.
    #
    # `timescaledb.telemetry_level=off` below stops the phone-home. It does NOT
    # remove the job: measured, `policy_telemetry` remains present and
    # `scheduled = true` while never being given a `next_start` and never
    # running. So a healthy cluster permanently carries one job whose next_start
    # is NULL — the exact shape an oracle would read as "the scheduler is
    # stuck", on every cluster, forever, caused by our own setting. A check that
    # is red on a working system is a check that gets switched off.
    #
    # Deleting it here is a no-op if a future image stops creating it.
    "SELECT delete_job(job_id) FROM timescaledb_information.jobs WHERE proc_name = 'policy_telemetry';",
  ]

  parameters = {
    # No phone-home. The operand image ships the telemetry job enabled by
    # default; this is the half that stops it reporting, and the delete_job above
    # is the half that stops it confusing the oracle.
    "timescaledb.telemetry_level" = "off"
  }

  depends_on = [module.namespace, module.cnpg]
}

# CloudNativePG — the operator that owns both database stores (ADR-020 A2).
# Installs the operator + CRDs and, when backups are enabled, the Barman Cloud
# plugin. The Clusters themselves are the two modules above.
# Installed on non-HA installs too (decision D4) — backup is not an HA
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
