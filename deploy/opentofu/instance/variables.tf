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

variable "instance_namespace" {
  description = "The instance's own namespace: `dci-` followed by the instance id, so instance `acme` runs in `dci-acme`. The broker, the event store and their Secrets live here, so two instances on one cluster share no names and a destroyed instance leaves nothing of its own behind. 🔴 IT IS NOT THE INSTANCE ID — the id still names the database, the database login, the Helm release (`dc-<id>`) and the messaging subject prefix, and this root never reconstructs one from the other: it is handed the namespace and uses it as one. 🔴 Deliberately NOT named `namespace`: dcctl routes each variable to every root that declares it, and the cluster root's `namespace` is the shared one."
  type        = string

  # The `dci-` prefix is REQUIRED, not merely expected, and this is the only place
  # outside dcctl that says so. dcctl computes this string and the Helm chart computes
  # it again from instance.id; if the two ever disagree, the broker and the event store
  # land in one namespace while every Deployment, Service and Secret lands in another —
  # an instance built in the wrong place, which nothing downstream reports as wrong. A
  # namespace that does not carry the prefix cannot be the one the chart will render
  # into, so refusing it here turns that disagreement into a failed first apply.
  validation {
    condition     = can(regex("^dci-[a-z0-9]([-a-z0-9]*[a-z0-9])?$", var.instance_namespace))
    error_message = "instance_namespace is the instance's NAMESPACE, not its id: it must be `dci-` followed by the instance id — for instance `acme`, pass `dci-acme` — and the whole string must be a DNS-1123 label (lower-case alphanumerics and `-`, starting and ending alphanumeric)."
  }

  # 54 = 4 + 50, and both terms come from somewhere else:
  #   4   the `dci-` prefix above.
  #   50  the instance id's own cap (deploy/helm/devicechain's values.schema.json caps
  #       instance.id at 50 — itself Helm's 53-character release-name limit less the
  #       `dc-` the release name carries).
  # A DNS-1123 LABEL, which a namespace is, is capped at 63, so 54 is the tighter of the
  # two bounds and the one worth enforcing: a longer value is either not a namespace this
  # platform produces or an id that the chart would have refused first.
  #
  # 🔴 This read `<= 50` before the prefix existed, when the namespace WAS the id. Left
  # that way it refuses every id longer than 46 characters — and refuses it at APPLY
  # time, after dcctl has already written the Instance declaration, the namespace and
  # every credential into the cluster.
  validation {
    condition     = length(var.instance_namespace) <= 54
    error_message = "instance_namespace must be at most 54 characters: the 4-character `dci-` prefix plus an instance id of at most 50, which is the id length the DeviceChain chart accepts. Shorten the instance id."
  }
}

variable "legacy_namespace" {
  description = "Where an instance built before the CloudNativePG cutover ran its database StatefulSets. Read only by the cutover guard, which refuses to orphan them."
  type        = string
  default     = "dc-system"
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
    streams — replicated servers, unreplicated data. `dcctl install --ha` sets
    both from one value and preflights that they agree; a direct tofu user must set
    the Helm value themselves.
  EOT
  type        = bool
  default     = false
}

variable "nats_cluster_replicas" {
  description = "Number of NATS servers. 0 (default) derives it from var.ha — 3 when true, 1 when false. Set explicitly for a topology the toggle cannot express (5). ODD ONLY, at most 5: RAFT commits on a majority, so an even cluster tolerates no more failures than the odd size below it while costing a server and a wider quorum, and JetStream refuses more than 5 replicas per stream. See the nats module variable for the full rationale, including why this is only half the HA toggle."
  type        = number
  default     = 0

  validation {
    condition     = contains([0, 1, 3, 5], var.nats_cluster_replicas)
    error_message = "nats_cluster_replicas must be 0 (derive from var.ha), 1, 3, or 5."
  }
}

variable "nats_prom_exporter" {
  description = "Run the prometheus-nats-exporter sidecar on each NATS server, publishing BROKER-side metrics (routes, RAFT/JetStream cluster health) on :7777. The PodMonitor that scrapes it is rendered by the DeviceChain Helm chart, not here — it is an Operator CRD, and rendering one from this root would fail the apply on a fresh cluster where the monitoring stack is still installing alongside it. Set false to drop the sidecar (dcctl's compact preset does)."
  type        = bool
  default     = true
}

# --- NATS (messaging + MQTT + JetStream KV, ADR-003/006/007) --------------------

variable "nats_chart_version" {
  description = <<-EOT
    Version of the nats Helm chart to install. See the chart_version variable in
    modules/nats for why this is pinned rather than tracked. In short, the module's
    config-adoption mechanism and
    its helm timeout are both sized against chart internals verified at this
    version, and the pod-template checksum includes the chart's own version label —
    so "latest" would roll the broker whenever upstream cuts a release.
  EOT
  type        = string
  default     = "2.14.4"

  # Fail closed. An empty string used to mean "install latest", which made the chart
  # version resolve at APPLY time — the chart repository became a dependency of
  # planning, and a repo hiccup surfaced as the helm provider's "inconsistent final
  # plan ... .version: was known, but now unknown", naming neither the chart nor the
  # network. The regex also refuses a helm version RANGE ("4.15.1 - 5.0.0"), which is
  # a constraint resolved at apply time wearing a pin's clothes.
  validation {
    condition     = can(regex("^v?[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.]+)?$", var.nats_chart_version))
    error_message = "nats_chart_version must be an exact chart version (e.g. \"1.2.3\" or \"v1.2.3\"); an empty value or a version range is resolved at apply time, which is not a pin."
  }
}

variable "nats_jetstream_storage" {
  description = <<-EOT
    PersistentVolume size for NATS JetStream file storage. The default must fit the
    platform's OWN stream set on a cold start: every per-suffix stream reserves its
    byte ceiling UP FRONT at creation, against max_file_store (90% of this, FLOORED
    to whole units of the size's own magnitude — 16Gi yields 14Gi, not 14.4Gi). The
    platform creates 16 such streams — a FIXED set (streams are per-suffix and cover
    every tenant via the wildcard subject), so this does not grow with tenant count.

    THIS IS PER NODE, not per cluster. JetStream reserves on the server that holds a
    replica, so a 3-node HA cluster (var.ha) provisions this volume three times and
    each node stores one copy of the same reservation. Do NOT multiply it by the
    replica factor — that would triple a budget that is already correct, and the
    only thing it would buy is the illusion of headroom. (The account-level quota
    WOULD multiply, which is exactly why the APP account is left on dynamic limits;
    see the landmine note in modules/nats/main.tf.)

    Sizing history, because getting this wrong fails in a confusing way: 8Gi (→7Gi
    ceiling) fit only ~7 streams at the old uniform 1 GiB bound, so a fresh bring-up
    crashlooped its last stream-creating services (device-management,
    event-processing, event-sources) with "insufficient storage resources available".
    That was first fixed by raising the PV to 32Gi, which worked but spent most of it
    on control-plane streams that never hold more than a few MiB.

    The bound is now split hot/cold in backend/core/streams (see streams.All): 7 hot
    streams at 1 GiB, 10 control-plane streams at 128 MiB, and the capture stream at
    256 MiB reserve 8704Mi (8.5Gi). The MQTT gateway's own streams — which
    nats-server creates UNBOUNDED, and which the platform bounds at startup so they
    cannot eat the rest — add 384Mi. The KV buckets are bounded on the same principle
    (see kv.All): 5 State buckets at 128Mi + 6 Cache buckets at 64Mi reserve a
    further 1024Mi. Total reserved: 9.875Gi.

    Why 16Gi and not 12Gi: at 12Gi the ceiling is 10Gi, leaving 128Mi unreserved —
    BELOW the 512Mi headroom floor the budget test asserts. A PV sized to the margin
    has to be moved for every stream or bucket the platform adds, and the failure for
    not doing so is the crashloop above rather than a test. 16Gi (→14Gi ceiling) leaves 4.125Gi, which is
    room for the reservation to grow by about 40%. The extra 4Gi of disk is the
    cheapest part of this deployment; the alternative was a budget where every new
    bucket is a deploy-time landmine.

    NEVER shrink this independently of the stream bounds, and do NOT size the PV as
    (sum of stream ceilings) / 0.9 — the flooring above makes that formula unsafe.
    A 9.875Gi sum / 0.9 rounds to an 11Gi PV, whose ceiling is floor(11 × 0.9) = 9Gi,
    which is BELOW the sum and brings the crashloop back. Pick the smallest whole
    magnitude where floor(magnitude × 0.9) >= the sum, then leave real margin above
    it. Raise it for real ingest volume.

    This value IS linked to a test. dcctl's TestRenderedReservationFitsTheJetStreamStore
    (backend/cli/bootstrap) reads this default out of the embedded infrastructure
    config, applies the flooring rule above, and checks the ceilings the Helm chart
    actually renders against it — so lowering this without lowering the stream bounds
    fails there rather than on someone's fresh install.

    CAVEAT — one copy is still un-linked. backend/core/config/instance_test.go carries
    its own `pvGi` constant mirroring this default, and lowering this value will not
    make those tests fail. They pin the arithmetic on the values core/config computes
    for itself, which is worth keeping; just do not read them as a guard on THIS
    variable. Changing this value means updating that constant by hand.
  EOT
  type        = string
  default     = "16Gi"
}

variable "nats_jetstream_max_file_store" {
  description = "Server-level max_file_store — the hard aggregate JetStream disk ceiling (ADR-023). Empty derives it as 90% of nats_jetstream_storage, FLOORED to a whole unit of that size's own magnitude (16Gi yields 14Gi, not 14.4Gi), leaving filesystem headroom; set explicitly to override. Must be <= nats_jetstream_storage. Note this is a PER-NODE ceiling: an HA cluster applies it on every server, each holding one replica."
  type        = string
  default     = ""
}

variable "nats_mqtt_reject_qos2_publish" {
  description = "Refuse MQTT QoS 2 PUBLISH at the broker (ADR-023). QoS 2 buys nothing over the platform's own event de-duplication and is the one gateway store a device can fill on purpose. Note the rejection tears down the CONNECTION rather than NACKing the message, so a device that publishes QoS 2 in a loop reconnects in a loop; QoS 0/1 are unaffected. See the nats module variable for the full rationale."
  type        = bool
  default     = true
}

variable "nats_enable_tls" {
  description = "Terminate TLS on the NATS client + MQTT listeners (ADR-025). Keep in sync with the services' instance config: when true the bring-up must thread the nats_ca output into infrastructure.nats.tls so clients dial over TLS. Set false only for plaintext debugging."
  type        = bool
  default     = true
}

variable "nats_ca_cert_pem" {
  description = "PEM-encoded certificate of the authority that signed the broker's server certificate (ADR-025). dcctl mints the authority and writes the TLS Secret the broker mounts BEFORE this apply, so only this public half crosses into the infrastructure state -- the private key that signs with it never does. Empty leaves the CA-only ConfigMap and the nats_ca output empty, which is a broker clients cannot verify."
  type        = string
  default     = ""
}

variable "nats_enable_auth" {
  description = "Enable broker authentication + the device auth-callout (ADR-025). Defaults false because it needs credentials minted out-of-band (nkeys aren't a TF primitive); the bring-up (dcctl / up.sh) mints them, sets this true, and threads the corresponding plaintext credential into the services' instance config (the broker gets the bcrypt hash). The broker flag and the client flag MUST agree."
  type        = bool
  default     = false
}

variable "nats_callout_issuer_public" {
  description = "Public account nkey (A...) the auth-callout signs device user JWTs with. Minted by the bring-up; required when nats_enable_auth is true."
  type        = string
  default     = ""
}

variable "nats_service_password_bcrypt" {
  description = "BCRYPT HASH ($2a$...) of the shared `dc_service` password, placed in the broker config (the plaintext goes only into the services' instance-config Secret; the broker bcrypt-compares). Minted by the bring-up; required when nats_enable_auth is true. Sensitive."
  type        = string
  default     = ""
  sensitive   = true
}

variable "nats_sys_password_bcrypt" {
  description = "BCRYPT HASH ($2a$...) of the `dc_sys` system-account password, read by the event-sources broker-presence tap. Minted by the bring-up alongside the service password; a separate credential because the system account observes every account's connections. Empty leaves SYS without users and the tap off. Sensitive."
  type        = string
  default     = ""
  sensitive   = true
}

variable "nats_mqtt_node_port" {
  description = <<-EOT
    Local-kind only: expose the MQTT gateway as a NodePort on this node port so a
    host device/tool reaches the broker at ssl://127.0.0.1:1883 through the kind
    1883->31883 host map. 0 = ClusterIP only (the cloud default; a NodePort would
    publish MQTT on every node IP). dcctl sets 31883 on a local context, matching the
    kind map; leave it 0 on real clouds. See the nats module var for the full story.
  EOT
  type        = number
  default     = 0
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

variable "backup_server_name_tsdb" {
  description = "Path within the event store's bucket. Empty means the Cluster's own name (`dc-tsdb`). See backup_server_name_rdb — a restored cluster must set this, or it hangs in `Setting up primary` rather than failing."
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

variable "restore_tsdb_from" {
  description = "Recover the EVENT store from this serverName instead of initialising an empty one. Same rules as restore_rdb_from, including the mandatory distinct backup_server_name_tsdb."
  type        = string
  default     = ""
}

variable "restore_tsdb_target_time" {
  description = "Point-in-time recovery target for the event store. Empty replays the entire archive. See restore_rdb_target_time."
  type        = string
  default     = ""

  validation {
    condition     = var.restore_tsdb_target_time == "" || var.restore_tsdb_from != ""
    error_message = "restore_tsdb_target_time is set but restore_tsdb_from is empty, so nothing is being restored and the target would be silently ignored. Set restore_tsdb_from, or drop the target."
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

# --- TimescaleDB (event hypertables, ADR-004) -----------------------------------

variable "timescale_image" {
  description = <<-EOT
    Operand image for the event store (ADR-020 A2.4).

    🔴 This must be a CloudNativePG OPERAND image that also carries TimescaleDB,
    and no community image satisfies both. It is OURS, built by
    deploy/images/timescaledb and published by .github/workflows/operand-image.yml,
    because every community option measured shipped a TimescaleDB below 2.26.4 —
    the release that fixes continuous-aggregate jobs sticking at
    `next_start = -infinity` after a failover, a state in which the database looks
    entirely healthy and has silently stopped aggregating.

    🔴 The tag here is a SECOND COPY. Its source of truth is
    deploy/images/timescaledb/versions.conf, from which the workflow computes
    `<pg_minor>-ts<timescaledb_version>-r<revision>`. Nothing links the two, so
    hack/check-tofu-validations.sh recomputes the tag from versions.conf and
    asserts this default matches it.
  EOT
  type        = string
  default     = "ghcr.io/devicechain-io/postgresql-timescaledb:17.10-ts2.28.3-r1"
}

variable "timescale_instances" {
  description = <<-EOT
    Number of event-store instances. 0 (default) derives it from var.ha — 3 when
    true, 1 when false. Same 1-or-3-never-2 rule as postgres_instances, and for
    the same reason; see that variable.

    This store runs `preferred` data durability rather than `required`, so a lost
    standby degrades it to asynchronous replication instead of stalling ingest.
    The instance-count rule still applies: two instances buy no fault tolerance
    worth the second node.
  EOT
  type        = number
  default     = 0

  validation {
    condition     = contains([0, 1, 3, 5], var.timescale_instances)
    error_message = "timescale_instances must be 0 (derive from var.ha), 1, 3, or 5. 2 is deliberately rejected: it costs a node without tolerating a standby loss."
  }
}

variable "allow_legacy_tsdb_removal" {
  description = <<-EOT
    Proceed even though this cluster still runs the pre-A2.4 event-database
    StatefulSet (dc-timescaledb-single).

    🔴 An ASSERTION THAT YOU HAVE HANDLED THE DATA, not a migration — the exact
    sibling of allow_legacy_rdb_removal, and see that variable for why Terraform's
    own prevent_destroy does not cover this. The data at risk here is all recorded
    device event history.
  EOT
  type        = bool
  default     = false
}

variable "timescale_database" {
  description = "The event store's one database, created by initdb. dcctl sets it to the instance id: every service connects to the database named after the instance, and none of them creates it."
  type        = string
  default     = "devicechain"
}

variable "timescale_username" {
  description = <<-EOT
    Application role for the event store. Owns the instance databases and is the
    identity every event-management connection uses.

    🔴 This was `postgres` for as long as this store was a plain StatefulSet, and
    A2.4 changes it to `devicechain` to match the relational store. That is not
    tidying. On the stock postgres image POSTGRES_USER *is* the superuser, so the
    platform has been connecting to the event store as a superuser all along;
    under CloudNativePG the application role is a distinct, unprivileged role and
    `postgres` is reserved for the operator.

    Measured on CNPG 1.30.0: a managed role named `postgres` is refused by the
    admission webhook, but `bootstrap.initdb.owner: postgres` is ACCEPTED — so the
    old value fails loudly only by accident of which fields the chart renders. The
    chart now refuses a reserved owner outright.

    Changing this changes the DSN, so it must move together with the event-store
    username in deploy/helm/devicechain/values.yaml, the compiled-in default in
    backend/core/config/instance.go and dcctl's shipped default CR.
  EOT
  type        = string
  default     = "devicechain"
}

variable "timescale_analytics_readers" {
  description = <<-EOT
    Read-only SQL/BI login roles on the event store -- the roles a Metabase,
    Grafana or Power BI connection authenticates as.

    Telemetry already lives in a Postgres-speaking database with continuous
    aggregates, so a BI tool needs no export and no second store. What it needs is
    a role that is safe to hand out, and this is where one is declared.

    🔴 THE ROLE NAME CARRIES THE TENANT, AND IT IS THE ONLY THING THAT DOES. A
    role named `analytics_acme` reads tenant `acme`; the read surface derives that
    from the connected role's own identity, which a client cannot change. Get the
    name wrong and the role reads a different tenant's telemetry, or -- if no
    tenant matches -- nothing at all. There is no second place to correct it.

    Each entry:
      name              must be `analytics_<tenant id>`
      connection_limit   REQUIRED, above 0. These sessions come out of the same
                         max_connections event-management's pool draws on, so an
                         unlimited role can stall ingest without failing loudly.
      password_secret    a kubernetes.io/basic-auth Secret in the INSTANCE's own
                         namespace (the event store's), holding `username` and
                         `password`. YOU create
                         it; CloudNativePG reconciles the role to match. The
                         password is deliberately not a variable here -- putting
                         it in this file would put it in OpenTofu state.
      reads_location     OPTIONAL, defaults to false. Whether this reader can read
                         device POSITIONS -- latitude, longitude, elevation,
                         accuracy, speed, heading.

    🔴 POSITION IS OFF BY DEFAULT, AND THAT IS THE PLATFORM'S OWN BOUNDARY RATHER
    THAN CAUTION APPLIED HERE. Everywhere else, reading where a device IS is a
    separate authority from reading what it MEASURES: knowing a vehicle's or a
    person's location differs in kind from knowing how warm it is, so that authority
    is deliberately absent from the read-only viewer baseline and is only ever held
    by explicit grant. This surface had no notion of it, so declaring any BI reader
    handed it every tracked position.

    A SQL session cannot be asked which authorities it holds -- it authenticates as
    a role and carries nothing else -- so the authority is a GRANT: position lives on
    a second group role, and `reads_location` is what puts this reader in it. The
    tenant filter is unchanged either way; a location reader reads its own tenant's
    positions and nobody else's.

    What an ordinary reader keeps is the base event envelope, which carries no
    coordinates. It can still see THAT a location event occurred, from which device
    and when -- the same line the API draws.

    Nothing further is needed: the role is a member of the reader group, which the
    event store grants the read surface to on every boot.

    🔴 BI ACCESS IS OPERATOR-DECLARED, NOT TIER-GOVERNED, and that divergence is
    deliberate rather than an omission. Every other per-tenant ceiling on this
    platform cascades tenant override -> tier -> platform default and is resolved by
    the enforcing service at request time. This one cannot be: the value is consumed
    once, by the database, when the role is created — and the platform's application
    role holds no CREATEROLE, so it could not apply a resolved value to a role even
    if it read one. The ceiling therefore lives where it binds, and the render-time
    check below is what keeps it honest.
  EOT
  type = list(object({
    name             = string
    connection_limit = number
    password_secret  = optional(string, "")
    reads_location   = optional(bool, false)
  }))
  default = []

  validation {
    condition     = alltrue([for r in var.timescale_analytics_readers : startswith(r.name, "analytics_") && length(r.name) > length("analytics_")])
    error_message = "Every analytics reader must be named analytics_<tenant id>; the read surface derives the tenant from the role name and a role outside that convention reads nothing."
  }

  validation {
    # 🔴 63 IS POSTGRESQL'S IDENTIFIER LIMIT, AND EXCEEDING IT IS SILENT. A longer name is
    # TRUNCATED with a NOTICE, not refused — measured: `analytics_` + 53 x's + `y` (64
    # bytes) becomes the 63-byte name, which is the role for the tenant `x`*53. So a reader
    # declared for one tenant reads a DIFFERENT one, and every check downstream agrees it is
    # correct, because by then the name really is the other tenant's. Tenant tokens are
    # allowed 128 characters, so this is reachable rather than theoretical.
    condition     = alltrue([for r in var.timescale_analytics_readers : length(r.name) <= 63])
    error_message = "An analytics reader's role name must be at most 63 characters, which caps the tenant id at 53: PostgreSQL truncates a longer identifier instead of rejecting it, and the truncated name can be another tenant's reader."
  }

  validation {
    # Neither group role is a reader. Declaring one here would give it LOGIN and a password,
    # and a role that can start a session is one whose own name the tenant derivation has to
    # keep refusing — a boundary better kept out of reach than kept correct.
    #
    # 🔴 BOTH NAMES ARE LISTED, and the second is the one that will be forgotten: it arrived
    # with the position split, it matches the reader prefix exactly as the first does, and
    # `analytics_location_reader` given LOGIN would resolve to a tenant called
    # `location_reader` — a legal tenant token.
    condition     = alltrue([for r in var.timescale_analytics_readers : !contains(["analytics_reader", "analytics_location_reader"], r.name)])
    error_message = "analytics_reader and analytics_location_reader are the read surface's group roles, not readers. Declaring either here would give it LOGIN; name the reader after its tenant instead."
  }
}

variable "timescale_analytics_reserved_connections" {
  description = <<-EOT
    Connections the platform itself must still be able to open on the event store
    after every analytics reader has taken its limit.

    Sized from the pools that actually exist rather than from a round number:
    event-management is the only service holding a pool against this store, capped
    at 20 (backend/core/rdb defaultMaxOpenConnections), and a RollingUpdate has two
    of its pods alive at once. 40 is that, and it is what a render-time check keeps
    available. Raise it before scaling event-management out, or the shortfall lands
    on whichever connection is opened last -- normally the application's.

    🔴 IT IS 20, NOT THE `maxConnections: 5` IN THE HELM VALUES, and the two are easy
    to confuse because they sit under the same store. That key is the LEGACY
    instance-level PostgresConfig.MaxConnections; backend/core/rdb/postgres.go states
    it is subsumed by the per-microservice MaxOpen/MaxIdle and no longer drives the
    pool. event-management sets neither, so poolSizing falls back to 20 — which is
    what the pod logs on startup ("max_open_connections":20). Deriving this number
    from the Helm value would under-reserve by a factor of four.
  EOT
  type        = number
  default     = 40
}

variable "timescale_storage" {
  description = "PersistentVolume size for the event store, PER INSTANCE. 🔴 This is spec.storage.size on the CloudNativePG Cluster, so the cluster-wide total is this times timescale_instances — three times this under --ha. It sized a single StatefulSet before A2.4."
  type        = string
  default     = "8Gi"
}

variable "timescale_storage_class" {
  description = "StorageClass for the event store data volume. Empty uses the cluster default (often reclaimPolicy Delete). FOR PRODUCTION DURABILITY set this to a StorageClass whose reclaimPolicy is Retain, so the underlying volume and its data outlive PVC/PV deletion and can still be recovered FROM. 🔴 That is the whole guarantee: a retained PV goes to Released and will not bind a new claim without someone clearing its claimRef, and a new CloudNativePG Cluster bootstraps via initdb rather than adopting an existing PGDATA. It does NOT mean a redeploy comes back up on the old volume — an earlier version of this description said it did. The supported recovery path is bootstrap.recovery from a backup."
  type        = string
  default     = ""
}

# 🔴 THE ARCHIVE CONTRACT, SUPPLIED — this root cannot derive any of it.
#
# The object store is a cluster prerequisite now: it lives in the cluster root,
# beside the shared relational store whose archive it also holds. This root writes
# the event store's archive into it and has no way to see it — the endpoint is a
# Service DNS name the other root composes, and the key names inside the Secret
# depend on which kind of store it turned out to be.
#
# 🔑 THE KEY NAMES ARE VALUES, NOT ASSUMPTIONS, and that is the point of taking
# four variables where a `backup_destination` enum would have taken one. An
# in-cluster MinIO names them MINIO_ROOT_USER/MINIO_ROOT_PASSWORD; an external
# store names them ACCESS_KEY_ID/SECRET_ACCESS_KEY. A copy of that decision here
# could disagree with the store that actually exists, and the symptom would be an
# archiver that authenticates with nothing and fails per WAL segment without
# stalling a single write. dcctl reads all four from the cluster root's outputs.

variable "backup_access_key_id_key" {
  description = "Key within backup_credentials_secret holding the access key ID. Read from the cluster root's output of the same name; never guessed from the destination."
  type        = string
  default     = "MINIO_ROOT_USER"
}

variable "backup_secret_access_key_key" {
  description = "Key within backup_credentials_secret holding the secret access key. Read from the cluster root's output of the same name; never guessed from the destination."
  type        = string
  default     = "MINIO_ROOT_PASSWORD"
}
