---
sidebar_position: 4
title: dcctl 命令参考
---

# dcctl 命令参考 {#dcctl-reference}

列出所有 `dcctl` 命令及其标志和默认值。本页由二进制文件中的命令定义自动生成，因此不会落后于实现；在终端中运行 `dcctl <命令> --help` 可看到相同的文本。

:::note
下面的命令和标志参考为自动生成，仅提供英文版本。
:::

## dcctl {#dcctl}

DeviceChain CLI.

```
dcctl [command]
```

### Commands

* [dcctl bootstrap](#dcctl-bootstrap)	 - Bootstrap a DeviceChain instance
* [dcctl dead-letters](#dcctl-dead-letters)	 - Inspect work the platform accepted and then gave up on
* [dcctl destroy](#dcctl-destroy)	 - Destroy a DeviceChain instance
* [dcctl ha](#dcctl-ha)	 - Inspect an instance's high-availability posture
* [dcctl install](#dcctl-install)	 - Prepare a cluster for DeviceChain instances
* [dcctl instances](#dcctl-instances)	 - Inspect the DeviceChain instances on this machine
* [dcctl preflight](#dcctl-preflight)	 - Check local prerequisites for installing a cluster
* [dcctl presence](#dcctl-presence)	 - Inspect and repair device presence
* [dcctl resgen](#dcctl-resgen)	 - Generate configuration resources
* [dcctl secrets](#dcctl-secrets)	 - Inspect and verify instance secret material
* [dcctl sim](#dcctl-sim)	 - Create and drive DeviceChain simulations
* [dcctl upgrade](#dcctl-upgrade)	 - Move a live instance onto a released version
* [dcctl version](#dcctl-version)	 - Get version info

## dcctl bootstrap {#dcctl-bootstrap}

Bootstrap a DeviceChain instance

### Synopsis

Provisions a usable DeviceChain instance on the given provider (e.g. "local")

```
dcctl bootstrap <provider> <instance> [flags]
```

### Options

```
      --allow-legacy-db-removal         proceed even though this cluster still runs the pre-CloudNativePG event-store StatefulSet (dc-timescaledb-single). 🔴 This ASSERTS THAT YOU HAVE HANDLED THE DATA — it is not a migration, nothing verifies it, and applying with it set destroys that StatefulSet and brings up an empty database on the same hostname. Dump first, or use it deliberately to discard a local instance
      --build                           build images from source into a local registry (developer path; requires source + ko)
      --cluster string                  local provider: the kind cluster 'dcctl install local' prepared (default "devicechain")
      --dev                             local-developer preset: --build --host localhost --no-tls --yes, and implies --no-escrow (a zero-config http://localhost/ bring-up); rejects contradictory flags
      --dry-run                         print what would happen without applying changes
      --enable-area strings             additionally deploy a functional area on TOP of the profile (repeatable, e.g. --enable-area lwm2m-ingest --enable-area sparkplug-ingest). Composes with the cluster's --compact setting; validated against the area catalog (unknown area or unmet hard dependency fails before any cluster spin-up)
      --escrow-file string              where to write the encrypted root-key escrow artifact (default ~/.devicechain/escrow/<instance>-rootkey.escrow). Refuses a path inside ~/.devicechain/instances/<instance>, which 'dcctl destroy' deletes
      --escrow-passphrase-file string   read the escrow passphrase from this file instead of $DCCTL_ESCROW_PASSPHRASE or an interactive prompt (trailing newline stripped)
  -h, --help                            help for bootstrap
      --host string                     ingress host to expose the instance on (default devicechain.local; use 'localhost' for a local cluster to skip the /etc/hosts edit)
      --kube-context string             build the instance on the cluster this context reaches, prepared with 'dcctl install --kube-context'
      --lwm2m-identities string         path to a JSON file of LwM2M DTLS-PSK credentials to provision: [{identity, psk(base64), tenant, externalId, deviceTypeToken, autoRegister}]. Renders the PSKs into a chart-owned Secret and binds each to lwm2m-ingest; implies --enable-area lwm2m-ingest. Validated up front (short PSK / missing tenancy fails before any cluster). Re-running bootstrap WITHOUT this flag removes the provisioned credentials
      --no-escrow                       do NOT escrow the secret-store root key. The key then exists only inside the cluster: losing the cluster makes every stored secret permanently unreadable, even from a database backup, because no DeviceChain backup contains etcd. For throwaway instances only; implied by --dev
      --no-tls                          serve plain HTTP instead of a self-signed cert (with --host localhost, a zero-config http://localhost/). On by default on a cluster installed without cert-manager, where --no-tls=false is refused
      --profile string                  configuration profile to apply
      --registry string                 image registry to deploy from (default: published ghcr.io/devicechain-io, or localhost:5000 with --build)
      --restore-root-key string         disaster recovery: seed the instance's secret-store root key FROM this escrow artifact instead of minting a fresh one, so a rebuilt cluster can read secrets restored from a database backup. Needs the artifact's passphrase
      --restore-tsdb-at string          stop the event store's recovery at this RFC3339 timestamp instead of replaying the whole archive. For the disaster where the data was destroyed correctly — a mistaken delete — so pick a moment strictly before the damage. Needs --restore-tsdb-from
      --restore-tsdb-from string        disaster recovery: recover the EVENT store from this archive path (the serverName inside the backup bucket, e.g. dc-tsdb) instead of initialising an empty database. 🔴 Only takes effect when the cluster is CREATED — recover by destroying the instance and rebuilding it with this set, not by re-running against a live one. When the backups are in the cluster's in-cluster object store, destroy with --keep-backups: a destroy without it deletes the archive this reads
      --skip-preflight                  skip the local-system preflight checks
      --version string                  image version/tag to deploy (default: the published release version, or 'dev' with --build)
  -y, --yes                             assume yes for prompts
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI

## dcctl dead-letters {#dcctl-dead-letters}

Inspect work the platform accepted and then gave up on

### Synopsis

Inspect work the platform accepted and then gave up on.

Four consumers record a dead letter when they have retried a message to its delivery
cap and it still cannot be completed: a detection whose actions could not be
dispatched, an alarm that reached nobody, a device's answer that could not be
recorded against its command, and an alarm edge that could not be applied.

NOTHING REPLAYS THESE. The record exists so a failure is visible and diagnosable
instead of being a log line nobody read; the consequences of the failure itself stand
either way. An alarm that was not raised was not raised.

This reads the operator plane and authenticates as an identity, not as a tenant.

### Options

```
      --email string      identity to authenticate as (required)
  -h, --help              help for dead-letters
      --password string   password for the identity (required)
  -s, --server string     instance host for platform API calls (default "localhost")
      --tls               use https for platform endpoints
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI
* [dcctl dead-letters list](#dcctl-dead-letters-list)	 - List dead letters, newest first

## dcctl dead-letters list {#dcctl-dead-letters-list}

List dead letters, newest first

### Synopsis

List dead letters, newest first.

Narrow with --tenant, --kind, --source and --since. Reading is bounded by --pages:
when more records exist than were fetched, the output says so rather than letting a
truncated list read as a complete one.

```
dcctl dead-letters list [flags]
```

### Options

```
  -h, --help            help for list
      --kind string     only this kind: detection-action, notification, command-response, connector-dispatch, event, command, control-fact
      --page int        records per server round-trip (default 50)
      --pages int       how many pages to fetch before stopping (default 4)
      --since string    only records from this RFC3339 time onward (e.g. 2026-09-04T00:00:00Z)
      --source string   only records written by this functional area
  -t, --tenant string   only this tenant's records
```

### Options inherited from parent commands

```
      --email string      identity to authenticate as (required)
      --password string   password for the identity (required)
  -s, --server string     instance host for platform API calls (default "localhost")
      --tls               use https for platform endpoints
```

### SEE ALSO

* [dcctl dead-letters](#dcctl-dead-letters)	 - Inspect work the platform accepted and then gave up on

## dcctl destroy {#dcctl-destroy}

Destroy a DeviceChain instance

### Synopsis

Removes a DeviceChain instance — the inverse of bootstrap.

This deletes the instance and ALL ITS DATA, in this order: its Helm release; its
own infrastructure — the broker and event store — by "tofu destroy" over the
instance's infrastructure state; its database and login on the shared relational
store; its namespace, waiting until it is gone; and, last, its local state under
~/.devicechain/instances/\<instance>. The root-key escrow is kept. A destroy that
fails part-way keeps the local state, and running it again resumes.

Once the namespace is gone, destroy also removes the instance's event-store
backups (WAL archive and base backups) from the cluster's in-cluster object store:
everything under the exact path the event store was archiving to, read from it
before anything was changed, and it checks that the path is empty afterwards.
Backups in an object store you supplied (install --backup-credentials-file) are
never deleted; destroy prints where they are. --keep-backups keeps in-cluster
backups too — use it when you mean to rebuild the instance from them with
"dcctl bootstrap --restore-tsdb-from", because a destroy without it deletes the
archive that restore would read. If the object store cannot be reached, destroy
still finishes and names the archive it left.

If the instance's infrastructure state is missing or empty but its broker or event
store is running, or the state cannot be read, or it still holds the cluster's shared
prerequisites (an instance built by an older dcctl), destroy refuses before changing
anything. --without-state removes such an instance anyway: it skips "tofu destroy"
and removes the instance by its Helm release, database and login, and namespace,
saying that tofu destroy was skipped and what it left on the cluster.

The cluster, and the shared prerequisites "dcctl install" put there, are never
touched — other instances may be using them, and destroy leaves the cluster
running whether or not dcctl created it. To delete a local cluster, do it by
hand: kind delete cluster --name \<name>

Which cluster the instance is in comes from a record written at bootstrap, not
from the instance's name; --kube-context overrides it. An instance bootstrapped
before dcctl recorded this has no record, and destroy falls back to guessing the
cluster from the instance name — saying so as it goes. Run "dcctl instances list"
to see which instances are in that state. If the cluster is already gone, only
the local state is cleared.

Use --all to destroy every instance on this machine.

```
dcctl destroy <provider> <instance> [flags]
```

### Options

```
      --all                   destroy EVERY instance on this machine (takes no arguments)
      --dry-run               print what would happen without destroying anything
  -h, --help                  help for destroy
      --keep-backups          keep the instance's event-store backups in the cluster's in-cluster object store (they are removed by default; backups in an object store you supplied are never removed). Use it to rebuild the instance from them with --restore-tsdb-from
      --kube-context string   kube-context to target (default: the cluster recorded at bootstrap)
      --without-state         skip tofu destroy and remove the instance by its release, database, login and namespace (for an instance whose infrastructure state is lost)
  -y, --yes                   assume yes for prompts
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI

## dcctl ha {#dcctl-ha}

Inspect an instance's high-availability posture

### Options

```
  -h, --help   help for ha
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI
* [dcctl ha verify](#dcctl-ha-verify)	 - Assert an instance's broker actually holds the replication it declares
* [dcctl ha verify-db](#dcctl-ha-verify-db)	 - Assert a database store actually holds the replication it declares

## dcctl ha verify {#dcctl-ha-verify}

Assert an instance's broker actually holds the replication it declares

### Synopsis

Assert, from live broker state, that an instance's JetStream streams, KV
buckets, durable consumers and NATS pods are replicated the way the instance
declares.

This reads the broker, not the deployment. The failure it exists to catch is an
instance that looks highly available from every rendered artifact -- a three-node
NATS cluster, three healthy pods, the HA toggle on -- while every stream and
bucket on it is single-replica: three times the compute, zero node failures
survived. Nothing about that state is visible from Helm values, OpenTofu state,
or pod health, because all three describe what was asked for.

The declared replica factor comes from the instance-config Secret the pods mount,
so the comparison is between what this instance claims and what its broker holds.

Exit status is the result: 0 when every assertion holds, 1 when any does not.

```
  dcctl ha verify --instance default
```

Use --expect-fail to invert that, for the negative control. A check suite that
cannot fail asserts nothing, so the drill runs the SAME command against a
single-node instance and requires it to report failures:

```
  dcctl ha verify --instance default --replicas 3 --expect-fail
```

```
dcctl ha verify [flags]
```

### Options

```
      --expect-fail           invert the exit status: succeed only if the check FAILS (the negative control)
  -h, --help                  help for verify
      --instance string       instance id; its services and broker run in namespace dci-<instance> (default "default")
      --kube-context string   kubeconfig context (default: current context)
      --nats-url string       dial this NATS URL instead of opening a port-forward
      --probe-mqtt            open one MQTT connection first so the broker's $MQTT_* streams exist and their replica factor can be observed. MUTATES the broker (it creates those streams if no client has ever connected), so it is off by default
      --replicas int          override the declared replica factor (default: read from the instance's deployed configuration)
      --settle duration       keep re-checking for this long while assertions fail, for the interval in which a RAFT peer set is reconfiguring or a consumer group is remapping. Never turns a failure into a pass: on expiry the last assertion report is returned in full (if the last attempt could not collect state at all, that collection error is returned instead). 0 disables it (default 1m30s)
      --timeout duration      bound the whole check (default 5m0s)
```

### SEE ALSO

* [dcctl ha](#dcctl-ha)	 - Inspect an instance's high-availability posture

## dcctl ha verify-db {#dcctl-ha-verify-db}

Assert a database store actually holds the replication it declares

### Synopsis

Assert, from live PostgreSQL and Kubernetes state, that a CloudNativePG
database store is replicated the way it is expected to be.

This is the database sibling of "ha verify", and it exists for the same reason:
every artifact that describes the store describes what was ASKED for. A Cluster
with three ready pods, a green apply and a healthy operator can still be
replicating asynchronously -- Helm accepts a misspelled field and the Kubernetes
API server prunes it silently, so a one-character slip in a chart template
removes synchronous replication without removing anything visible.

So the checks run against the running server:

```
  the instance count and pod placement  <- the Kubernetes API
  the alias Service and its endpoints   <- the Kubernetes API
  synchronous_standby_names             <- PostgreSQL
  connected + synchronous standbys      <- PostgreSQL
```

Exit status is the result: 0 when every assertion holds, 1 when any does not.

```
  dcctl ha verify-db --cluster dc-rdb --require-synchronous
```

Use --expect-fail for the negative control, stating the topology the store is
expected NOT to hold:

```
  dcctl ha verify-db --cluster dc-rdb --instances 3 --require-synchronous --expect-fail
```

```
dcctl ha verify-db [flags]
```

### Options

```
      --alias-service string   the Service name clients use, whose selector must be the operator-maintained primary alias; empty skips that check (and says so in the report) (default "dc-postgresql")
      --cluster string         the CloudNativePG Cluster object to check (default "dc-rdb")
      --durability string      the dataDurability this store is expected to declare, checked only with --require-synchronous. Empty means "required". The event store runs "preferred" so a lost standby degrades it to asynchronous replication instead of applying backpressure to ingest, so checking it against "required" would report a correct configuration as broken
      --expect-fail            invert the exit status: succeed only if the check FAILS (the negative control)
  -h, --help                   help for verify-db
      --instances int          expected instance count (default: read from the live Cluster spec). State it explicitly for the negative control — read from the spec, a single-instance store expects one instance and duly has one, so it can never fail
      --kube-context string    kubeconfig context (default: current context)
      --namespace string       namespace holding the CloudNativePG Cluster: dc-system for the shared relational store (dc-rdb), dci-<instance> for that instance's event store (dc-tsdb) (default "dc-system")
      --require-synchronous    demand synchronous replication. Deliberately NOT derived from the Cluster spec: a pruned or misspelled field leaves a spec that asks for nothing, which a spec-derived check would happily confirm
      --settle duration        keep re-checking for this long while assertions fail, for the interval in which a failover is completing or a standby rejoining. Never turns a failure into a pass: on expiry the last assertion report is returned in full (if the last attempt could not collect state at all, that collection error is returned instead). 0 disables it (default 1m30s)
      --timeout duration       bound the whole check (default 5m0s)
      --timescale-jobs         also assert TimescaleDB background-job health across every database on the server. Continuous aggregates, retention and compression are all background jobs: if the scheduler stops, the database stays up, replicates, fails over and passes every other check here while silently no longer aggregating
```

### SEE ALSO

* [dcctl ha](#dcctl-ha)	 - Inspect an instance's high-availability posture

## dcctl install {#dcctl-install}

Prepare a cluster for DeviceChain instances

### Synopsis

Prepares a cluster once, so that any number of instances can be built on it with
"dcctl bootstrap".

For the local provider it creates a kind cluster (named by --cluster, default
"devicechain") if there is none, or uses the one that exists. --kube-context installs
into an existing cluster instead, which dcctl never creates or deletes.

It installs what every instance on the cluster shares: the DeviceChain operator and
its CRDs, the relational store and the backup object store in namespace dc-system,
and the CloudNativePG operator, cert-manager, ingress and the monitoring stack each
in a namespace of its own. It creates the base database identity each instance's own
login is made with, and records the install in the cluster. Every bootstrap follows
that record: an instance on an --ha cluster is HA, an instance on a --compact
cluster is compact.

The operator and its CRDs are the cluster's, not any instance's — there is one copy
shared by every instance, so the release a cluster is prepared at is chosen here with
--version, and moving it is this command's job rather than a side effect of building
or upgrading one instance.

--database-node-selector places the relational store, and the event store of every
instance bootstrapped on the cluster, on the nodes it names; --database-toleration lets
them onto nodes tainted to keep other workloads off.

Running it again converges. Changing its settings is refused while any instance runs on
the cluster, with one exception: the connection budget may be raised.

```
dcctl install <provider> [flags]
```

### Options

```
      --allow-legacy-db-removal          proceed even though this cluster still runs the pre-CloudNativePG relational StatefulSet (dc-postgresql). 🔴 This ASSERTS THAT YOU HAVE HANDLED THE DATA — applying with it set destroys that StatefulSet and brings up an empty database on the same hostname
      --backup-credentials-file string   send database backups to an object store you already own, described by this JSON file: {endpointUrl, bucketRdb, bucketTsdb, accessKeyId, secretAccessKey}. Without it the cluster provisions its own in-cluster store, which lives in the same failure domain as the databases it backs up. Keep the file readable only by you
      --backup-snapshot-class string     take each database's daily base backup as a CSI volume snapshot with this VolumeSnapshotClass instead of a full copy in the backup store. A full copy still goes to the store weekly, WAL archiving is unchanged, and restores still read the store. The class must exist, use deletionPolicy Delete, and belong to the driver that provisions the database volumes. Every instance on the cluster follows it
      --build                            developer path: ko-build the operator image from this source checkout and push it to a local registry, instead of pulling a published one. Builds ONLY the operator; the service images are an instance's and are built by dcctl bootstrap --build
      --cluster string                   local provider: the kind cluster to create or use (default "devicechain")
      --compact                          small-footprint preset for the cluster and every instance on it: smaller volumes, lowered JetStream/KV ceilings and scheduling requests, no monitoring stack, and — unless --no-tls=false — no cert-manager and therefore no database backups. Instances on a compact cluster keep the default profile or a smaller one
      --database-node-selector strings   run the databases only on nodes with this label, as key=value (repeatable; every label must match). Applies to the relational store and to the event store of every instance bootstrapped on the cluster. Refused when too few schedulable nodes match
      --database-toleration strings      let the databases run on nodes with this taint, written as kubectl taint writes it: key=value:Effect, or key:Effect for any value (repeatable). Needs --database-node-selector
      --dev                              local-developer preset: --build --yes (builds the operator image from this source checkout); rejects contradictory flags
      --dry-run                          print what would happen without applying changes
      --ha                               a replicated relational store (3 CloudNativePG instances, synchronous), and every instance bootstrapped on this cluster HA too: a 3-server NATS cluster with replicated streams and a replicated event store. Needs at least 3 schedulable nodes; database volumes are sized per instance
  -h, --help                             help for install
      --kube-context string              install into this existing cluster instead of a local kind cluster (never created or deleted by dcctl)
      --max-connections int              the relational store's connection budget (default 600 on a first install, and what the cluster has on a re-run). Each instance reserves (its relational services x 40) of it when bootstrapped; the default admits two default-profile instances
      --no-cnpg                          skip the CloudNativePG operator and the database backup plugin — for a cluster that ALREADY runs CNPG, since Helm cannot adopt objects another installer created
      --no-monitoring                    skip the monitoring stack (Prometheus/Grafana); instances on the cluster render no ServiceMonitors or alerts
      --no-tls                           with --compact: instances serve plain HTTP, so cert-manager is not installed and database backups (whose plugin needs it) are off. --compact --no-tls=false keeps both
      --registry string                  pull the operator image from this registry (default: the published registry, or the local one with --build)
      --restore-rdb-at string            stop the relational store's recovery at this RFC3339 timestamp instead of replaying the whole archive. For the disaster where the data was destroyed correctly — a bad migration, a mistaken delete — so pick a moment strictly before the damage. Needs --restore-rdb-from
      --restore-rdb-from string          disaster recovery: recover the RELATIONAL store from this archive path (the serverName inside the backup bucket, e.g. dc-rdb) instead of initialising an empty database. 🔴 Only takes effect when the store is CREATED — recover by installing into a cluster whose relational store is not there, not by re-running against a live one
      --skip-preflight                   skip the local-system preflight checks
      --version string                   install the operator at this released tag (default: the release this dcctl was built for). This is the CLUSTER's version — the instances on it carry their own
  -y, --yes                              assume yes for prompts
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI

## dcctl instances {#dcctl-instances}

Inspect the DeviceChain instances on this machine

### Options

```
  -h, --help   help for instances
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI
* [dcctl instances list](#dcctl-instances-list)	 - List the instances bootstrapped on this machine and the clusters they live in
* [dcctl instances reclaim](#dcctl-instances-reclaim)	 - Take the cluster lock from an operator whose run has stopped
* [dcctl instances release](#dcctl-instances-release)	 - Remove an instance declaration without destroying the instance

## dcctl instances list {#dcctl-instances-list}

List the instances bootstrapped on this machine and the clusters they live in

### Synopsis

Lists every instance dcctl has state for, the cluster it was bootstrapped into,
and whether that cluster is still there.

The cluster column is read from a record written at bootstrap. An instance created
before dcctl recorded that shows as "no record" — destroy still works on it, but it
falls back to guessing the cluster from the instance name, which is wrong for any
instance bootstrapped with --kube-context.

A row reading "cluster gone" is an instance whose cluster has been deleted out from
under it, leaving only local state. That is the normal end state of a validation rig
run, and clearing it is what `dcctl destroy` does.

```
dcctl instances list [flags]
```

### Options

```
  -h, --help   help for list
```

### SEE ALSO

* [dcctl instances](#dcctl-instances)	 - Inspect the DeviceChain instances on this machine

## dcctl instances reclaim {#dcctl-instances-reclaim}

Take the cluster lock from an operator whose run has stopped

### Synopsis

Takes the lock a dcctl run holds while it applies to a cluster.

A run holds the lock and renews it continuously. If the process is killed without
being able to give it back — a lost laptop, a dead SSH session, an OOM — the lock
stays until somebody takes it.

This command refuses until the lock has gone a full lease duration with nothing
touching it, which it verifies by watching rather than by comparing timestamps, so
that two machines whose clocks disagree cannot produce a wrong answer. That check
takes about a minute and there is no way to skip it.

🔴 It cannot tell a dead process from one that is merely stopped — a suspended VM
or a closed laptop lid looks exactly the same from here, and no amount of waiting
changes that. Confirm the other process is really gone before you take its lock.
You will be asked to type the holder's identity back, which is there to make you
read it.

```
dcctl instances reclaim [flags]
```

### Options

```
  -h, --help                  help for reclaim
      --kube-context string   kube context of the cluster whose lock to take (required off the machine that bootstrapped it)
```

### SEE ALSO

* [dcctl instances](#dcctl-instances)	 - Inspect the DeviceChain instances on this machine

## dcctl instances release {#dcctl-instances-release}

Remove an instance declaration without destroying the instance

### Synopsis

Removes the finalizer from an instance declaration and deletes it.

🔴 THIS DESTROYS NOTHING. The namespaces, the databases, the volumes and the
workloads are all still there afterwards, and dcctl will no longer have a
declaration describing them. That is the point: it is the escape hatch for a
declaration that cannot be deleted because the operator that would clear its
finalizer is gone.

If what you want is to remove the instance, use `dcctl destroy`, which
clears the finalizer itself as its last step.

```
dcctl instances release <instance> [flags]
```

### Options

```
  -h, --help                  help for release
      --kube-context string   kube context of the cluster holding the declaration
      --yes                   skip the confirmation prompt
```

### SEE ALSO

* [dcctl instances](#dcctl-instances)	 - Inspect the DeviceChain instances on this machine

## dcctl preflight {#dcctl-preflight}

Check local prerequisites for installing a cluster

### Synopsis

Proactively diagnoses local-system prerequisites (tools, docker, kernel limits, disk, ports, kube contexts) before 'dcctl install' prepares a cluster, with an actionable fix for each problem found.

These are the prerequisites for standing a cluster up, not for building an instance on one: a bootstrap runs against a cluster 'dcctl install' has already prepared, so run this before the install.

```
dcctl preflight [provider] [flags]
```

### Options

```
  -h, --help   help for preflight
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI

## dcctl presence {#dcctl-presence}

Inspect and repair device presence

### Synopsis

Operate on the live presence projection of a tenant's devices.

A device's presence is either INFERRED — derived from its own traffic, and swept to
offline after a period of silence — or ASSERTED, meaning some event source is telling
the platform directly whether that device is connected.

An asserted device is judged only by that source. If the source stops running, the
platform stops hearing about the device and neither of the two mechanisms that would
ordinarily repair a stale row can touch it: the inactivity sweep skips asserted rows,
and a data event cannot flip one. The device keeps whatever presence it last had, for
as long as the source stays away.

'presence demote' is the way back: it releases a source's custody of its devices and
hands them to inference again.

### Options

```
      --email string      identity to authenticate as; it must be a member of the tenant (required)
  -h, --help              help for presence
      --password string   password for the identity (required)
  -s, --server string     instance host for platform API calls (default "localhost")
  -t, --tenant string     tenant whose devices are affected (required)
      --tls               use https for platform endpoints
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI
* [dcctl presence demote](#dcctl-presence-demote)	 - Return an event source's asserted devices to inferred presence

## dcctl presence demote {#dcctl-presence-demote}

Return an event source's asserted devices to inferred presence

### Synopsis

Release an event source's custody of every device it asserts presence for,
returning those devices to inferred presence.

Use this when a presence source has stopped running and its devices are frozen. A
device that was connected when the source went away reads connected forever — the
console, agents and rules all report a live device, and commands are dispatched into a
transport that drops them. A device that was disconnected has its commands held, and
those held commands count against the tenant's undelivered ceiling, so one wedged
device can block enqueues for healthy ones.

Demoting asserts NOTHING about connectivity. It does not mark anything offline: it
changes who decides. A stale-online device becomes eligible for the inactivity sweep
again and is judged on its own traffic; a wedged-offline device is reconnected by its
next event.

THE BLAST RADIUS IS AN ENTIRE EVENT SOURCE unless you narrow it with --device, so
--source is required and is never guessed. Pass it exactly as a device's reported
source: for MQTT and HTTP that is the event source's own configured id ("mqtt1",
"http1"), for Sparkplug "sparkplug:\<hostId>", for LwM2M "lwm2m". A source nobody uses
is not an error — it simply matches nothing — so a misspelling looks exactly like a
finished run, and this command says so when the first page matches no rows.

The server answers one page at a time; this command walks the whole source and reports
the totals. It is safe to re-run: an interrupted walk resumes by running it again, and
a device that is already inferred is not matched a second time.

Start with --dry-run, which writes nothing and lists the devices in scope.

```
dcctl presence demote [flags]
```

### Options

```
      --device stringArray   limit the run to this device token, within the source; repeat for more (default: the whole source)
      --dry-run              list the devices in scope and write nothing
  -h, --help                 help for demote
      --page int             devices per server round-trip, 1-1000 (default 200)
      --reason string        why this source is being released; recorded on every event written (required)
      --source string        event source whose devices are released, exactly as they report it (required)
  -y, --yes                  assume yes for prompts
```

### Options inherited from parent commands

```
      --email string      identity to authenticate as; it must be a member of the tenant (required)
      --password string   password for the identity (required)
  -s, --server string     instance host for platform API calls (default "localhost")
  -t, --tenant string     tenant whose devices are affected (required)
      --tls               use https for platform endpoints
```

### SEE ALSO

* [dcctl presence](#dcctl-presence)	 - Inspect and repair device presence

## dcctl resgen {#dcctl-resgen}

Generate configuration resources

### Synopsis

Generates configuration resources directly from the microservice codebase.

Writes the default instance configuration as a YAML file under ./resources (created in
the current directory if it does not exist), named \<group>_\<name>.yaml. The files are
rendered from the same typed configuration the services validate at startup, so they
are a starting point for an instance declaration that is known to parse.

```
dcctl resgen [flags]
```

### Options

```
  -h, --help   help for resgen
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI

## dcctl secrets {#dcctl-secrets}

Inspect and verify instance secret material

### Options

```
  -h, --help   help for secrets
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI
* [dcctl secrets escrow](#dcctl-secrets-escrow)	 - Work with root-key escrow artifacts

## dcctl secrets escrow {#dcctl-secrets-escrow}

Work with root-key escrow artifacts

### Synopsis

The instance secret-store root key wraps every stored secret. Its only copy inside the
cluster lives in etcd, which no DeviceChain backup contains — so `dcctl bootstrap` writes an
encrypted escrow artifact, and these commands are how you check that artifact is the right
one BEFORE the day you need it.

### Options

```
  -h, --help   help for escrow
```

### SEE ALSO

* [dcctl secrets](#dcctl-secrets)	 - Inspect and verify instance secret material
* [dcctl secrets escrow show](#dcctl-secrets-escrow-show)	 - Print what an escrow artifact says about itself (no passphrase needed)
* [dcctl secrets escrow verify](#dcctl-secrets-escrow-verify)	 - Check that an escrow artifact holds the root key a live instance is running

## dcctl secrets escrow show {#dcctl-secrets-escrow-show}

Print what an escrow artifact says about itself (no passphrase needed)

### Synopsis

Print the cleartext header of a root-key escrow artifact: the instance it belongs to, when it
was created, its cipher and key-derivation parameters, and the digest of the root key it holds.

Everything printed is cleartext and needs no passphrase; nothing is decrypted. The header is
bound to the encrypted key, so an edit to it fails at restore, but show does not check that. Use it to tell which of several files belongs to which instance. To check an
artifact against a live instance, use "dcctl secrets escrow verify".

```
dcctl secrets escrow show <file> [flags]
```

### Options

```
  -h, --help   help for show
```

### SEE ALSO

* [dcctl secrets escrow](#dcctl-secrets-escrow)	 - Work with root-key escrow artifacts

## dcctl secrets escrow verify {#dcctl-secrets-escrow-verify}

Check that an escrow artifact holds the root key a live instance is running

### Synopsis

Check that a root-key escrow artifact holds the root key a live instance is running.

The check compares key digests: the artifact's against the digest of the key in the instance's
configuration Secret (read through --kube-context, or the current context). It needs no
passphrase and decrypts nothing, so it is safe to run as often as you like, from a scheduled
job or a CI gate.

A mismatch means the artifact is intact but belongs to a different key (usually the instance
was re-bootstrapped after the file was written, or the file belongs to another instance), so
the instance has no usable escrow. The command exits non-zero in that case.

```
  dcctl secrets escrow verify --instance prod ./prod-rootkey.escrow
```

```
dcctl secrets escrow verify <file> [flags]
```

### Options

```
  -h, --help                  help for verify
      --instance string       instance id to check the artifact against (required)
      --kube-context string   kube-context to target (default: current)
```

### SEE ALSO

* [dcctl secrets escrow](#dcctl-secrets-escrow)	 - Work with root-key escrow artifacts

## dcctl sim {#dcctl-sim}

Create and drive DeviceChain simulations

### Synopsis

Create and drive standalone DeviceChain simulations.

'sim create' mints a scoped per-sim identity + tenant on the instance and writes a
handshake file the dc-simulator process reads to come up. 'sim start/stop/status'
drive an already-running sim through its control API; 'sim destroy' tears the
scoped identity and the tenant back down.

The superuser password comes from --admin-password, else $DC_ADMIN_PASSWORD, else the
instance's own Secret on the cluster (read through --kube-context, or the context the
instance was bootstrapped on).

### Options

```
      --admin-email string      superuser identity that mints the scoped sim identity (default "superuser@devicechain.local")
      --admin-password string   superuser password (default: $DC_ADMIN_PASSWORD, else read from the instance's Secret on the cluster)
      --control-addr string     the dc-simulator process's control API address (default "http://localhost:8090")
  -h, --help                    help for sim
  -i, --instance string         instance id (the literal event-ingress path segment; must match the deployed instance.id) (default "devicechain")
      --kube-context string     kube-context to read the superuser password from when none is given (default: the one the instance was bootstrapped on)
  -s, --server string           instance host for platform API calls (default "localhost")
      --tls                     use https/wss for platform endpoints
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI
* [dcctl sim create](#dcctl-sim-create)	 - Mint a scoped identity + tenant for a sim and write its handshake
* [dcctl sim destroy](#dcctl-sim-destroy)	 - Delete a sim's scoped identity and its tenant
* [dcctl sim start](#dcctl-sim-start)	 - Start (resume) a stopped sim's emit loop
* [dcctl sim status](#dcctl-sim-status)	 - Show a running sim's lifecycle state
* [dcctl sim stop](#dcctl-sim-stop)	 - Stop a running sim's emit loop (tenant + data are kept)

## dcctl sim create {#dcctl-sim-create}

Mint a scoped identity + tenant for a sim and write its handshake

### Synopsis

Mint a scoped per-sim identity and tenant on the instance, then write the
handshake file the dc-simulator actor reads to come up.

dcctl is the ONLY caller of the instance admin surface: it logs in as the superuser,
creates the sim's tenant, creates an identity with NO system roles, and binds it to
that tenant with the tenant-admin role (full authority scoped to that one tenant).
The sim actor authenticates as that identity and never touches the admin surface.

Idempotent: re-running create against an existing sim reconciles it (and keeps the
stored password in sync with the identity).

```
dcctl sim create <name> [flags]
```

### Options

```
  -h, --help                 help for create
      --ingress string       device-plane HTTP ingress base URL (default http(s)://<server>:8081)
      --manifest string      built-in scenario to run (devicepulse, buildingpulse, widgetlab, sitepulse, fleetpulse) (default "devicepulse")
      --mqtt-broker string   NATS MQTT gateway a scenario's command far end dials (default ssl://<server>:1883)
      --mqtt-insecure        skip verification of the MQTT gateway's certificate (defaults ON without --tls, since a local bring-up's gateway cert is self-signed; pass --mqtt-insecure=false to force verification)
      --seed int             deterministic generation seed for the sim's populations (default 1)
      --shed-priority int    shed-priority override 1-100 (0 = inherit the tier's); a load-test lever to place a probe tenant in a shed band
      --tier string          tenant tier to package the sim at (default "silver")
```

### Options inherited from parent commands

```
      --admin-email string      superuser identity that mints the scoped sim identity (default "superuser@devicechain.local")
      --admin-password string   superuser password (default: $DC_ADMIN_PASSWORD, else read from the instance's Secret on the cluster)
      --control-addr string     the dc-simulator process's control API address (default "http://localhost:8090")
  -i, --instance string         instance id (the literal event-ingress path segment; must match the deployed instance.id) (default "devicechain")
      --kube-context string     kube-context to read the superuser password from when none is given (default: the one the instance was bootstrapped on)
  -s, --server string           instance host for platform API calls (default "localhost")
      --tls                     use https/wss for platform endpoints
```

### SEE ALSO

* [dcctl sim](#dcctl-sim)	 - Create and drive DeviceChain simulations

## dcctl sim destroy {#dcctl-sim-destroy}

Delete a sim's scoped identity and its tenant

### Synopsis

Tear a sim back down: delete its scoped identity (with its membership), then its
tenant, then the local sim record.

The tenant goes with the sim, unconditionally. A sim tenant exists only to hold that
sim's entities, and the identity 'sim create' minted is the only member it is ever
given — so a kept tenant is one that appears in nobody's tenant menu, reachable only
by a superuser breaking glass into it through the API. (This used to be opt-in behind
--purge, whose default claimed to keep the tenant "for inspection" while deleting the
membership that made inspection routine.)

Teardown order is enforced: the identity goes first, because the server refuses a
tenant delete while any membership still references it.

🔴 THE NAME STAYS TAKEN UNTIL THE PURGE FINISHES. 'dcctl sim create \<name>' refuses a
name whose tenant is still being deleted, and that refusal is the point: the tenant
token is the key every functional area stores its rows under, so a sim recreated at a
name whose data is still there would attach to the previous run's devices, dashboards
and telemetry instead of starting clean. Once every area reports its rows reclaimed the
name is free again.

What this does NOT delete, yet: those rows in the other functional areas. Nothing
cascades a tenant delete to them today, so they are kept (telemetry too, unless the
instance opted in to a retention policy) until the tenant purge reclaims them — which
means, until that sweep exists, the name does not come back.

Human access ends at once: no membership remains and no token can be minted. The DEVICE
plane is cut too, but it drains rather than stopping dead, and the difference matters if
you are watching a dashboard. New connects, ingest and command dispatch are refused
within about a minute (the refusal is a cached read, refreshed on that interval). A
session already established keeps its broker-issued credential until it expires — up to
12 hours by default — though its ingest is refused at admission regardless. And a device
still holding a lifetime or a subscription drops off on its own schedule, so presence can
read connected for a while after the data stops.

```
dcctl sim destroy <name> [flags]
```

### Options

```
  -h, --help   help for destroy
```

### Options inherited from parent commands

```
      --admin-email string      superuser identity that mints the scoped sim identity (default "superuser@devicechain.local")
      --admin-password string   superuser password (default: $DC_ADMIN_PASSWORD, else read from the instance's Secret on the cluster)
      --control-addr string     the dc-simulator process's control API address (default "http://localhost:8090")
  -i, --instance string         instance id (the literal event-ingress path segment; must match the deployed instance.id) (default "devicechain")
      --kube-context string     kube-context to read the superuser password from when none is given (default: the one the instance was bootstrapped on)
  -s, --server string           instance host for platform API calls (default "localhost")
      --tls                     use https/wss for platform endpoints
```

### SEE ALSO

* [dcctl sim](#dcctl-sim)	 - Create and drive DeviceChain simulations

## dcctl sim start {#dcctl-sim-start}

Start (resume) a stopped sim's emit loop

```
dcctl sim start <name> [flags]
```

### Options

```
  -h, --help   help for start
```

### Options inherited from parent commands

```
      --admin-email string      superuser identity that mints the scoped sim identity (default "superuser@devicechain.local")
      --admin-password string   superuser password (default: $DC_ADMIN_PASSWORD, else read from the instance's Secret on the cluster)
      --control-addr string     the dc-simulator process's control API address (default "http://localhost:8090")
  -i, --instance string         instance id (the literal event-ingress path segment; must match the deployed instance.id) (default "devicechain")
      --kube-context string     kube-context to read the superuser password from when none is given (default: the one the instance was bootstrapped on)
  -s, --server string           instance host for platform API calls (default "localhost")
      --tls                     use https/wss for platform endpoints
```

### SEE ALSO

* [dcctl sim](#dcctl-sim)	 - Create and drive DeviceChain simulations

## dcctl sim status {#dcctl-sim-status}

Show a running sim's lifecycle state

```
dcctl sim status <name> [flags]
```

### Options

```
  -h, --help   help for status
```

### Options inherited from parent commands

```
      --admin-email string      superuser identity that mints the scoped sim identity (default "superuser@devicechain.local")
      --admin-password string   superuser password (default: $DC_ADMIN_PASSWORD, else read from the instance's Secret on the cluster)
      --control-addr string     the dc-simulator process's control API address (default "http://localhost:8090")
  -i, --instance string         instance id (the literal event-ingress path segment; must match the deployed instance.id) (default "devicechain")
      --kube-context string     kube-context to read the superuser password from when none is given (default: the one the instance was bootstrapped on)
  -s, --server string           instance host for platform API calls (default "localhost")
      --tls                     use https/wss for platform endpoints
```

### SEE ALSO

* [dcctl sim](#dcctl-sim)	 - Create and drive DeviceChain simulations

## dcctl sim stop {#dcctl-sim-stop}

Stop a running sim's emit loop (tenant + data are kept)

```
dcctl sim stop <name> [flags]
```

### Options

```
  -h, --help   help for stop
```

### Options inherited from parent commands

```
      --admin-email string      superuser identity that mints the scoped sim identity (default "superuser@devicechain.local")
      --admin-password string   superuser password (default: $DC_ADMIN_PASSWORD, else read from the instance's Secret on the cluster)
      --control-addr string     the dc-simulator process's control API address (default "http://localhost:8090")
  -i, --instance string         instance id (the literal event-ingress path segment; must match the deployed instance.id) (default "devicechain")
      --kube-context string     kube-context to read the superuser password from when none is given (default: the one the instance was bootstrapped on)
  -s, --server string           instance host for platform API calls (default "localhost")
      --tls                     use https/wss for platform endpoints
```

### SEE ALSO

* [dcctl sim](#dcctl-sim)	 - Create and drive DeviceChain simulations

## dcctl upgrade {#dcctl-upgrade}

Move a live instance onto a released version

### Synopsis

Upgrades an existing instance: its message broker and event store, applied
from this release's OpenTofu configuration; the configuration document its
services read; and the Helm release that runs them.

A DeviceChain release is one version across the service images, the Helm chart,
the operator and dcctl — but this command moves ONE INSTANCE, not the cluster.
The operator and its CRDs are cluster-scoped: there is one copy, shared by every
instance on the cluster. Moving them is 'dcctl install', so that a cluster-wide
change happens when somebody asks for one, rather than as a side effect of
upgrading whichever instance came first.

So an upgrade is two commands, in this order:

```
  dcctl install <provider> --version <tag>     # once, moves the cluster
  dcctl upgrade <provider> <instance> --version <tag>   # per instance
```

This command checks the cluster's operator and never moves it. It REFUSES when the
cluster has no operator, or has one identifiably from another release, naming the
install command that fixes it. An operator installed by hand carries no record of
which release put it there, and dcctl cannot tell that apart from one an older
dcctl overwrote — so that case is allowed through with a note rather than refused.

It mints no credentials. Every credential the instance is running on is read back
and kept: the database passwords, the broker's authority and logins, the
cross-service secret and the secret-store root key. A version change here cannot
become a credential change.

It does not change an instance's shape. The profile, topology and functional
areas come from the instance's own declaration, which is what 'dcctl bootstrap'
recorded. Use it to move versions, not to reconfigure.

The broker and event store are applied from this machine's OpenTofu state for the
instance, in ~/.devicechain/instances/\<instance>/, so the upgrade needs 'tofu' on
PATH and runs where the instance was bootstrapped. It plans first, prints what
changes, and REFUSES — changing nothing — when this machine has no state for the
instance, when the plan would delete or replace anything the configuration
manages, or when it would shorten the event store's recovery window or drop an
analytics reader nobody declared. Volume sizes are kept. NATS servers restart one
at a time under --ha; without it the single server restarts and the broker is
unavailable meanwhile. The upgrade waits for both to be healthy before it moves
the services. Values you set on the instance's OpenTofu configuration are kept
when they are in a terraform.tfvars beside its state
(~/.devicechain/instances/\<instance>/infra/instance/), and not otherwise.
--skip-infrastructure moves only the services, and says what it left.

```
  # Move an instance onto a published release
  dcctl upgrade local devicechain --version v1.3.0

  # Point at images you built yourself
  dcctl upgrade local devicechain --registry localhost:5000 --version my-build

  # See what it would do first, including the broker and event store plan
  dcctl upgrade local devicechain --version v1.3.0 --dry-run

  # Move only the services, leaving the broker and event store as they are
  dcctl upgrade local devicechain --version v1.3.0 --skip-infrastructure
```

```
dcctl upgrade <provider> <instance> [flags]
```

### Options

```
      --dry-run                         print what would happen without changing anything
      --escrow-file string              where this instance's root-key escrow artifact lives (default ~/.devicechain/escrow/<instance>-rootkey.escrow). The upgrade checks it still protects the key the instance is running on
      --escrow-passphrase-file string   read the escrow passphrase from this file (or $DCCTL_ESCROW_PASSPHRASE). Only needed to WRITE an escrow for an instance that has none — checking an existing one needs no passphrase
  -h, --help                            help for upgrade
      --kube-context string             kube-context to target (default: the cluster recorded at bootstrap)
      --registry string                 image registry to pull from (default: ghcr.io/devicechain-io)
      --skip-infrastructure             move only the services: leave this instance's message broker and event store on the configuration they were built with
      --version string                  release version to upgrade to (default: this dcctl's pinned version)
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI

## dcctl version {#dcctl-version}

Get version info

### Synopsis

Gets build information for the CLI: version, source commit, build date,
and the container image tag this binary deploys by default.

A dcctl built outside the makefile carries no build stamps; the fields it cannot
know are reported as unknown rather than guessed.

```
dcctl version [flags]
```

### Options

```
  -h, --help    help for version
      --short   print just the version string (for scripts)
```

### SEE ALSO

* [dcctl](#dcctl)	 - DeviceChain CLI
