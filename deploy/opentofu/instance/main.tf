# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0


# NATS: messaging + MQTT ingress + JetStream KV (ADR-003/006/007).
module "nats" {
  source = "../modules/nats"

  namespace                = var.namespace
  chart_version            = var.nats_chart_version
  jetstream_storage        = var.nats_jetstream_storage
  jetstream_max_file_store = var.nats_jetstream_max_file_store
  ha                       = var.ha
  cluster_replicas         = var.nats_cluster_replicas
  enable_prom_exporter     = var.nats_prom_exporter
  enable_tls               = var.nats_enable_tls
  ca_cert_pem              = var.nats_ca_cert_pem
  reject_qos2_publish      = var.nats_mqtt_reject_qos2_publish
  enable_auth              = var.nats_enable_auth
  callout_issuer_public    = var.nats_callout_issuer_public
  service_password_bcrypt  = var.nats_service_password_bcrypt
  sys_password_bcrypt      = var.nats_sys_password_bcrypt
  mqtt_node_port           = var.nats_mqtt_node_port

  # 🔑 NO depends_on ON THE NAMESPACE, BECAUSE THIS ROOT NO LONGER OWNS IT. The
  # namespace is a cluster prerequisite now — created by the cluster root, and by
  # dcctl before either apply, because the credentials these workloads are built
  # from are Secrets that have to be written into it first. An ordering edge to a
  # resource in another root is not expressible, and faking one with a data source
  # would only turn a missing namespace into a slightly earlier error than the
  # apply already gives.
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
  # trips the guard.
  #
  # 🔑 ONE ENTRY, WHERE THE PRE-SPLIT ROOT HAD TWO. The relational store's entry
  # moved to the cluster root with the store itself; this root replaces only the
  # event store, so guarding the other one from here would refuse an instance for
  # the state of a database it no longer owns.
  legacy_db_statefulsets = {
    tsdb = {
      statefulset = "dc-timescaledb-single"
      store       = "event-database"
      holds       = "all recorded device event history"
      allow       = var.allow_legacy_tsdb_removal
      allow_var   = "allow_legacy_tsdb_removal"
      dump        = "pg_dumpall -U <user> > events.sql   # NOT pg_dump: every service CREATEs its own database, so this store holds one per instance"
    }
  }

  # 🔴 THIS IS NOT `enable_database_backups && enable_cnpg`, AND THE DIFFERENCE IS
  # A SILENTLY UNBACKED-UP EVENT STORE.
  #
  # Before the split one root both installed the operator and created the stores,
  # so ANDing the two was right: asking for backups without the plugin that
  # performs them is a contradiction, and deriving it once stopped the halves
  # drifting apart.
  #
  # After the split the operator belongs to the CLUSTER root, so this root always
  # runs in the mode `enable_cnpg = false` describes — "the cluster already runs
  # CNPG". MEASURED: with enable_database_backups = true and enable_cnpg = false
  # the old conjunction evaluates FALSE, which drops the ObjectStore, the
  # ScheduledBackup and every archive setting on the Cluster. The operator asks
  # for backups and gets none, and nothing fails.
  #
  # So the flag now means what it says, and the question the conjunction was
  # really asking — IS THE PLUGIN ACTUALLY HERE? — is asked of the cluster instead,
  # by backup_prerequisite_guard below. A belief about another root was never
  # evidence about it.
  backups_on = var.enable_database_backups

  # The archive target, SUPPLIED rather than derived. The object store lives in the
  # cluster root now, so its endpoint and the key names inside its Secret are not
  # readable from here — they are outputs of that root, threaded through by dcctl
  # (or by hand, from `tofu output`, for someone applying the two roots directly).
  #
  # 🔑 The key names are supplied too, not assumed. They differ by destination —
  # an in-cluster MinIO names them MINIO_ROOT_USER/MINIO_ROOT_PASSWORD and an
  # external store ACCESS_KEY_ID/SECRET_ACCESS_KEY — and this root deliberately
  # does not know which kind it was pointed at. That is the whole benefit of taking
  # them as values: `backup_destination` is the cluster root's question now, and a
  # copy of it here could disagree with the store that actually exists.
  tsdb_backup = local.backups_on ? {
    bucket                = var.backup_bucket_tsdb
    endpoint_url          = var.backup_endpoint_url
    credentials_secret    = var.backup_credentials_secret
    access_key_id_key     = var.backup_access_key_id_key
    secret_access_key_key = var.backup_secret_access_key_key
    schedule              = var.backup_schedule
    retention_policy      = var.backup_retention
    server_name           = var.backup_server_name_tsdb

    # 🔴 Four parallel WAL uploads for the event store against the relational
    # store's two, and this asymmetry is the same judgement that gave the two
    # stores different durability settings. This one takes device telemetry at
    # ingest rates, so it generates WAL far faster; an archiver that cannot keep
    # up does not drop segments, it leaves them on the database's own volume
    # until that volume fills and Postgres stops.
    wal_max_parallel = 4
  } : null

  # The event store's RESTORE configuration (ADR-028, ADR-020 A2.5). Null is a
  # normal install; non-null recovers from the archive instead of initialising
  # empty.
  #
  # 🔴 PER STORE, NEVER ONE SWITCH FOR BOTH — which the split now enforces
  # structurally rather than by convention: the relational store's restore is a
  # different root's variable. The two have independent WAL timelines and
  # independent archives, so "restore the database" was never one operation, and
  # losing the event store to a bad retention change does not mean the control
  # plane needs rewinding.
  #
  # 🔴 A RESTORE ONLY TAKES EFFECT ON A CLUSTER THAT DOES NOT YET EXIST.
  # `spec.bootstrap` is read when CloudNativePG CREATES the Cluster; setting this
  # against a live instance is expected to change nothing at all, quietly. That is
  # why it is a rebuild-time lever and not a repair one.
  tsdb_restore = var.restore_tsdb_from == "" ? null : {
    source_server_name = var.restore_tsdb_from
    recovery_target = var.restore_tsdb_target_time == "" ? {} : {
      targetTime = var.restore_tsdb_target_time
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

# THE RESTORE GUARD (ADR-028, ADR-020 A2.5).
#
# The cnpg-cluster chart refuses these combinations too, and that is not
# redundant — it is deliberately doubled, because WHEN each one fires is the
# whole point. A chart `fail` surfaces during `helm_release` APPLY: the operator
# has already created a cluster, already run the infrastructure apply, and is
# some minutes into a rebuild before anything says the restore was
# mis-specified. A root precondition fires at PLAN, before a single object is
# created, which is the difference between a typo and a restart of the
# procedure.
#
# The audience is an operator working an incident against a runbook. Everything
# below is therefore refused with a message that says what to set, not what is
# wrong.
resource "terraform_data" "restore_guard" {
  count = local.tsdb_restore == null ? 0 : 1

  input = var.restore_tsdb_from

  lifecycle {
    precondition {
      condition     = local.backups_on
      error_message = <<-EOT
        A restore was requested while database backups are off.

        There is no ObjectStore to read from: enable_database_backups is false.
        Nothing would be recovered, and the cluster would come up EMPTY rather
        than failing — which during a rebuild looks exactly like a restore that
        found no data to bring back.

        Set enable_database_backups = true, with the bucket and endpoint the
        backup was written to.
      EOT
    }

    # 🔴 The wedge, refused before it can happen. Verified in CloudNativePG's
    # own recovery documentation: a cluster that archives into a non-empty
    # archive is stopped by a safety check and sits in `Setting up primary` with
    # the pod logging that the archive is not empty. It does not fail the apply
    # and it does not resume without editing the Cluster in place.
    precondition {
      condition     = var.backup_server_name_tsdb != "" && var.backup_server_name_tsdb != var.restore_tsdb_from
      error_message = <<-EOT
        Restoring the event store needs backup_server_name_tsdb set to a path of
        its OWN, different from restore_tsdb_from.

        A recovered cluster keeps archiving. CloudNativePG refuses to archive
        into an archive that already has data in it, so a restored store pointed
        back at the path it recovered from comes up and then HANGS in `Setting up
        primary` — not an apply failure, a wedged database, discovered while
        restoring.

        Unset is the same collision: it means "use the cluster's own name".

        Set backup_server_name_tsdb to something like "${var.restore_tsdb_from}-restored".
      EOT
    }
  }
}

# 🔴 THE CROSS-ROOT PRECONDITION, ASKED OF THE CLUSTER RATHER THAN ASSUMED.
#
# Archiving is performed by the Barman Cloud plugin, which is an extension of the
# CloudNativePG operator — and after the split both belong to the CLUSTER root.
# One OpenTofu graph used to order that dependency; two roots cannot, so the
# instance root is applied against a cluster whose plugin it has no way to
# require.
#
# The pre-split answer was `enable_database_backups && enable_cnpg`, a conjunction
# over this root's OWN variable. That was never evidence about the cluster, and
# after the split it is actively wrong: this root runs with enable_cnpg false by
# construction, so the conjunction reads FALSE and silently drops every archive
# setting.
#
# So ask the cluster. The plugin serves ObjectStore objects, so its CRD being
# established is the plugin being installed — the same "does the thing that must
# already be here exist?" shape as the cutover guard above, and it fires at PLAN
# time, before a Cluster is created with archiving it cannot perform.
data "kubernetes_resources" "barman_plugin_crd" {
  count = local.backups_on ? 1 : 0

  api_version    = "apiextensions.k8s.io/v1"
  kind           = "CustomResourceDefinition"
  field_selector = "metadata.name=objectstores.barmancloud.cnpg.io"
}

resource "terraform_data" "backup_prerequisite_guard" {
  count = local.backups_on ? 1 : 0

  input = length(data.kubernetes_resources.barman_plugin_crd[0].objects)

  lifecycle {
    precondition {
      condition     = length(data.kubernetes_resources.barman_plugin_crd[0].objects) > 0
      error_message = <<-EOT
        Database backups are enabled for this instance, but the Barman Cloud
        plugin is not installed on this cluster.

        The plugin is what performs WAL archiving and base backups, and it is a
        cluster prerequisite: it is installed by the cluster root, not by this
        one. Without it the event store would be created with archive settings
        nothing acts on — no WAL shipped, no base backup taken, and no error,
        until a restore is attempted and finds an empty archive.

        Apply the cluster root against this cluster first (dcctl bootstrap does
        this for you), or set enable_database_backups = false to create an
        instance that deliberately has no backups.
      EOT
    }
  }
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
  source = "../modules/cnpg-cluster"

  namespace          = var.namespace
  name               = "dc-tsdb"
  alias_service_name = "dc-timescaledb-single"
  image              = var.timescale_image
  database           = var.timescale_database
  username           = var.timescale_username
  storage            = var.timescale_storage
  storage_class      = var.timescale_storage_class

  instances = var.timescale_instances != 0 ? var.timescale_instances : (var.ha ? 3 : 1)

  # The read-only SQL/BI surface's roles.
  #
  # The GROUP role is unconditional and the per-tenant readers are not, which is
  # the whole shape of "shipped, not sold": the surface exists on every install,
  # and it has no readers until an operator names one. The group is NOLOGIN and
  # holds nothing but SELECT on the analytics views, so an install with no readers
  # carries a role that can neither connect nor be connected through.
  #
  # 🔴 It is declared even with no readers ON PURPOSE. event-management grants the
  # surface to this role at boot; without the role there is nothing to grant to,
  # and adding the first reader would then need a restart of event-management as
  # well as an apply. Declared always, adding a reader is one apply.
  # 🔴 TWO GROUP ROLES, BECAUSE POSITION IS A SEPARATE AUTHORITY. analytics_reader
  # holds the surface; analytics_location_reader holds the one view carrying
  # latitude/longitude/elevation/accuracy/speed/heading. A reader joins the first
  # unconditionally and the second only where the operator wrote reads_location = true,
  # which is the one place a SQL session can carry an authority at all: it has no
  # claims, only a role. Both are declared on every install for the same reason the
  # first always was — event-management grants to them at boot, and a group created
  # later would need a restart as well as an apply.
  extra_roles = concat(
    [{
      name     = "analytics_reader"
      login    = false
      in_roles = []
      },
      {
        name     = "analytics_location_reader"
        login    = false
        in_roles = []
    }],
    [for r in var.timescale_analytics_readers : {
      name                 = r.name
      login                = true
      connection_limit     = r.connection_limit
      password_secret_name = r.password_secret
      in_roles             = r.reads_location ? ["analytics_reader", "analytics_location_reader"] : ["analytics_reader"]
    }]
  )
  reserved_application_connections = var.timescale_analytics_reserved_connections

  # 🔑 NO max_connections OVERRIDE HERE — the relational store carries one and this
  # store deliberately does not. (This block DOES set `parameters`, further down,
  # for timescaledb.telemetry_level; add to that map rather than starting a second
  # one.)
  #
  # Exactly ONE service holds a pool against this store: event-management, the
  # only caller passing Persistence.Tsdb. That is 1 x 20 = 20 against 97 usable,
  # so the A2.7b arithmetic next door lands nowhere near the ceiling here, and the
  # stock 100 is a real margin rather than an inherited accident.
  #
  # 🔴 RE-DERIVE IT ON `replicas`, WHICH IS THE LIKELIER TRIGGER than a second
  # service. event-management inherits replicas 1 + RollingUpdate, and it is the
  # ingest persistence path — the area most likely to be scaled out first. With
  # maxSurge:1 the break point is 5 concurrent pods (5 x 20 = 100 > 97), i.e.
  # replicas 4 mid-upgrade. A second tsdb-backed service moves it sooner.
  #
  # Remember the failure is silent: pools open lazily, the Cluster keeps reporting
  # Ready, and the exporter's own metrics vanish with the application's. The
  # CNPGClusterUnusable alert covers this store too, and would be the only warning.
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

  # The event-data half of the DR split (ADR-028): its own bucket, restorable
  # without touching the control plane. The root key gates the CORE half only, so
  # a core-restore-alone yields an operational instance and an event-restore-
  # alone yields history — two operations, deliberately, because they are.
  backup  = local.tsdb_backup
  restore = local.tsdb_restore

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

  # 🔑 NO depends_on, for the same reason as the broker above: both the namespace
  # and the CloudNativePG operator this Cluster needs belong to the cluster root.
  # The operator's presence is not left to hope — backup_prerequisite_guard checks
  # the plugin at plan time, and a Cluster created with no operator at all simply
  # never reconciles, which is loud rather than silent.
}
