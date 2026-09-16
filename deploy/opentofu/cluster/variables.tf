# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0


# --- Cluster access -------------------------------------------------------------

variable "kubeconfig_path" {
  description = "Path to the kubeconfig for the target cluster."
  type        = string
  default     = "~/.kube/config"
}

variable "kubeconfig_context" {
  description = "kubeconfig context to use. Empty uses the current-context."
  type        = string
  default     = ""
}

variable "namespace" {
  description = "Namespace for the DeviceChain infrastructure dependencies. Services default to reaching them at <name>.<namespace> (e.g. dc-nats.dc-system)."
  type        = string
  default     = "dc-system"
}

variable "create_namespace" {
  description = "Whether this root creates the namespace (set false if it is managed elsewhere)."
  type        = bool
  default     = true
}

# --- High availability ----------------------------------------------------------

variable "ha" {
  description = <<-EOT
    Shorthand for the ADR-020 NATS topology: 3 NATS servers in a RAFT cluster,
    spread one-per-node, instead of 1. Defaults to single-node.

    It ALSO now sets BOTH databases to 3 replicated CloudNativePG instances —
    the relational store synchronously (ADR-020 A2.3, see postgres_instances,
    where synchronous replication is what forces a third instance rather than a
    second) and the event store with `preferred` durability (A2.4, see
    timescale_instances). The two differ deliberately: a lost standby stalls
    writes on the relational store and merely degrades the event store to
    asynchronous replication, because event ingest has an upstream replay and the
    audit journal does not.

    WHAT THIS DOES NOT DO: it does not, on its own, replicate
    the JetStream STREAMS: that is a per-stream replica factor in the services'
    config (instance.config.infrastructure.nats.streamReplicas), rendered by the
    DeviceChain Helm chart, which this root does not install. Both halves must be
    raised together or the instance runs a 3-node broker holding single-replica
    streams — replicated servers, unreplicated data. `dcctl bootstrap --ha` sets
    both from one value and preflights that they agree; a direct tofu user must set
    the Helm value themselves.
  EOT
  type        = bool
  default     = false
}

# --- Relational Postgres (control-plane RDB) ------------------------------------

variable "postgres_image" {
  description = <<-EOT
    Operand image for the relational Postgres database (ADR-020 A2.3).

    🔴 This must be a CloudNativePG OPERAND image, not a stock `postgres:` one.
    CNPG's instance manager runs initdb and supervises the server itself, and it
    expects the operand's layout (uid 26, PGDATA conventions, the barman client
    tools on PATH). `postgres:16-alpine` — which this defaulted to while the
    store was a plain StatefulSet — does not satisfy that.

    PostgreSQL 17, matching the TimescaleDB operand image A2.6 builds, so both
    stores sit on one major version.

    This store is deliberately the STOCK image and not our TimescaleDB operand:
    `rdb` holds the control plane and has no hypertables, so the extension would
    be 36 MB of attack surface serving nothing.
  EOT
  type        = string
  default     = "ghcr.io/cloudnative-pg/postgresql:17.10-standard-bookworm"
}

variable "allow_legacy_rdb_removal" {
  description = <<-EOT
    Proceed even though this cluster still runs the pre-A2.3 relational-database
    StatefulSet (dc-postgresql).

    🔴 This is an ASSERTION THAT YOU HAVE HANDLED THE DATA, not a migration.
    Nothing verifies it. Setting it true and applying will destroy the old
    StatefulSet and leave whatever was in it on an orphaned PersistentVolumeClaim
    while a new, empty CloudNativePG Cluster takes over the same hostname — and
    the instance will come up perfectly healthy in that state, which is what makes
    the mistake expensive rather than obvious.

    The guard exists because Terraform's own does not fire here: prevent_destroy
    protects a resource that is still in the configuration, and removing a module
    block orphans its resources instead — orphans are destroyed without consulting
    a lifecycle block that no longer exists. Measured, not assumed.

    Leave false unless you have dumped the data or are deliberately discarding it.
  EOT
  type        = bool
  default     = false
}

variable "postgres_instances" {
  description = <<-EOT
    Number of relational-database instances. 0 (default) derives it from var.ha —
    3 when true, 1 when false.

    🔴 1 OR 3, NEVER 2, and the middle is the whole reason this is not a plain
    bool. Synchronous replication needs one standby to confirm every commit, so
    at two instances losing EITHER one stalls every write — strictly worse for
    availability than the single node it replaced, while being better for
    durability. Three means a standby can go without the cluster losing its
    synchronous candidate. The module derives synchronous replication from this
    count, and the chart refuses to render the unsafe combination, so an
    unattainable value fails at plan/render time rather than at 3am.

    1 is a fully supported production topology (decision D4), not a degraded
    one: CloudNativePG owns the store on every install so that backup and
    point-in-time recovery exist independently of HA. At one instance there is
    no standby, commits are a local fsync, and the latency is what the
    StatefulSet gave.
  EOT
  type        = number
  default     = 0

  validation {
    condition     = contains([0, 1, 3, 5], var.postgres_instances)
    error_message = "postgres_instances must be 0 (derive from var.ha), 1, 3, or 5. 2 is deliberately rejected: it makes any standby loss a total write outage."
  }
}

variable "postgres_database" {
  description = "The relational store's bootstrap database. Instances do not use it: each gets its own database, owned by its own login, created by the provisioner role. 🔴 Contains an underscore so it can never equal an instance id (a DNS-1123 label), which names every instance database."
  type        = string
  default     = "dc_owner"
}

variable "postgres_username" {
  description = "The relational store's initdb owner. NOT a superuser, and not what services connect as -- each instance has its own login. 🔴 Contains an underscore so it can never equal an instance id, which names every instance login."
  type        = string
  default     = "dc_owner"
}

variable "postgres_provisioner_role" {
  description = "The base identity: the login role with CREATEROLE and CREATEDB that dcctl uses to create each instance's login and database. 🔴 Contains an underscore so it can never equal an instance id."
  type        = string
  default     = "dc_provisioner"
}

variable "postgres_storage" {
  description = "PersistentVolume size for the relational Postgres, PER INSTANCE. 🔴 This is spec.storage.size on the CloudNativePG Cluster, so the cluster-wide total is this times postgres_instances — three times this under --ha. It sized a single StatefulSet before A2.3."
  type        = string
  default     = "8Gi"
}

variable "postgres_storage_class" {
  description = "StorageClass for the relational Postgres data volume. Empty uses the cluster default (often reclaimPolicy Delete). FOR PRODUCTION DURABILITY set this to a StorageClass whose reclaimPolicy is Retain, so the underlying volume and its data outlive PVC/PV deletion and can still be recovered FROM. 🔴 That is the whole guarantee: a retained PV goes to Released and will not bind a new claim without someone clearing its claimRef, and a new CloudNativePG Cluster bootstraps via initdb rather than adopting an existing PGDATA. It does NOT mean a redeploy comes back up on the old volume — an earlier version of this description said it did. The supported recovery path is bootstrap.recovery from a backup."
  type        = string
  default     = ""
}

# --- Ingress + TLS (ADR-002) ----------------------------------------------------

variable "enable_ingress_nginx" {
  description = "Install the NGINX ingress controller. Set false if the cluster already has one."
  type        = bool
  default     = true
}

variable "ingress_nginx_namespace" {
  description = "Namespace for the ingress-nginx controller."
  type        = string
  default     = "ingress-nginx"
}

variable "ingress_nginx_chart_version" {
  description = <<-EOT
    ingress-nginx chart version. Pinned by default — 4.15.1 ships controller 1.15.1,
    which is what the module's snippet/risk-level values were verified against.
    Upstream keeps tightening that surface, and when it moves again the symptom is
    not a version diff: the instance chart's Ingress is REJECTED BY THE ADMISSION
    WEBHOOK partway through bootstrap.
  EOT
  type        = string
  default     = "4.15.1"

  # Fail closed. An empty string used to mean "install latest", which made the chart
  # version resolve at APPLY time — the chart repository became a dependency of
  # planning, and a repo hiccup surfaced as the helm provider's "inconsistent final
  # plan ... .version: was known, but now unknown", naming neither the chart nor the
  # network. The regex also refuses a helm version RANGE ("4.15.1 - 5.0.0"), which is
  # a constraint resolved at apply time wearing a pin's clothes.
  validation {
    condition     = can(regex("^v?[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.]+)?$", var.ingress_nginx_chart_version))
    error_message = "ingress_nginx_chart_version must be an exact chart version (e.g. \"1.2.3\" or \"v1.2.3\"); an empty value or a version range is resolved at apply time, which is not a pin."
  }
}

variable "ingress_use_host_port" {
  description = <<-EOT
    Local-kind only: bind the ingress controller to the node's host 80/443 and use
    a NodePort Service instead of a LoadBalancer (which stays <pending> on kind and
    times out the apply). Leave false on real clouds. deploy/local/up.sh sets true.
  EOT
  type        = bool
  default     = false
}

variable "ingress_class" {
  description = "IngressClass name the controller registers; set the same on the Helm chart's ingress.className."
  type        = string
  default     = "nginx"
}

variable "enable_cert_manager" {
  description = "Install cert-manager (+ CRDs). Set false if the cluster already has it."
  type        = bool
  default     = true
}

variable "cert_manager_namespace" {
  description = "Namespace for cert-manager."
  type        = string
  default     = "cert-manager"
}

variable "cert_manager_chart_version" {
  description = <<-EOT
    cert-manager chart version. Pinned by default — v1.21.1 is the version the CRD
    install path was verified against, and that path is the whole reason this module
    runs: with no cert-manager CRDs the instance chart's Issuer/Certificate cannot
    apply, and enable_database_backups fails outright.
  EOT
  type        = string
  default     = "v1.21.1"

  # Fail closed. An empty string used to mean "install latest", which made the chart
  # version resolve at APPLY time — the chart repository became a dependency of
  # planning, and a repo hiccup surfaced as the helm provider's "inconsistent final
  # plan ... .version: was known, but now unknown", naming neither the chart nor the
  # network. The regex also refuses a helm version RANGE ("4.15.1 - 5.0.0"), which is
  # a constraint resolved at apply time wearing a pin's clothes.
  validation {
    condition     = can(regex("^v?[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.]+)?$", var.cert_manager_chart_version))
    error_message = "cert_manager_chart_version must be an exact chart version (e.g. \"1.2.3\" or \"v1.2.3\"); an empty value or a version range is resolved at apply time, which is not a pin."
  }
}

# --- CloudNativePG (database HA + backup, ADR-020 A2 / ADR-028) -----------------

variable "enable_cnpg" {
  description = "Install the CloudNativePG operator (+ CRDs). Default-on for every install, HA or not: backup is not an HA feature, and one storage shape means HA becomes an instance count rather than a migration (decision D4). Set false if the cluster already runs CNPG."
  type        = bool
  default     = true
}

variable "cnpg_namespace" {
  description = "Namespace for the CloudNativePG operator and the backup plugin."
  type        = string
  default     = "cnpg-system"
}

variable "cnpg_chart_version" {
  description = "cloudnative-pg chart version. Pinned by default — 0.29.0 ships operator 1.30.0, the version the A2 spike validated the DNS contract, failover timing and synchronous semantics against."
  type        = string
  default     = "0.29.0"

  # Fail closed. An empty string used to mean "install latest", which made the chart
  # version resolve at APPLY time — the chart repository became a dependency of
  # planning, and a repo hiccup surfaced as the helm provider's "inconsistent final
  # plan ... .version: was known, but now unknown", naming neither the chart nor the
  # network. The regex also refuses a helm version RANGE ("4.15.1 - 5.0.0"), which is
  # a constraint resolved at apply time wearing a pin's clothes.
  validation {
    condition     = can(regex("^v?[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.]+)?$", var.cnpg_chart_version))
    error_message = "cnpg_chart_version must be an exact chart version (e.g. \"1.2.3\" or \"v1.2.3\"); an empty value or a version range is resolved at apply time, which is not a pin."
  }
}

variable "cnpg_plugin_chart_version" {
  description = "plugin-barman-cloud chart version. Pinned by default — 0.7.0 ships plugin v0.13.0. The plugin is 0.x and stands between the platform and its backups, so an unpinned minor is entitled to break the thing we least want broken."
  type        = string
  default     = "0.7.0"

  # Fail closed. An empty string used to mean "install latest", which made the chart
  # version resolve at APPLY time — the chart repository became a dependency of
  # planning, and a repo hiccup surfaced as the helm provider's "inconsistent final
  # plan ... .version: was known, but now unknown", naming neither the chart nor the
  # network. The regex also refuses a helm version RANGE ("4.15.1 - 5.0.0"), which is
  # a constraint resolved at apply time wearing a pin's clothes.
  validation {
    condition     = can(regex("^v?[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.]+)?$", var.cnpg_plugin_chart_version))
    error_message = "cnpg_plugin_chart_version must be an exact chart version (e.g. \"1.2.3\" or \"v1.2.3\"); an empty value or a version range is resolved at apply time, which is not a pin."
  }
}

variable "enable_database_backups" {
  description = <<-EOT
    WAL archiving, scheduled base backups and PITR for both database stores
    (ADR-028, ADR-020 A2.5). This is the master switch: it installs the Barman
    Cloud plugin AND provisions the destination the plugin writes to.

    🔴 Requires cert-manager to be installed and READY — the plugin's chart
    renders an Issuer and two Certificates, so it fails outright without the
    CRDs. Turning this off yields an install with database HA and NO backups,
    which is a real configuration (compact uses it) but must be a deliberate one:
    the difference is invisible from the Cluster resources.

    🔑 Until A2.5 this flag installed the PLUGIN and nothing else — no object
    store, no ObjectStore resources, no ScheduledBackup — so `true` meant "point-
    in-time recovery is possible in principle" while nothing was being archived
    anywhere. It now means what it says. Where the backups LAND is
    var.backup_destination, and an in-cluster destination is not off-site backup;
    read that variable before believing an instance is recoverable.
  EOT
  type        = bool
  default     = true
}

variable "backup_destination" {
  description = <<-EOT
    Where database backups go when enable_database_backups is on.

      in-cluster  Provision a single-replica MinIO in this cluster and archive to
                  it. The default, so that a plain bootstrap produces an instance
                  whose WAL is genuinely archived rather than one carrying a
                  backup plugin with nowhere to put anything.

      external    Archive to an S3-compatible endpoint you supply via
                  backup_endpoint_url + backup_access_key/backup_secret_key. The
                  recommended production configuration.

    🔴 `in-cluster` IS NOT OFF-SITE BACKUP. It shares the cluster's failure
    domain and, on a single-node install, the node's disk — lose the cluster and
    the backups go with it. What it does buy is the recovery that is actually
    needed most often: point-in-time recovery from an operator error, a bad
    migration or a bad delete, where the cluster is fine and the data is wrong.
    It also makes the restore drill exercise the same code path production uses.

    There is deliberately no third value meaning "backups on, destination none".
    That state is the one this slice exists to remove: a plugin installed, a flag
    reading true, and nothing archived anywhere. Set enable_database_backups to
    false instead, which is honest and is reported as such.
  EOT
  type        = string
  default     = "in-cluster"

  validation {
    condition     = contains(["in-cluster", "external"], var.backup_destination)
    error_message = "backup_destination must be \"in-cluster\" or \"external\"."
  }
}

variable "backup_endpoint_url" {
  description = <<-EOT
    S3 endpoint for backup_destination = "external", e.g.
    https://s3.eu-west-1.amazonaws.com or a MinIO/Ceph/R2 endpoint.

    🔴 Required when the destination is external, and refused when it is
    in-cluster — the in-cluster endpoint is an output of the object-store module,
    and accepting an override would let the two disagree. An empty value is the
    same string as "I forgot", so it is a validation error rather than a fallback
    to AWS's default endpoint.
  EOT
  type        = string
  default     = ""
}

variable "backup_bucket_rdb" {
  description = "Bucket for the relational store's backups. One bucket per store, never a shared bucket with two prefixes: two stores are two independent restore domains, and the core-data half is what the root-key escrow gates."
  type        = string
  default     = "devicechain-rdb"
}

variable "backup_bucket_tsdb" {
  description = "Bucket for the event store's backups. Separate from the relational one so event data can be restored without touching the control plane, and vice versa."
  type        = string
  default     = "devicechain-tsdb"
}

variable "backup_credentials_secret" {
  description = "Name of the Secret holding the credentials the archiver presents to an EXTERNAL backup destination, under keys ACCESS_KEY_ID / SECRET_ACCESS_KEY. 🔴 This tree is told the NAME, never the value: those credentials belong to somebody else's object store, so they are SUPPLIED rather than minted, and a supplied secret in a variable lands in the state file exactly as a generated one would. dcctl writes it from --backup-credentials-file before the apply. Unused when backup_destination is \"in-cluster\"."
  type        = string
  default     = "dc-backup-credentials"
}

variable "backup_server_name_rdb" {
  description = <<-EOT
    Path within the relational store's bucket. Empty means the Cluster's own name
    (`dc-rdb`), which is right for every ordinary install.

    🔴 A RESTORED CLUSTER MUST SET THIS, and it is exposed at the root precisely
    so that a restore does not require editing module source during an incident.
    CloudNativePG refuses to let a recovered cluster archive back over the path it
    recovered from, and the refusal is not a clean error: the cluster stays in
    `Setting up primary` indefinitely, logging `WAL archive check failed for
    server ...: Expected empty archive`. It does not fail the apply — it hangs
    half-built, which is a far worse thing to meet at 3am than a rejection.

    Set it to something new (e.g. "dc-rdb-restored-2026-07-28") on the cluster you
    recover INTO, so it starts a fresh timeline alongside the archive it read
    rather than writing into it.
  EOT
  type        = string
  default     = ""
}

variable "backup_schedule" {
  description = <<-EOT
    Cron schedule for the recurring base backup, applied to both stores.

    🔴 SIX FIELDS, NOT FIVE. CloudNativePG's cron carries a leading SECONDS
    field, unlike a Kubernetes CronJob. A five-field entry is accepted by the API
    and then never runs: the object exists, `kubectl get scheduledbackup` shows
    it, and no backup is ever taken. The default below is 03:00 daily. The chart
    counts the fields and refuses at render time.

    Empty disables the recurring base backup while leaving WAL archiving on,
    which is a destination that grows forever and restores nothing.
  EOT
  type        = string
  default     = "0 0 3 * * *"
}

# ---------------------------------------------------------------------------
# restore (ADR-028, ADR-020 A2.5)
#
# 🔴 THESE ARE REBUILD-TIME LEVERS, NOT REPAIR LEVERS. `spec.bootstrap` is read
# when CloudNativePG CREATES a Cluster, so pointing one of these at a store that
# already exists is expected to do nothing whatsoever — no error, no restore, a
# green apply. The procedure is: lose (or deliberately destroy) the instance,
# then rebuild it with these set. hack/dr-rig.sh rehearses exactly that.
#
# One pair per store rather than a single switch, because the two stores keep
# independent WAL timelines and are genuinely restored separately: an event
# store rewound to yesterday does not mean the control plane should be, and
# rewinding it anyway discards every tenant, device and rule created since.
# ---------------------------------------------------------------------------

variable "restore_rdb_from" {
  description = <<-EOT
    Recover the RELATIONAL store from this serverName (the folder inside
    backup_bucket_rdb) instead of initialising an empty one. Empty is a normal
    install.

    🔴 Setting this REQUIRES backup_server_name_rdb, set to something else.
    CloudNativePG refuses to archive into a non-empty archive, so a restored
    cluster pointed back at the path it recovered from comes up and then hangs
    in `Setting up primary` with its WAL going nowhere. The root guard below
    refuses the combination at PLAN time rather than letting an operator find it
    mid-incident.
  EOT
  type        = string
  default     = ""
}

variable "restore_rdb_target_time" {
  description = <<-EOT
    Point-in-time recovery target for the relational store: an RFC3339 or
    PostgreSQL timestamp. Empty replays the entire archive, which is what a
    hardware-loss restore wants.

    Set this for the OTHER kind of disaster — the one where the data was
    destroyed correctly, by a bad migration or a mistaken delete, and the goal
    is the state just before it. Recovery stops at the target, so pick a moment
    strictly before the damage.
  EOT
  type        = string
  default     = ""

  validation {
    condition     = var.restore_rdb_target_time == "" || var.restore_rdb_from != ""
    error_message = "restore_rdb_target_time is set but restore_rdb_from is empty, so nothing is being restored and the target would be silently ignored. Set restore_rdb_from, or drop the target."
  }
}

variable "backup_retention" {
  description = <<-EOT
    Recovery WINDOW to keep, e.g. "7d". Not a backup count: this guarantees the
    cluster stays restorable to any point in the window, so barman keeps the base
    backup predating the window plus every WAL since. Empty disables pruning.

    🔴 THIS AND backup_object_store_storage ARE ONE DECISION, and the first
    version of this configuration shipped them as two. Every scheduled backup is
    a FULL base backup, so a `Nd` window retains roughly `N+1` complete copies of
    BOTH databases:

        destination ≈ (retention_days + 1) × compressed(rdb + tsdb) + WAL

    WAL is the cheap term and can be ignored: `archive_timeout` forces a segment
    every 5 minutes, but a segment closed early is zero-filled past the switch
    record and gzips to tens of KiB, so both stores together cost well under a
    GiB per month. The base backups are the whole cost.

    At the shipped 20Gi destination that budget is roughly 2.5 GiB of combined
    compressed base backup — comfortable for a small-to-moderate instance at 7
    days, and NOT comfortable at 30. A 30-day window was the original default and
    it fills the shipped destination in about three weeks on an instance of any
    real size, monotonically, because pruning removes nothing until backups start
    ageing out of the window. What follows is the documented cascade: the
    destination fills, archiving fails, WAL accumulates on the DATABASES' volumes,
    and PostgreSQL stops.

    So: raising this REQUIRES raising backup_object_store_storage with it. There
    is no check that enforces it — the sizes depend on data nobody knows at plan
    time — which is why it is stated here rather than assumed.
  EOT
  type        = string
  default     = "7d"
}

variable "backup_object_store_storage" {
  description = <<-EOT
    Data volume for the in-cluster object store.

    🔴 SIZE IT WITH backup_retention, NOT WITH THE DATABASE VOLUMES. Every
    scheduled backup is a FULL base backup, so this holds roughly
    `retention_days + 1` complete compressed copies of BOTH databases, plus the
    WAL between them:

        destination ≈ (retention_days + 1) × compressed(rdb + tsdb) + WAL

    The default pairs 20Gi with a 7-day window, which budgets about 2.5 GiB of
    combined compressed base backup. Raise BOTH together or neither: the
    arithmetic is multiplicative in the retention window and no check can enforce
    it, because the compressed size of a database is not knowable at plan time.

    🔴 When it fills, archiving fails — and failed archiving does not stall
    commits, it accumulates WAL on the DATABASES' own volumes until those fill and
    PostgreSQL stops. A too-small destination takes the instance down by a route
    that points nowhere near a bucket. BackupDestinationAlmostFull fires at 85% to
    give warning, which is the only reason the failure is survivable.

    🔴 Growing this later may not work in place. It is a PVC, so expansion needs a
    StorageClass with allowVolumeExpansion — kind's default local-path has none,
    and every provisioner refuses a SHRINK. That last one bites a real path:
    re-running bootstrap on an existing instance with `--compact` asks for a
    smaller value than the default and the apply fails.
  EOT
  type        = string
  default     = "20Gi"
}

variable "backup_object_store_storage_class" {
  description = "StorageClass for the in-cluster object store's volume. Empty uses the cluster default."
  type        = string
  default     = ""
}

variable "allow_backup_destination_removal" {
  description = <<-EOT
    Proceed even though this apply destroys an in-cluster backup destination that
    currently holds backups.

    🔴 An ASSERTION THAT YOU HAVE HANDLED THE BACKUPS, not a migration — the same
    shape as allow_legacy_rdb_removal, and for the same reason Terraform's own
    prevent_destroy does not cover it: the object store is conditional on
    `enable_database_backups && backup_destination == "in-cluster"`, and dropping a
    module's count to zero ORPHANS its resources, which are then destroyed without
    consulting any lifecycle rule.

    What is at risk is the entire recovery window for BOTH databases — every base
    backup and every archived WAL segment, for the control plane and the event
    history alike. Nothing here can tell a copied archive from an abandoned one,
    so setting this is a claim only you can make.
  EOT
  type        = bool
  default     = false
}

# --- Observability (kube-prometheus-stack — Prometheus/Grafana/Alertmanager) -----

variable "enable_monitoring" {
  description = "Install the kube-prometheus-stack observability stack (Prometheus Operator + Prometheus + Grafana + Alertmanager). Default-on like Postgres/Timescale; set false if the cluster already has the Prometheus Operator, or to skip metrics collection entirely."
  type        = bool
  default     = true
}

variable "monitoring_namespace" {
  description = "Namespace for the monitoring stack."
  type        = string
  default     = "monitoring"
}

variable "monitoring_chart_version" {
  description = "kube-prometheus-stack chart version. Pinned by default — this chart's Operator CRDs are installed once and never upgraded by Helm, so an unpinned 'latest' drifting across bootstraps skews the operator against install-day CRDs. Bump deliberately."
  type        = string
  default     = "65.1.1"

  # Fail closed. An empty string used to mean "install latest", which made the chart
  # version resolve at APPLY time — the chart repository became a dependency of
  # planning, and a repo hiccup surfaced as the helm provider's "inconsistent final
  # plan ... .version: was known, but now unknown", naming neither the chart nor the
  # network. The regex also refuses a helm version RANGE ("4.15.1 - 5.0.0"), which is
  # a constraint resolved at apply time wearing a pin's clothes.
  validation {
    condition     = can(regex("^v?[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.]+)?$", var.monitoring_chart_version))
    error_message = "monitoring_chart_version must be an exact chart version (e.g. \"1.2.3\" or \"v1.2.3\"); an empty value or a version range is resolved at apply time, which is not a pin."
  }
}

variable "monitoring_slim" {
  description = "Reduce the footprint for a local/kind cluster: Prometheus keeps its TSDB on emptyDir (no PVC) and requests fewer resources. The bring-up sets this true on a local context."
  type        = bool
  default     = false
}

variable "monitoring_prometheus_retention" {
  description = "How long Prometheus retains samples (e.g. 15d). Lower it for a slim/local cluster on emptyDir."
  type        = string
  default     = "15d"
}

variable "monitoring_prometheus_storage" {
  description = "PVC size for the Prometheus TSDB when NOT slim. Ignored in slim mode (emptyDir)."
  type        = string
  default     = "20Gi"
}

variable "monitoring_storage_class" {
  description = "StorageClass for the Prometheus PVC; empty uses the cluster default. Ignored in slim mode."
  type        = string
  default     = ""
}

# --- Grafana SSO (ADR-047) — set by the bring-up when it provisions the client ----

variable "monitoring_grafana_oauth_enabled" {
  description = "Turn on Grafana OAuth SSO against user-management (ADR-047), operator/superuser-tier only. The bring-up sets this true once it has minted the client secret and computed the URLs below."
  type        = bool
  default     = false
}

variable "monitoring_grafana_oauth_client_id" {
  description = "OAuth client_id Grafana authenticates as (the confidential client seeded in user-management)."
  type        = string
  default     = "grafana"
}

variable "monitoring_grafana_oauth_client_secret" {
  description = "Cleartext Grafana OAuth client secret (user-management stores its bcrypt hash). Minted by the bring-up. Sensitive."
  type        = string
  default     = ""
  sensitive   = true
}

variable "monitoring_grafana_oauth_auth_url" {
  description = "Browser-facing authorize endpoint (public host via the /api/user-management ingress), e.g. https://<host>/api/user-management/oauth/authorize."
  type        = string
  default     = ""
}

variable "monitoring_grafana_oauth_token_url" {
  description = "Server-side token endpoint Grafana's pod calls — an IN-CLUSTER service URL (the pod cannot reach the public host), e.g. http://user-management.<instance>:8080/oauth/token."
  type        = string
  default     = ""
}

variable "monitoring_grafana_oauth_api_url" {
  description = "Server-side userinfo endpoint Grafana's pod calls — an in-cluster service URL, e.g. http://user-management.<instance>:8080/oauth/userinfo."
  type        = string
  default     = ""
}

variable "monitoring_grafana_root_url" {
  description = "External root URL Grafana is served at, e.g. https://<host>/grafana (drives root_url + serve_from_sub_path + the OAuth redirect base)."
  type        = string
  default     = ""
}

variable "monitoring_grafana_ingress_host" {
  description = "Host the /grafana ingress answers on (same host as the app ingress). Empty leaves Grafana ClusterIP + port-forward only."
  type        = string
  default     = ""
}
