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
  sys_password_bcrypt      = var.nats_sys_password_bcrypt
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

  # Backups need the plugin, and the plugin is an extension of the operator, so
  # an install without the operator has no backups no matter what the backup flag
  # says. Deriving it ONCE here rather than writing the conjunction at each of
  # the five places that need it is what stops the two halves drifting: a site
  # that tested only var.enable_database_backups would provision an object store
  # and two ObjectStore resources for a plugin that is not installed.
  backups_on = var.enable_database_backups && var.enable_cnpg

  # Where the archiver actually points, resolved once from the destination. The
  # in-cluster case reads the module's outputs rather than re-deriving the
  # Service DNS name and the Secret's key names, so the module stays free to
  # change either without this file silently disagreeing.
  backup_endpoint = local.backups_on ? (
    var.backup_destination == "in-cluster" ? module.object_store[0].endpoint_url : var.backup_endpoint_url
  ) : ""

  backup_credentials = local.backups_on ? (
    var.backup_destination == "in-cluster" ? {
      secret     = module.object_store[0].credentials_secret
      access_key = module.object_store[0].access_key_id_key
      secret_key = module.object_store[0].secret_access_key_key
      } : {
      secret     = kubernetes_secret_v1.backup_credentials[0].metadata[0].name
      access_key = "ACCESS_KEY_ID"
      secret_key = "SECRET_ACCESS_KEY"
    }
  ) : null

  # The two per-store backup configurations. Null when backups are off, which is
  # what the cnpg-cluster module reads as "this store has no backups" — one
  # nullable object rather than an `enabled` flag beside five fields that are
  # meaningless without it.
  rdb_backup = local.backups_on ? {
    bucket                = var.backup_bucket_rdb
    endpoint_url          = local.backup_endpoint
    credentials_secret    = local.backup_credentials.secret
    access_key_id_key     = local.backup_credentials.access_key
    secret_access_key_key = local.backup_credentials.secret_key
    schedule              = var.backup_schedule
    retention_policy      = var.backup_retention

    # Threaded from the root so a RESTORE is expressible without editing module
    # source mid-incident. CloudNativePG refuses to let a recovered cluster
    # archive back over the path it recovered from, and it refuses by hanging in
    # `Setting up primary` rather than by failing — so the operator who needs this
    # is the one least able to go looking for it.
    server_name = var.backup_server_name_rdb
  } : null

  tsdb_backup = local.backups_on ? {
    bucket                = var.backup_bucket_tsdb
    endpoint_url          = local.backup_endpoint
    credentials_secret    = local.backup_credentials.secret
    access_key_id_key     = local.backup_credentials.access_key
    secret_access_key_key = local.backup_credentials.secret_key
    schedule              = var.backup_schedule
    retention_policy      = var.backup_retention
    server_name           = var.backup_server_name_tsdb

    # 🔴 Four parallel WAL uploads for the event store against the relational
    # store's two, and this asymmetry is the same judgement that gave the two
    # stores different durability settings. This one takes device telemetry at
    # ingest rates, so it generates WAL far faster; an archiver that cannot keep
    # up does not drop segments, it leaves them on the database's own volume
    # until that volume fills and Postgres stops. The relational store's write
    # rate is control-plane traffic and does not need it.
    wal_max_parallel = 4
  } : null

  # The two per-store RESTORE configurations (ADR-028, ADR-020 A2.5). Null is a
  # normal install; non-null recovers that store from the archive instead of
  # initialising it empty.
  #
  # 🔴 PER STORE, NEVER ONE SWITCH FOR BOTH. The relational store and the event
  # store have independent WAL timelines and independent archives, so "restore
  # the database" is not a single operation and a shared point-in-time target
  # would be a coincidence rather than a guarantee. They are also genuinely
  # restored separately in practice: losing the event store to a bad retention
  # change does not mean the control plane needs rewinding, and rewinding it
  # anyway would discard every tenant, device and rule created since.
  #
  # 🔴 A RESTORE ONLY TAKES EFFECT ON A CLUSTER THAT DOES NOT YET EXIST.
  # `spec.bootstrap` is read when CloudNativePG CREATES the Cluster; setting
  # these against a live instance is expected to change nothing at all, quietly.
  # That is not yet measured — hack/dr-rig.sh is what will settle it — so treat
  # it as the reason this is a rebuild-time lever and not a repair one.
  rdb_restore = var.restore_rdb_from == "" ? null : {
    source_server_name = var.restore_rdb_from
    recovery_target = var.restore_rdb_target_time == "" ? {} : {
      targetTime = var.restore_rdb_target_time
    }
  }

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

# Database backups — the destination, and the two per-store configurations that
# point at it (ADR-028, ADR-020 A2.5).
#
# 🔑 ONE FLAG DECIDES FOR BOTH STORES. It would be easy to make backups
# per-store, and it would be a mistake: the state nobody would ever choose on
# purpose is one store backed up and the other not, and that is exactly what a
# per-store flag produces the first time someone edits one of two nearly
# identical blocks. Whether a store HAS backups is a property of the instance;
# WHERE they go is per-store, and that part genuinely differs (two buckets).

# The credentials the ObjectStore resources present to the endpoint.
#
# For the in-cluster destination this is the object-store module's own Secret,
# reused rather than copied — MinIO's root credentials and the credentials the
# archiver presents ARE the same credentials, and two Secrets holding one value
# is two things to rotate and one of them to forget. For an external destination
# there is no module, so the root writes one.
resource "kubernetes_secret_v1" "backup_credentials" {
  count = local.backups_on && var.backup_destination == "external" ? 1 : 0

  metadata {
    name      = "dc-backup-credentials"
    namespace = var.namespace
    labels = {
      "app.kubernetes.io/name"       = "dc-backup"
      "app.kubernetes.io/component"  = "database-backup"
      "app.kubernetes.io/managed-by" = "opentofu"
    }
  }

  data = {
    ACCESS_KEY_ID     = var.backup_access_key
    SECRET_ACCESS_KEY = var.backup_secret_key
  }

  depends_on = [module.namespace]
}

# The in-cluster object store. Only for backup_destination = "in-cluster"; an
# external destination provisions nothing here.
module "object_store" {
  source = "./modules/object-store"
  count  = local.backups_on && var.backup_destination == "in-cluster" ? 1 : 0

  namespace     = var.namespace
  buckets       = [var.backup_bucket_rdb, var.backup_bucket_tsdb]
  access_key    = var.backup_access_key
  secret_key    = var.backup_secret_key
  storage       = var.backup_object_store_storage
  storage_class = var.backup_object_store_storage_class

  depends_on = [module.namespace]
}

# 🔴 THE BACKUPS ARE THE LEAST-PROTECTED THING IN THIS CONFIGURATION, and this
# guard is what fixes that.
#
# The database Clusters carry prevent_destroy because "databases outlive the
# deployment". Until this existed, the thing that outlives the DATABASE carried
# nothing: the object store is `count = local.backups_on && in-cluster`, so
# anything that flips either half drops the count to zero and takes the PVC — and
# on a default StorageClass with reclaimPolicy Delete, the retention window's
# worth of backups — with it. No confirmation, no diff anyone would read as
# destructive.
#
# The paths that do it are ordinary, not exotic:
#   - enable_database_backups = false
#   - backup_destination flipped to "external"
#   - `dcctl bootstrap <existing-instance> --compact --no-tls`, which emits
#     enable_database_backups=false on its own
#
# 🔴 AND prevent_destroy ON THE PVC WOULD NOT HELP, which is why this is a
# precondition in the ROOT instead. Dropping a module's count to zero ORPHANS its
# resources, and orphans are destroyed without consulting their lifecycle rules —
# the same trap the legacy-StatefulSet cutover guard above exists for. A guard
# that only fires while the resource is still in the configuration does not fire
# on the one operation that removes it.
data "kubernetes_resources" "object_store_pvc" {
  api_version    = "v1"
  kind           = "PersistentVolumeClaim"
  namespace      = var.namespace
  field_selector = "metadata.name=dc-object-store-data"
}

resource "terraform_data" "backup_removal_guard" {
  # A missing namespace returns ZERO objects rather than erroring, which is what
  # makes this safe on a fresh install: there is no namespace yet, the guard
  # passes, and nothing is protected because nothing exists.
  input = length(data.kubernetes_resources.object_store_pvc.objects)

  lifecycle {
    precondition {
      condition = (
        length(data.kubernetes_resources.object_store_pvc.objects) == 0 ||
        (local.backups_on && var.backup_destination == "in-cluster") ||
        var.allow_backup_destination_removal
      )
      error_message = <<-EOT
        This cluster has an in-cluster backup destination holding real backups,
        and this configuration removes it.

        The object store's PersistentVolumeClaim (dc-object-store-data in
        ${var.namespace}) is provisioned only while database backups are on AND
        backup_destination is "in-cluster". This apply satisfies neither, so the
        claim would be destroyed — and on a StorageClass whose reclaimPolicy is
        Delete, which is the default nearly everywhere, its contents go with it.

        That is every base backup and every archived WAL segment for BOTH
        databases: the entire recovery window, for the control plane and the event
        history alike.

        Terraform's prevent_destroy does not stop this and cannot: dropping the
        module's count to zero ORPHANS its resources, and orphans are destroyed
        without consulting their lifecycle rules.

        The usual ways to arrive here, none of which look destructive:

          enable_database_backups = false
          backup_destination      = "external"
          dcctl bootstrap <instance> --compact --no-tls   (turns backups off)

        If the backups are already safe elsewhere, or you do not want them:

          allow_backup_destination_removal = true

        Setting it asserts you have handled the backups. Nothing checks that for
        you, and nothing can — this configuration cannot tell a copied archive
        from an abandoned one.
      EOT
    }
  }
}

# 🔴 A PLAN-TIME REFUSAL, not an apply-time one, and not a variable validation.
#
# The condition spans two variables, and cross-variable references in a
# `validation` block need a newer core than this root's `required_version = ">=
# 1.6"` allows. A precondition on a terraform_data is the portable form and is
# the shape this root already uses for the legacy-StatefulSet cutover guard.
#
# What it refuses is the configuration that CANNOT work and does not say so: an
# external destination with no endpoint. barman-cloud with an empty endpointURL
# does not fail to start — it talks to the real AWS S3, where the bucket does not
# exist and the credentials are a local MinIO's, so every archive attempt fails
# against a service the operator never intended to contact.
resource "terraform_data" "backup_destination_guard" {
  count = local.backups_on ? 1 : 0

  input = var.backup_destination

  lifecycle {
    precondition {
      condition     = var.backup_destination != "external" || var.backup_endpoint_url != ""
      error_message = <<-EOT
        backup_destination = "external" needs backup_endpoint_url, and it is empty.

        An empty endpoint is not "no endpoint" to barman-cloud — it is the default
        AWS S3 endpoint. Archiving would be attempted against real AWS with a
        bucket that does not exist there, and it would fail on every WAL segment
        without stalling a single write. Nothing would crash and nothing would be
        backed up.

        Set backup_endpoint_url to your S3-compatible endpoint, or set
        backup_destination = "in-cluster" to provision one here.
      EOT
    }

    precondition {
      condition     = var.backup_destination != "in-cluster" || var.backup_endpoint_url == ""
      error_message = <<-EOT
        backup_endpoint_url is set while backup_destination = "in-cluster".

        The in-cluster endpoint is an output of the object-store module this
        configuration provisions, so an override here would be silently ignored —
        leaving a configuration that names one destination and archives to
        another. Pick one: drop backup_endpoint_url, or set
        backup_destination = "external".
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
  count = local.rdb_restore == null && local.tsdb_restore == null ? 0 : 1

  input = "${var.restore_rdb_from}|${var.restore_tsdb_from}"

  lifecycle {
    precondition {
      condition     = local.backups_on
      error_message = <<-EOT
        A restore was requested while database backups are off.

        There is no ObjectStore to read from: enable_database_backups is false,
        or enable_cnpg is. Nothing would be recovered, and the cluster would come
        up EMPTY rather than failing — which during a rebuild looks exactly like
        a restore that found no data to bring back.

        Set enable_database_backups = true (and enable_cnpg = true) with the
        bucket and endpoint the backup was written to.
      EOT
    }

    # 🔴 The wedge, refused before it can happen. Verified in CloudNativePG's
    # own recovery documentation: a cluster that archives into a non-empty
    # archive is stopped by a safety check and sits in `Setting up primary` with
    # the pod logging that the archive is not empty. It does not fail the apply
    # and it does not resume without editing the Cluster in place.
    precondition {
      condition     = local.rdb_restore == null || (var.backup_server_name_rdb != "" && var.backup_server_name_rdb != var.restore_rdb_from)
      error_message = <<-EOT
        Restoring the relational store needs backup_server_name_rdb set to a
        path of its OWN, different from restore_rdb_from.

        A recovered cluster keeps archiving. CloudNativePG refuses to archive
        into an archive that already has data in it, so a restored store pointed
        back at the path it recovered from comes up and then HANGS in `Setting up
        primary` — not an apply failure, a wedged database, discovered while
        restoring.

        Unset is the same collision: it means "use the cluster's own name".

        Set backup_server_name_rdb to something like "${var.restore_rdb_from}-restored".
      EOT
    }

    precondition {
      condition     = local.tsdb_restore == null || (var.backup_server_name_tsdb != "" && var.backup_server_name_tsdb != var.restore_tsdb_from)
      error_message = <<-EOT
        Restoring the event store needs backup_server_name_tsdb set to a path of
        its OWN, different from restore_tsdb_from. See the relational store's
        message above — the failure is a database wedged in `Setting up primary`
        during a restore, not an apply error.

        Set backup_server_name_tsdb to something like "${var.restore_tsdb_from}-restored".
      EOT
    }

    # The two stores have separate archives under separate buckets, so the same
    # serverName means two different things and is not a collision. Restoring
    # one from the OTHER's path is, and it is an easy line to copy wrongly in a
    # runbook: it would bring the event store back holding the control plane's
    # data, or the reverse, and the first symptom is services failing on tables
    # that are not there.
    precondition {
      condition     = local.rdb_restore == null || local.tsdb_restore == null || var.backup_bucket_rdb != var.backup_bucket_tsdb
      error_message = <<-EOT
        Both stores are being restored and backup_bucket_rdb equals
        backup_bucket_tsdb.

        Two stores sharing one bucket are not two restore domains, and their
        serverNames are the only thing keeping their archives apart. Give each
        store its own bucket.
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

  # 🔴 max_connections IS SIZED FROM THE SERVICE POOLS, and leaving it at the
  # Postgres default of 100 was an ADR-020 A2.7b defect rather than a tuning
  # preference. Measured on the HA rig.
  #
  # Every rdb-backed service opens its own pool, capped by
  # defaultMaxOpenConnections = 20 (backend/core/rdb/postgres.go); no service
  # overrides it. There is no pgbouncer, and all of them resolve `dc-postgresql`,
  # whose selector is byte-identical to `dc-rdb-rw` — so every pool lands on the
  # SAME primary and they all draw from one budget:
  #
  #   default profile  7 services x 20 = 140
  #   full profile     9 services x 20 = 180   (adds ai-inference, outbound-connectors)
  #
  # against `max_connections - superuser_reserved_connections` = 100 - 3 = 97.
  # The default profile oversubscribes the primary 1.44x, full 1.86x. Confirmed by
  # opening connections as the application role until refusal: number 94 failed with
  # `FATAL: remaining connection slots are reserved for roles with the SUPERUSER
  # attribute`.
  #
  # 🔑 WHY NOTHING CAUGHT IT. Pools open LAZILY, so an idle deployment holds a
  # handful of connections and the rig sits green until real concurrent load
  # arrives. Worse, the failure is invisible to every alert we have — measured
  # while holding 98 of 100 slots:
  #
  #   - the Cluster still reports Ready=True / "Cluster in healthy state", so
  #     CNPGClusterNotReady cannot see it;
  #   - `cnpg_backends_total` and `cnpg_pg_settings_setting` DISAPPEAR, because the
  #     exporter authenticates as cnpg_metrics_exporter, which is not a superuser
  #     and is refused by the same reservation. A utilization-ratio alert would go
  #     ABSENT rather than fire.
  #
  # Only the operator's own superuser connections survive, which is exactly why the
  # Cluster keeps reporting healthy while every application service is locked out.
  # The detector that does work is `cnpg_collector_up == 0`; see
  # prometheusrule-database-control-plane.yaml, which alerts on it.
  #
  # 🔴 SIZE IT FROM THE ROLLOUT, NOT FROM THE STEADY STATE — this is where the
  # obvious number is wrong, and 300 (the first value here) was wrong for exactly
  # this reason.
  #
  # `rollingUpdate` ships maxUnavailable:0 / maxSurge:1 (values.yaml), so a new pod
  # must pass readiness BEFORE the old one is removed. Both hold full pools at once,
  # and a plain `helm upgrade` therefore roughly DOUBLES the pod count of every
  # RollingUpdate area. Eight of the nine rdb consumers surge; only event-processing
  # is Recreate (it is a single writer). And values.yaml recommends `replicas >= 2`
  # in production, which is the configuration we tell people to run:
  #
  #                                    pods                      x20    vs 597
  #   default, replicas 1, upgrading   6x2 + 1 = 13              260    ok
  #   full,    replicas 1, upgrading   8x2 + 1 = 17              340    ok
  #   default, replicas 2, upgrading   6x3 + 1 = 19              380    ok
  #   full,    replicas 2, upgrading   8x3 + 1 = 25              500    ok
  #
  # At 300 the last three of those exceed the budget, so the fix would have been
  # undone by a routine upgrade of the profile we ship, with the silent
  # Ready-but-locked-out symptom described above. 600 covers the recommended
  # production configuration mid-rollout with margin for the operator, the exporter,
  # dcctl bootstrap/migrations and drdrill. The next break point is replicas 3 on the
  # full profile (8x4 + 1 = 33 pods = 660); re-derive before going there.
  #
  # 🔑 RAISE THIS BEFORE RAISING `replicas` OR THE POOL CAP, and note that changing
  # it is applied by CNPG as a rolling in-place restart (~2.5 min on the rig), not a
  # reload.
  #
  # 🔑 THE FOOTPRINT COST IS SMALL, AND IT WAS MEASURED RATHER THAN ASSUMED, because
  # `--compact` exists for small nodes and the instances request only 256Mi with no
  # limit. Postgres sizes some shared memory from max_connections, so this is a real
  # question. `SHOW shared_memory_size`, same rig, same shared_buffers (128MB):
  #
  #   max_connections   100 -> 145MB    300 -> 154MB    600 -> 169MB
  #
  # 24MB for 500 extra slots. The important part is that max_connections is a
  # CEILING, not an allocation: a slot nothing connects to costs nothing beyond
  # that, and the actual number of backends is bounded by the deployed pools
  # (500 worst case above), not by this value. Raising it does not raise
  # steady-state memory; it removes a cliff.
  parameters = {
    max_connections = "600"
  }

  # `required` durability for this store specifically: it holds the audit
  # journal and the control plane, RPO=0 is the point, and a write stall is a
  # loud recoverable failure rather than silent loss. The event store takes
  # `preferred` in A2.4 — that asymmetry is a decision, not an oversight.
  #
  # The module turns this off by construction below three instances, so a
  # non-HA install does not inherit a setting that would wedge it.
  synchronous     = true
  data_durability = "required"

  # WAL archiving + a daily base backup to this store's own bucket. Null when
  # backups are off, which the module reads as "no backups for this store" and
  # reports through its backup_destination output rather than leaving the caller
  # to re-derive it from the flags.
  backup = local.rdb_backup

  # Null on every normal install. Non-null recovers this store from the archive
  # instead of initialising it, and only takes effect on a Cluster that does not
  # yet exist — see the restore variables and terraform_data.restore_guard.
  restore = local.rdb_restore

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

  # 🔴 The CNPG control plane follows the instance's HA posture because it is IN
  # the database failover path, not merely adjacent to it. A single-replica
  # operator or backup plugin that dies with a node blocks promotion for every
  # Cluster in the instance until it is rescheduled — measured at 10m50s against
  # 1m51s when it survived. Replicating the databases and leaving the thing that
  # promotes them un-replicated is the false-HA shape one level up.
  ha = var.ha

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

  namespace     = var.monitoring_namespace
  chart_version = var.monitoring_chart_version
  slim          = var.monitoring_slim

  # Export the CloudNativePG Clusters' own status conditions through
  # kube-state-metrics (ADR-020 A1.5). Read from enable_cnpg rather than offered as
  # a preference: with no CNPG CRDs there is nothing to watch, and the alerts that
  # consume these series would load, select nothing and never fire.
  cnpg_cluster_metrics   = var.enable_cnpg
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
