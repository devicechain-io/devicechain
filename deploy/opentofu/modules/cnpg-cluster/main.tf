# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# A DeviceChain database store as a CloudNativePG Cluster (ADR-020 A2).
#
# Generic on image, instance count, durability and bootstrap SQL, so it serves
# BOTH stores exactly as the StatefulSet module it replaced did: `rdb` (A2.3)
# and `tsdb` (A2.4).
# Keeping one module is the point -- the two stores differ in four values, and a
# second copy would let them drift in the other forty.
#
# WHY A HELM RELEASE AND NOT A `kubernetes_manifest`
#
# A `kubernetes_manifest` resource needs its CRD present at PLAN time. The CNPG
# CRDs are installed by the cnpg module in the SAME apply, so a plan against a
# cluster that does not yet have them fails before anything runs -- and it fails
# on a fresh install only, which is the worst possible place to discover it.
# Helm renders and applies together, so this sidesteps the ordering entirely.
# The cert-manager module documents the same trap for the ingress Issuer.
#
# WHY THE CHART IS LOCAL
#
# Because the upstream cnpg/cluster chart fails open on the two fields this
# module exists to set -- see the long header in chart/templates/cluster.yaml
# for the measurement. In short: a misspelled `services` or `roles` key renders
# a Cluster with no `managed:` block and exits 0.
#
# ONE STORAGE SHAPE, HA OR NOT (decision D4)
#
# This module runs on every install, including `instances = 1`. Backup is not
# an HA feature: a single-instance install needs point-in-time recovery more
# than a replicated one, and if CNPG were the HA-only path then hack/dr-rig.sh
# would be drilling a restore that non-HA production never executes. HA is then
# an instance count, not a storage migration.

terraform {
  required_providers {
    helm = {
      source = "hashicorp/helm"
    }
    kubernetes = {
      source = "hashicorp/kubernetes"
    }
  }
}

variable "namespace" {
  type = string
}

variable "name" {
  description = "Cluster object name. NOT the DNS name clients use -- that is var.alias_service_name. These must differ: CNPG reserves <cluster>-rw/-ro/-r, so naming the Cluster after the legacy Service would collide with its own generated names."
  type        = string
}

variable "alias_service_name" {
  description = "The Service name clients connect to -- the DNS contract this store already has. CNPG creates it as a managed service whose selector it maintains, so it follows the primary across a failover, and its name lands in the server certificate's SANs."
  type        = string
}

variable "image" {
  type = string
}

variable "instances" {
  description = "Number of Postgres instances. 1 is a valid, supported production topology (decision D4); 3 is the smallest count at which synchronous replication tolerates a standby loss."
  type        = number

  validation {
    condition     = var.instances >= 1
    error_message = "instances must be at least 1."
  }
}

variable "synchronous" {
  description = <<-EOT
    Enable synchronous replication.

    🔴 Requires at least `synchronous_number + 2` instances, and the chart
    refuses to render below that. The reason is the whole reason A2.3 costs a
    third node: with `number: 1` and only two instances, losing either standby
    stalls every write, which is WORSE for availability than the single node it
    replaced while being better for durability. Three instances mean one
    standby can go without the cluster losing its sync candidate.
  EOT
  type        = bool
  default     = false
}

variable "synchronous_number" {
  description = "How many standbys must confirm a commit."
  type        = number
  default     = 1
}

variable "data_durability" {
  description = <<-EOT
    `required` holds a write until a standby confirms it; `preferred` falls back
    to asynchronous when no standby is available, and self-heals.

    This is a per-store judgement, not a default to inherit. `rdb` holds the
    audit journal and the control plane, where RPO=0 is the point and a write
    stall is a loud, recoverable failure. `tsdb` takes `preferred`: event ingest
    values continuing, the loss window is bounded by replication lag, and
    JetStream already holds un-persisted events durably upstream so the ingest
    path can replay.

    🔴 What `required` does NOT mean, measured on the A2 spike: a stalled write
    is COMMITTED, not rejected. The commit is local and the wait is for the
    remote flush, so a client that times out and gives up has still written the
    row. "Writes stall" is not "writes are rejected" -- a client that retries
    will double-write unless the operation is idempotent, and a primary lost
    after the local commit but before any standby returns loses a write nobody
    was told about. Synchronous replication protects writes the client WAITED
    for; it cannot protect one the client abandoned.
  EOT
  type        = string
  default     = "required"

  validation {
    condition     = contains(["required", "preferred"], var.data_durability)
    error_message = "data_durability must be \"required\" or \"preferred\"."
  }
}

variable "database" {
  type = string
}

variable "username" {
  type = string
}

variable "password" {
  type      = string
  sensitive = true
}

variable "storage" {
  type    = string
  default = "8Gi"
}

variable "storage_class" {
  description = "StorageClass for the data volume. Empty uses the cluster default. For production durability use a class whose reclaimPolicy is Retain."
  type        = string
  default     = ""
}

variable "wal_storage" {
  description = "Size of a dedicated WAL volume, or empty for none. Worth setting once WAL archiving is on: archiving that stalls accumulates WAL until the volume fills and stops Postgres, and a separate volume bounds that to the WAL."
  type        = string
  default     = ""
}

variable "post_init_template_sql" {
  description = "SQL run against template1 at initdb, so every database the app later creates inherits it. This is where the TimescaleDB extension goes -- postInitApplicationSQL would reach only the bootstrap database. 🔴 Confined to initdb, so it does NOT re-run on a restore."
  type        = list(string)
  default     = []
}

variable "extra_roles" {
  description = <<-EOT
    CNPG-managed roles beyond the application owner -- today, the read-only SQL/BI
    roles a Postgres-speaking tool connects as.

    🔴 Declaring them HERE rather than creating them from the application is the
    security decision, not a packaging one. The application role holds no
    CREATEROLE and is not given it: a role that can mint logins is a role a
    compromised pod can mint logins with, and the platform needs that exactly
    once, at install. CloudNativePG creates these instead, and keeps each
    password in step with a Kubernetes Secret the operator owns -- so the
    credential never passes through OpenTofu state, dcctl, or any DeviceChain
    process.

    `connection_limit` is required for a login role and rejected at -1. The chart
    refuses an unlimited one, and refuses a set whose total does not fit the
    server, because these sessions come out of the same max_connections the
    platform's own pools draw on.
  EOT
  type = list(object({
    name                 = string
    ensure               = optional(string, "present")
    login                = optional(bool, false)
    connection_limit     = optional(number, 0)
    password_secret_name = optional(string, "")
    in_roles             = optional(list(string), [])
  }))
  default = []

  validation {
    # Caught here as well as in the chart because a name is easier to fix in the
    # file that names it, and because a tofu-level message reaches `plan` rather
    # than waiting for the release to render.
    condition     = alltrue([for r in var.extra_roles : !r.login || r.connection_limit > 0])
    error_message = "Every login role in extra_roles needs a connection_limit above 0; an unlimited role can exhaust the connections the platform's own pools need."
  }
}

variable "reserved_application_connections" {
  description = "Connections the platform must still be able to open after every extra login role has taken its limit. The chart refuses to render an over-committed server. 0 disables the headroom check but not the refusal of an unlimited role."
  type        = number
  default     = 0
}

variable "shared_preload_libraries" {
  description = "🔴 A library named here and missing from the image makes Postgres refuse to start, and CNPG cannot self-heal out of that state."
  type        = list(string)
  default     = []
}

variable "parameters" {
  description = "Postgres GUCs."
  type        = map(string)
  default     = {}
}

variable "resources" {
  description = <<-EOT
    Resource requests/limits for the Postgres instances.

    🔴 The default is REQUESTS, not empty, and that matters more than it looks.
    An empty value yields a pod with no requests at all — QoS class BestEffort —
    which makes the database the FIRST thing the kubelet evicts under node
    memory pressure. The StatefulSet this replaced requested 100m/256Mi, so
    leaving this empty would have been a silent regression in exactly the
    configuration least able to absorb it: `--compact`, which exists for small
    nodes and is what the HA rig's control instance runs.

    Requests without limits is deliberate, matching the operator module: a CPU
    limit on a database turns load into throttling, and a memory limit turns a
    large query into an OOMKill.
  EOT
  type        = any
  default = {
    requests = {
      cpu    = "100m"
      memory = "256Mi"
    }
  }
}

variable "node_loss_toleration_seconds" {
  description = <<-EOT
    How long an instance pod stays put after its node is tainted NotReady or
    unreachable, before Kubernetes evicts it. null leaves Kubernetes' default,
    which is 300.

    🔴 IT DOES NOT GATE FAILOVER, and an earlier version of this text said it
    did. The claim was that CloudNativePG waits for the failed primary's pod to be
    evicted before promoting. Measured across three drills, it does not: promotion
    began 4.7s after the pod went `Ready=False`, and the event store completed a
    full failover while its old primary pod was still Running and un-evicted at a
    300s toleration.

    What dominates the RTO is whether the CNPG CONTROL PLANE survived the node —
    a Cluster whose plugins the operator cannot reach does not reconcile, and one
    that does not reconcile does not promote. Same fault, same cluster, minutes
    apart: 10m50s with the plugin on the dead node, 1m51s without. See
    modules/cnpg, which is where that is fixed.

    What this knob does is remove the dead primary's pod sooner, which shortens
    the TAIL of a failover rather than its start. Measured, same drill: the store
    at 30s came back 24s ahead of the store at 300s. Real and cheap, but a
    twentieth of what the original model predicted, so do not reach for it
    expecting the headline number.

    The default is 30 rather than 0 because the remaining terms are not ours to
    give — ~50s of kubelet-heartbeat grace before the taint lands, then CNPG's own
    detection and promotion. 0 would buy another 30s and spend the whole margin
    against a node that is briefly silent rather than gone.

    Safe to shorten because the taint already implies ~40s of confirmed silence
    (a kubelet restart re-heartbeats well inside that), and because Kubernetes'
    node controller throttles or halts eviction entirely when a large fraction of
    a zone is unhealthy — so the correlated-failure case this default was written
    for is handled above us. What is left is single-node loss, which is the case
    being optimised, and where the old primary rejoins as a replica on return.
  EOT
  type        = number
  default     = 30

  validation {
    condition     = var.node_loss_toleration_seconds == null || var.node_loss_toleration_seconds >= 0
    error_message = "node_loss_toleration_seconds must be null (Kubernetes' 300s default) or a non-negative number of seconds."
  }
}

variable "backup" {
  description = <<-EOT
    WAL archiving + scheduled base backups for this store, via the Barman Cloud
    plugin (ADR-028, ADR-020 A2.5). Null disables backup for this store.

    🔴 A NULL HERE IS A STORE WITH NO BACKUPS, and that is invisible from
    everything else about the Cluster. It runs, it replicates, it passes every
    health check, and it cannot be restored to any point in time. The root
    derives this from one flag for both stores so the two cannot end up in
    different states, and the `backup_destination` output reports where each
    store's backups actually land rather than restating the flag.

    The object is all-or-nothing on purpose: the chart refuses a partially
    populated destination rather than rendering an ObjectStore that fails every
    archive attempt. See chart/templates/objectstore.yaml for why a half-set
    destination is the dangerous case -- it does not stop writes and it does not
    crash anything.

    `server_name` is the path within the bucket, defaulting to the cluster name.
    A RESTORED cluster must set it: CloudNativePG refuses to archive back over
    the path it recovered from, so a recovery that keeps the old value comes up
    with archiving permanently broken.
  EOT

  type = object({
    bucket                = string
    endpoint_url          = string
    credentials_secret    = string
    access_key_id_key     = string
    secret_access_key_key = string

    server_name = optional(string, "")

    # Forces a WAL switch on an idle cluster, bounding the RPO to this interval
    # rather than to "whenever a segment happens to fill". Also what makes the
    # archive-lag alert meaningful instead of red on every quiet instance.
    archive_timeout = optional(string, "5min")

    schedule          = optional(string, "0 0 3 * * *")
    retention_policy  = optional(string, "30d")
    wal_max_parallel  = optional(number, 2)
    data_jobs         = optional(number, 2)
    endpoint_ca       = optional(object({ name = string, key = string }))
    sidecar_resources = optional(object({ cpu = string, memory = string }), { cpu = "50m", memory = "128Mi" })
  })

  default = null
}

variable "restore" {
  description = <<-EOT
    Recover this store from an existing backup instead of initialising an empty
    one (ADR-028, ADR-020 A2.5). Null is a normal install.

    🔴 THIS IS THE HALF THAT MAKES `backup` MEAN SOMETHING. A destination nobody
    has restored from is a destination nobody has shown to hold a restorable
    database, and every archive metric reads the same either way. The chart
    refuses the combinations that recover the wrong thing or wedge on the way
    back up; hack/dr-rig.sh is what proves the path end to end.

    `source_server_name` is the folder inside the bucket to read, and it has no
    default on purpose: the only inference available is this cluster's own name,
    which on a fresh install is an empty path that recovers nothing.

    `object_store_name` defaults to the ObjectStore this module renders for its
    own archiving, which is the normal case -- the bucket outlived the cluster.

    🔴 A RESTORED STORE THAT ALSO ARCHIVES NEEDS ITS OWN `backup.server_name`,
    and the chart will not render without one. CloudNativePG refuses to archive
    into a non-empty archive, so a restored cluster pointed back at the path it
    recovered from comes up and then hangs in `Setting up primary` -- during a
    restore, which is the worst time to be reading pod logs.
  EOT

  type = object({
    source_server_name = string
    object_store_name  = optional(string, "")

    # PostgreSQL recovery-target fields, camelCase, passed through as written.
    # Empty replays the whole archive. A misspelling here is caught by
    # hack/check-cnpg-chart-schema.sh against the real CRD rather than by this
    # type -- the CRD is the only thing that knows the field names.
    recovery_target = optional(map(string), {})

    # `targetImmediate` is separate because it is the one recovery-target field
    # that is a BOOLEAN. Inside the map above it would be the string "true",
    # which the API server rejects against a boolean field -- so it gets its own
    # typed lever rather than a footgun in a free-form map.
    recovery_target_immediate = optional(bool, false)
  })

  default = null
}

locals {
  # The credentials Secret, in the shape CNPG's bootstrap expects
  # (kubernetes.io/basic-auth with username/password). Left unset, CNPG mints a
  # password of its own -- and the DSN every service ships would then be unable
  # to log in. So the caller's configured credentials are preserved by handing
  # CNPG a Secret rather than by reading one back.
  credentials_secret = "${var.name}-app-credentials"

  # 🔴 ONE DEFINITION, READ BY BOTH THE CHART VALUES AND synchronous_enforced.
  #
  # This used to be written twice — the same expression in the `synchronous`
  # values block and again in the output — which made the output a second copy
  # rather than a report. A measured mutation of the values side (synchronous
  # replication silently OFF on a --ha install, so every write commits without a
  # standby having it) left the output reading `true` and every
  # hack/check-tofu-validations.sh assertion green: an assertion that reads a
  # duplicate is checking the duplicate against itself.
  #
  # Consumed by reference below, so the same mutation now moves the output too.
  synchronous_enforced = var.synchronous && var.instances >= var.synchronous_number + 2

  # The two components of the archive path, named once so `reported` below can
  # compose the destination out of what the chart is ACTUALLY handed rather than
  # re-deriving it from the variables. A restore aims at that string; if the
  # report and the reality can disagree, it aims at a path no WAL was written to.
  #
  # 🔴 THE RAW server_name IS WHAT GOES TO THE CHART, empty string and all. Do
  # not "helpfully" resolve it to the cluster name here: an unset serverName
  # renders NO plugin parameter, CNPG then defaults it to the cluster name, and
  # the chart's restore guard keys on exactly that emptiness to refuse a restored
  # cluster that would archive under an undecided path. Resolving it early would
  # make that guard unreachable while every render still looked right.
  backup_bucket      = var.backup == null ? null : var.backup.bucket
  backup_server_name = var.backup == null ? null : var.backup.server_name
}

resource "kubernetes_secret_v1" "app" {
  metadata {
    name      = local.credentials_secret
    namespace = var.namespace
    labels = {
      "app.kubernetes.io/name"       = var.name
      "app.kubernetes.io/component"  = "database"
      "app.kubernetes.io/managed-by" = "opentofu"
      # CNPG watches Secrets carrying this label. Without it the operator does
      # not reload on a credential change.
      "cnpg.io/reload" = "true"
    }
  }

  type = "kubernetes.io/basic-auth"

  data = {
    username = var.username
    password = var.password
  }
}

locals {
  # 🔴 THE VALUES LIVE IN A LOCAL, NOT INLINE IN THE RESOURCE, so the outputs can
  # READ THEM BACK. See `reported` below: an output that re-derives what it
  # reports is a copy, and the CI harness asserting on it is then checking the
  # copy against itself. A local is the only place both the resource and an
  # output can reach.
  base_values = {
    name                   = var.name
    imageName              = var.image
    instances              = var.instances
    aliasServiceName       = var.alias_service_name
    resources              = var.resources
    parameters             = var.parameters
    sharedPreloadLibraries = var.shared_preload_libraries

    nodeLossTolerationSeconds = var.node_loss_toleration_seconds

    # Camel-cased on the way in because the chart is the Cluster spec verbatim
    # (see chart/templates/cluster.yaml); the snake_case ends at this boundary.
    extraRoles = [for r in var.extra_roles : {
      name               = r.name
      ensure             = r.ensure
      login              = r.login
      connectionLimit    = r.connection_limit
      passwordSecretName = r.password_secret_name
      inRoles            = r.in_roles
    }]
    reservedApplicationConnections = var.reserved_application_connections

    storage = {
      size         = var.storage
      storageClass = var.storage_class
    }

    walStorage = {
      enabled      = var.wal_storage != ""
      size         = var.wal_storage != "" ? var.wal_storage : null
      storageClass = var.storage_class
    }

    # 🔴 DERIVED, not passed through. Synchronous replication at fewer than
    # three instances turns any standby loss into a total write outage, so a
    # single-instance install must never get it -- and "the caller remembers to
    # turn it off" is exactly the coupling that produced the false-HA trap A0
    # closed. The chart refuses to render the unsafe combination as well, so
    # this is belt and braces on the one setting whose failure mode is a
    # permanently wedged database.
    synchronous = {
      enabled        = local.synchronous_enforced
      method         = "any"
      number         = var.synchronous_number
      dataDurability = var.data_durability
    }

    bootstrap = {
      database            = var.database
      owner               = var.username
      secretName          = local.credentials_secret
      encoding            = "UTF8"
      localeCollate       = "C.utf8"
      localeCType         = "C.utf8"
      postInitTemplateSQL = var.post_init_template_sql
    }
  }

  backup_values = var.backup == null ? null : {
    backup = {
      enabled            = true
      bucket             = local.backup_bucket
      endpointURL        = var.backup.endpoint_url
      credentialsSecret  = var.backup.credentials_secret
      accessKeyIdKey     = var.backup.access_key_id_key
      secretAccessKeyKey = var.backup.secret_access_key_key
      serverName         = local.backup_server_name
      archiveTimeout     = var.backup.archive_timeout
      schedule           = var.backup.schedule
      retentionPolicy    = var.backup.retention_policy
      walCompression     = "gzip"
      dataCompression    = "gzip"
      walMaxParallel     = var.backup.wal_max_parallel
      dataJobs           = var.backup.data_jobs
      endpointCASecret   = var.backup.endpoint_ca == null ? {} : var.backup.endpoint_ca
      sidecarResources = {
        requests = {
          cpu    = var.backup.sidecar_resources.cpu
          memory = var.backup.sidecar_resources.memory
        }
      }
    }
  }

  restore_values = var.restore == null ? null : {
    restore = {
      enabled                 = true
      sourceServerName        = var.restore.source_server_name
      objectStoreName         = var.restore.object_store_name
      recoveryTarget          = var.restore.recovery_target
      recoveryTargetImmediate = var.restore.recovery_target_immediate
    }
  }

  # --- What the outputs report -------------------------------------------------
  #
  # 🔴 READ BACK OUT OF THE VALUES ABOVE, NOT RE-DERIVED FROM THE VARIABLES.
  #
  # Both of these used to restate their expression a second time in the output
  # block. That made the output a copy of the values rather than a report of
  # them, and hack/check-tofu-validations.sh asserts against the output.
  # Measured: setting `synchronous.enabled = false` in the values -- a `--ha`
  # install whose every write commits with no standby holding it, the database
  # face of the false-HA trap ADR-020 A0 closed for the broker -- left
  # `synchronous_enforced` reading `true` and the whole harness green. Pointing
  # the same bucket at the wrong path was silent for the same reason, and that
  # one decides whether a restore finds anything at all.
  #
  # The direction matters and is easy to get backwards: the derivations feed the
  # values, the values feed this. Nothing here feeds back, so there is no cycle
  # -- and a mutation of what Helm is handed now moves the assertion.
  reported = {
    synchronous_enforced = local.base_values.synchronous.enabled
    # Read out of the values for the same reason as the line above: this one
    # decides two thirds of the failover time, and re-deriving it from the
    # variable would make the output agree with the request rather than with what
    # Helm was handed. tostring() pins the type so the "unset means Kubernetes'
    # 300" case compares as a null string rather than an untyped null.
    node_loss_toleration_seconds = tostring(local.base_values.nodeLossTolerationSeconds)
    # tostring() pins the TYPE, not the value. Inside an object constructor a
    # bare `null` branch is a null of no particular type, and `tofu console`
    # prints that as `null` where a null string prints as `tostring(null)` --
    # so without this the harness's "backups off => no destination" assertion
    # fails on a spelling difference rather than on the fact it is checking.
    backup_destination = tostring(local.backup_values == null ? null : format(
      "s3://%s/%s",
      local.backup_values.backup.bucket,
      local.backup_values.backup.serverName != "" ? local.backup_values.backup.serverName : local.base_values.name,
    ))
  }
}

resource "helm_release" "cluster" {
  name      = var.name
  namespace = var.namespace
  chart     = "${path.module}/chart"

  # The Secret must exist before initdb reads it. This is an explicit edge
  # rather than an inferred one: the values below reference the Secret by NAME
  # (a string), and a string carries no dependency.
  depends_on = [kubernetes_secret_v1.app]

  # 🔴 TWO documents, not one map with a conditional `backup` key inside it.
  #
  # A `var.backup == null ? {...} : {...}` inside the map does not type-check:
  # both branches of a conditional must have the SAME object type, so the
  # "backups off" branch would have to restate all fifteen backup fields with
  # empty values. That is not merely verbose — it is a second, silent copy of the
  # backup schema that no longer fails when the real one gains a field.
  #
  # Helm merges later values documents over earlier ones, so an absent second
  # document leaves the chart's own `backup.enabled: false` default in force.
  # Backups off is then the chart saying so, not this file re-deriving it. A
  # THIRD document carries the restore block, for the same reason.
  values = concat(
    [yamlencode(local.base_values)],
    local.backup_values == null ? [] : [yamlencode(local.backup_values)],
    local.restore_values == null ? [] : [yamlencode(local.restore_values)],
  )

  # 🔴 `wait` DOES NOT WAIT FOR THE DATABASE, and an earlier version of this
  # comment claimed it did. The release manifest contains exactly one object —
  # the Cluster custom resource. The pods, PVCs and Services are created by the
  # OPERATOR and owned by the Cluster, so they are not in the release, and Helm's
  # readiness checker has no case for an arbitrary custom resource: it reports
  # ready the moment the CR is accepted. The timeout below therefore bounds the
  # apply, not the database.
  #
  # What actually covers the gap today is the Helm step that follows, which waits
  # on the eleven service Deployments with its own 10-minute timeout — and those
  # cannot become ready until the database accepts connections. That is real, but
  # it is downstream and incidental, so it is written here rather than assumed.
  timeout = 900

  # 🔴 atomic IS LOAD-BEARING, and it is here because of prevent_destroy below —
  # the two only make sense read together.
  #
  # Terraform taints a resource whose CREATE failed. The next plan therefore wants
  # to REPLACE it, replacement is a destroy plus a create, and prevent_destroy
  # refuses the destroy. The result is an instance that can never be bootstrapped
  # again: every subsequent apply dies with "Instance cannot be destroyed", and the
  # guard is protecting a database that does not exist and never held a byte.
  # Neither `tofu untaint` nor a re-run recovers it, because Helm also refuses to
  # upgrade a release with no deployed revision — the destroy Terraform wants IS
  # the correct recovery, and the lifecycle block forbids exactly that. Recovery
  # meant `helm uninstall` plus `tofu state rm`, which no operator will find.
  #
  # `atomic` removes the trap at its root rather than papering over it: Helm
  # uninstalls a failed install itself, so the next refresh finds no release, drops
  # the resource from state, and plans a clean create. The wedge cannot form.
  #
  # MEASURED, not reasoned (a server-side rejection injected into a throwaway
  # release carrying the same prevent_destroy):
  #   atomic = false → release "failed", state "tainted", next plan ERRORS
  #   atomic = true  → release removed, state empty, next plan creates cleanly
  #
  # This is what makes a lost admission-webhook race (the operator's webhook is not
  # routable the instant its Deployment reports ready) a re-runnable annoyance
  # instead of a dead instance.
  atomic = true

  # Databases outlive the deployment, so refuse a naive destroy. The StatefulSet
  # this replaced carried the same guard and it must not be lost in the move: the
  # Cluster OWNS its PersistentVolumeClaims, so deleting this release garbage-
  # collects the data volumes — a sharper edge than the old topology had, where
  # the PVC survived the StatefulSet.
  #
  # 🔴 This protects the resource while it is IN THE CONFIGURATION. It does NOT
  # protect it from being removed from the configuration — see the cutover guard
  # in the root, and the reason it had to exist.
  lifecycle {
    prevent_destroy = true
  }
}

output "host" {
  description = "In-cluster host:port for clients -- the alias Service, which CNPG keeps pointed at the primary."
  value       = "${var.alias_service_name}.${var.namespace}:5432"
}

output "service" {
  description = "In-cluster service host."
  value       = "${var.alias_service_name}.${var.namespace}"
}

output "cluster_name" {
  description = "The CNPG Cluster object name. Pods carry it as cnpg.io/cluster=<name>; the DR/HA rigs resolve through the alias Service instead, which is topology-independent."
  value       = var.name
}

output "backup_destination" {
  description = "Where this store's WAL and base backups actually go, or null if it has none. Read this rather than re-deriving it from the flags -- a store whose backup block was dropped is indistinguishable from one that has it, right up until a restore is attempted."
  value       = local.reported.backup_destination
}

output "synchronous_enforced" {
  description = "Whether synchronous replication is ACTUALLY in force, after the instance-count derivation. Read this rather than re-deriving it from the flags: an install that asked for synchronous and did not get enough instances runs asynchronously, and the difference is invisible from the Cluster resources alone -- which is the false-HA shape A0 closed for the broker."
  value       = local.reported.synchronous_enforced
}

output "node_loss_toleration_seconds" {
  description = "The eviction fuse this store's pods actually carry, as a string, or \"tostring(null)\" when Kubernetes' 300s default is left in force. Worth reporting because it is invisible everywhere else: an unset value is not absent from the pod, it is 300 injected by an admission plugin, and nothing else in the apply says so."
  value       = local.reported.node_loss_toleration_seconds
}
