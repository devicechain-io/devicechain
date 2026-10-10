---
sidebar_position: 1
title: Bootstrap an Instance
---

# Bootstrap an Instance

You build a DeviceChain deployment with two commands. `dcctl install` prepares a cluster
once. `dcctl bootstrap` then stands up a complete DeviceChain instance on it — its
infrastructure and all the service workloads — as many times as you want instances:

```bash
dcctl install local
dcctl bootstrap local my-instance
```

`dcctl` carries its own content. The OpenTofu infrastructure config, the Helm chart and the
operator manifests are all embedded in it, so you never need a checkout of the source tree
or `git` to deploy.

It does not carry its own tools. `install` and `bootstrap` drive `docker`, `kubectl`, `helm`
and `tofu` (or `terraform`) as binaries on your `PATH`, plus `kind` on the `local` provider.
Both commands check for all of them before they start and stop if one is missing, so install
them first. The full list is under [Prerequisites](#prerequisites).

:::note Status
DeviceChain is pre-release. `dcctl install local` and `dcctl bootstrap local` are implemented
and validated end-to-end on local Kubernetes (kind). `dcctl install local` creates the kind
cluster for you if none exists, and asks first unless you pass `--yes`. The `gcp` provider is
a planned follow-up.
:::

## Install the cluster {#install}

`dcctl install <provider>` prepares a cluster to hold DeviceChain instances. It installs the
prerequisites that every instance on the cluster shares.

In the `dc-k8s-system` namespace, it installs the **DeviceChain operator** and the two custom
resource definitions it reconciles, `Instance` and `InstanceConfiguration`. These are what
make a cluster able to hold an instance at all: `dcctl bootstrap` declares an `Instance`, and
it cannot declare one on a cluster where the definition does not exist. See
[the operator](./kubernetes-operator.md).

In the `dc-system` namespace, it installs:

- the relational database (`dc-rdb`), which holds one database per instance;
- the object store that database backups are archived to.

Each in a namespace of its own, it installs:

- the CloudNativePG operator (`cnpg-system`);
- cert-manager (`cert-manager`);
- monitoring (Prometheus and Grafana, in `monitoring` — see [Observability](./observability.md));
- the ingress controller (`ingress-nginx`).

The operator is one controller per cluster, shared by every instance on it. That is why
installing it is the cluster command's job rather than each instance's, and why moving a
cluster to a new release starts here: see
[Releases & upgrades](./releases-and-upgrades.md#zero-downtime-upgrades).

`install` also creates the base database identity that each instance's own database login is
created with, and records the install in the cluster.

Which cluster it installs into:

- **`local`** — `--cluster <name>` (default `devicechain`) names a kind cluster. If none of
  that name exists, `install` creates it, asking first unless you pass `--yes`. If one does,
  it is reused.
- **Any provider** — `--kube-context <ctx>` installs into a cluster that already exists.
  `dcctl` never creates or deletes a cluster reached this way.

### Re-running install {#re-running-install}

Re-running `install` against the same cluster converges it: what is already in place is left
as it is, and what is missing is added. That includes the backup store's volume, which keeps the
size it has; see [Backup store size](#backup-store-size).

**Changing its settings** — `--ha`, `--compact`, monitoring, backups,
`--backup-snapshot-class`, database placement — is refused while any instance exists on the cluster. Every instance was built to the settings in place when it was
bootstrapped, and none is rebuilt when they change. That includes the off-site archive: a
`--backup-credentials-file` naming a different endpoint or event-store bucket is refused too,
because every instance's event store keeps archiving to the one it was built with.

There is one exception: a re-run may raise `--max-connections` while instances run (see
[the connection budget](#connection-budget)). Lowering it is refused like any other change. A
re-run that does not pass `--max-connections` keeps the budget the cluster already has.

**A run that failed partway through is repaired the same way.** Once the cause is fixed, run
the same `dcctl install` again. If the backup object store could not start the first time,
for example because its image could not be pulled, the re-run deletes it, creates it again
and waits for it to become ready, exactly as the first run did. If the cause is still there,
the re-run fails the same way. Before it reports the cluster installed, `install` also checks
that the backup object store has finished rolling out, so a store left unready by an earlier
failed change is reported instead of being passed over.

`install` also waits, for up to 15 minutes, until every instance of the relational database has
joined, and exits with an error naming the database and how many of its instances are ready if one
has not. The install is already recorded by then and the cluster lock is released, so instances can
be bootstrapped while it is still waiting. To follow the database, watch it with `kubectl`; running
the same `dcctl install` again also keeps waiting, but it re-applies the prerequisites and refuses
bootstraps while it runs.

### Where install keeps its state {#install-state}

The prerequisites are applied with OpenTofu, and their state lives on the machine that ran
`dcctl install`, under `~/.devicechain/clusters/<cluster-uid>/infra`.

The directory is keyed on the cluster's identity — the UID of its `kube-system` namespace —
rather than on its name. A kind cluster that is deleted and recreated wears the same context
name while holding none of the resources the old state describes. `cluster.json`, beside
`infra/`, records the cluster name and kube-context you know it by, so you can match a
directory to its cluster:

```bash
cat ~/.devicechain/clusters/*/cluster.json
kubectl --context <kube-context> get namespace kube-system -o jsonpath='{.metadata.uid}'
```

The OpenTofu providers are pinned to exact versions, and every run, `dcctl destroy` included,
moves the directory's `.terraform.lock.hcl` onto the versions this dcctl pins. Each run
therefore asks the provider registry which versions exist, so the registry, or a provider
mirror you have configured, must be reachable.

A re-run works from that state, so **an installed cluster can only be re-installed from the
machine that holds its directory**. Run `dcctl install` against it from another machine and
it is refused before anything is applied:

```text
cluster ... is already installed (by dcctl <version>, <time>), but this machine holds no state
for it under ~/.devicechain/clusters/<cluster-uid>. It was installed from another machine, and
re-applying from empty state would try to create every prerequisite again. Run `dcctl install`
from the machine that installed it
```

Applying from empty state would plan every prerequisite as new and fail part-way on names
already in use, so the refusal is the safe outcome. But it makes that directory a
precondition of every re-run on this page, raising `--max-connections` included.

The directory is the only copy of the cluster's prerequisite state, and no DeviceChain backup
contains it. Keep it with the machine you install from. If another machine is to take over,
copy the whole `~/.devicechain/clusters/<cluster-uid>/` directory to it first. It holds the
cluster root's state, credentials included, so treat it as you treat the escrow directory.

### TLS, flags and a laptop install {#install-tls-and-flags}

`--no-tls` on `dcctl install` only means something together with `--compact`, where it drops
cert-manager. Without `--compact` it is refused. To serve one instance over plain HTTP, pass
`--no-tls` to `dcctl bootstrap` instead.

The flags are listed under [Install flags](#install-flags). On a laptop:

```bash
dcctl install local --dev
dcctl bootstrap local devicechain --dev
```

`dcctl bootstrap` refuses on a cluster where `dcctl install` has not completed, and the
refusal names the install command to run. `dcctl upgrade` refuses in the same case.

### Removing a cluster {#removing-a-cluster}

There is no command that uninstalls the prerequisites yet. [`dcctl destroy`](#destroy)
removes one instance and leaves them in place. To remove a local cluster that `dcctl install`
created, delete it with kind:

```bash
kind delete cluster --name devicechain
docker rm -f kind-registry   # the local image registry, if you used --build
```

kind knows nothing about `~/.devicechain/clusters/`, so deleting the cluster this way leaves
its directory behind. The next `dcctl destroy` of an instance that was on that cluster finds
the cluster gone and removes the directory along with the instance's own local state — see
[Removing an instance](#destroy). Or remove it by hand, once its `cluster.json` has confirmed
which cluster it belonged to.

On a cluster you keep, such as a managed cluster or any cluster that `dcctl install` did not
create, the prerequisites stay after the last instance is destroyed. Removing them by hand is not
supported: they include the shared relational database and its backups, and the in-cluster
backup object store if you use it, along with any instance backups a destroy kept or could not
remove (see
[What happens to the instance's backups](#destroy-backups)). Keep the cluster's directory under
`~/.devicechain/clusters/<cluster-uid>/` for as long as they are there, because every later
`dcctl install` on the cluster works from it (see
[Where install keeps its state](#install-state)).

### The connection budget {#connection-budget}

The relational database has a fixed number of connections, set by `--max-connections`
(default `600`). Each instance reserves a connection limit on its database login, sized from
the areas it enables. `dcctl bootstrap` refuses an instance whose reservation does not fit in
what is left.

That check runs **before anything of the instance is written** — no namespace, database or
login — so a refused bootstrap leaves nothing to clean up. A cluster meant to hold many
instances, or instances with many areas enabled, needs a larger budget.

Each service keeps every connection its pool has opened, up to the pool size (20 unless
`maxOpenConnections` is set), open between uses, and closes each one an hour after it was
opened. The reservation allows each area one pod with a full pool of the default size, plus one
more pod during a rollout, so at the defaults every pool fits within it. A service run at `replicas`
above 1, or with a raised `maxOpenConnections`, can hold more than that after a busy period, so
check those settings against the instance's connection limit.

The budget is the one install setting that can change under running instances, and only
upwards: re-run `dcctl install` with a larger `--max-connections`. It is not free. Changing
the database's connection limit restarts its database instances one at a time. On a cluster
installed without `--ha` there is only one, so **every instance on the cluster briefly loses
its database** while it restarts. Do it in a quiet window.

`dcctl upgrade` is admitted against the same budget. A release can change what an instance's
relational areas need, so the upgrade compares this release's need with the limit the
instance's login already holds, before it writes anything. A need that has not changed is not
re-admitted. A grow the budget cannot admit is refused with nothing moved:

```text
this release needs instance "my-instance"'s database login to hold <n> connections, up from <m>,
and nothing has been changed: ... Destroy an instance, or raise the budget by re-running
`dcctl install` with a larger --max-connections
```

The remedy is the one above, and it restarts the database. On a cluster whose budget is
nearly spent, raise it in a quiet window *before* the upgrade window rather than discovering
the need inside it.

A grow that fits is applied before the services roll. For a release that needs *less*, the
login is trimmed only after every service is ready on the new release. A trim that fails does
not fail the upgrade, since the services are already running. It is printed as a warning
ending in ``re-run `dcctl upgrade` to finish it``, and until you do, the login holds more of
the budget than it needs. `dcctl upgrade --dry-run` reports the check it would make without
signing in to the store.

## What bootstrap does {#what-it-does}

The bootstrap runs as an ordered pipeline that builds an instance, and tells you which step
failed if one does.

It is a create verb. Every credential the instance has is minted here — the database
passwords, the broker's authority and logins, the cross-service secret, the secret-store root
key — because none of them exists yet. Point it at an instance that is already running and it
stops at step 3, before the infrastructure or the chart are touched. It names the command
that does move a live instance: `dcctl upgrade`, covered in
[Releases & Upgrades](./releases-and-upgrades.md#zero-downtime-upgrades).

A run that *failed* partway through is a different case, and re-running it is still how you
repair it. Step 3 refuses only a **live** instance: one whose configuration document (written
in step 8) exists **and** whose first bootstrap has finished. When step 5 declares the
instance, it records on the declaration that its first bootstrap has not finished, and only a
run that ends successfully removes that record. Until then, a failure at any step, including
inside the Helm install or while waiting for readiness, leaves a half-built instance. Run the
same `dcctl bootstrap` command again, from the same machine, to finish it.

An instance whose first bootstrap was started by an earlier `dcctl` release carries no such
record. If that bootstrap failed after step 8 began, step 3 cannot tell the instance from a
running one and refuses. The refusal names the two ways on: `dcctl upgrade`, which runs the
same Helm install and readiness wait over the instance, or `dcctl destroy` followed by a fresh
bootstrap.

:::warning One-time exception: instances created before the database moved to CloudNativePG
The relational database changed from a StatefulSet to a CloudNativePG cluster, and there is
no in-place upgrade. On an instance created before that change, the bootstrap **refuses** and
tells you how to dump the data or discard it deliberately. See
[the legacy database exception](#legacy-db-removal).
:::

### The legacy database exception {#legacy-db-removal}

A StatefulSet's data directory cannot be adopted by the operator, which is why there is no
in-place upgrade. The refusal is the point: without it, the old database would be removed and
a new, empty one would take over the same hostname, leaving an instance that looks perfectly
healthy and has no data in it.

This is the one documented reason to run `dcctl bootstrap` against an instance that is
already live, so `--allow-legacy-db-removal` is carved out of the refusal in step 3 as well
as this one. Nothing else is.

The flag is split along the same line as the databases. The relational database belongs to
the cluster, so `dcctl install --allow-legacy-db-removal` covers it. The event store belongs
to the instance, so `dcctl bootstrap --allow-legacy-db-removal` covers that.

### Several instances on one cluster {#several-instances}

A cluster can hold more than one DeviceChain instance. Each instance's services, its broker
(NATS), its event store (TimescaleDB) and its credentials live in a namespace of their own,
named for the instance behind a `dci-` prefix: instance `devicechain` runs in namespace
`dci-devicechain`.

The prefix keeps an instance's namespace from ever colliding with one the cluster itself
uses. `monitoring`, `cert-manager`, `ingress-nginx` and the rest are out of reach by
construction, so no instance id can take one of them.

Each instance connects to the shared relational database with a login of its own that owns
exactly one database, so no instance can reach another's data.

What instances share are the cluster's prerequisites: the DeviceChain operator and its custom
resource definitions, the ingress controller, cert-manager, the CloudNativePG operator,
monitoring, the relational database and the backup object store.
[`dcctl install`](#install) installs them once. Every bootstrap reuses them, and follows the
settings the cluster was installed with — high availability, compact sizing, monitoring and
backups.

Two things on a cluster can belong to only one instance, and the bootstrap handles both:

- **The ingress host.** An ingress controller given two instances on one host serves only one
  of them, silently. A bootstrap whose host another instance already serves is refused. Give
  each instance its own `--host` (on a local cluster, for example `--host beta.localhost`).
- **The local MQTT port.** On a local cluster, port 1883 on your machine reaches the broker of
  the first instance only. Later instances' brokers are reachable from inside the cluster, and
  the bootstrap says so when it happens.

#### The instance namespace {#instance-namespace}

The bootstrap checks the instance's namespace at the same point. An instance owns
`dci-<id>`: dcctl writes its root key, its broker TLS keypair and every one of its database
credentials there, and `dcctl destroy` deletes the whole namespace. So a `dci-<id>` that
already exists and does not carry the label `devicechain.io/instance=<id>` is refused before
any of that is written.

Nothing but dcctl creates a namespace under `dci-`. The likely cause is an earlier
`dcctl destroy` of this same instance that did not finish. The refusal says so and names the
command to finish it, `dcctl destroy <provider> <id>`. A namespace still being deleted is
refused too, until it is gone.

If the namespace is one you made on purpose — to carry a quota, a policy or RBAC of your own
— hand it to the instance and run the bootstrap again:

```bash
kubectl label namespace dci-<id> devicechain.io/instance=<id>
```

#### Instance names {#instance-names}

Instance names are lowercase letters, digits and `-`, at most 50 characters. The name is the
instance's database and that database's login as written. It is also the tail of two other
names: the namespace is `dci-` plus the name, and the Helm release is `dc-` plus the name. The
50-character limit comes from that release name: Helm caps a release at 53 characters, so the
name itself has 50 to spend.

An instance built before instances had namespaces of their own runs its broker and event
store in the shared `dc-system` namespace, and they cannot be moved in place. The bootstrap
refuses such an instance and says to destroy it and bootstrap it again.

### The bootstrap steps {#bootstrap-steps}

These are the steps the run prints as it goes (`[8/10] Install instance (Helm)`), so a
failure names a step you can find here:

1. **Ensure local registry** — the developer `--build` path only: provision a local registry
   and build every image into it. On the published-image path it does nothing and says so. It
   goes first because the chart installed later names those images, and on the `--build` path
   this is the step that produces them.
2. **Claim the cluster** — create the operator's namespace and take the **cluster lock**,
   before anything is applied. While it is held, a second `dcctl bootstrap` against the same
   cluster is refused rather than quietly applying over this one. See
   [The Cluster Lock](./cluster-lock.md), which also covers what to do when the cluster turns
   out to be claimed by somebody else.
3. **Refuse a rebuild** — ask the cluster whether this instance is already live, and stop if
   it is. Its position is deliberate on both sides. It comes *after* the lock, because a
   concurrent bootstrap is exactly what would make the answer stale between reading it and
   acting on it. It comes *before* anything of the instance is applied, because every step
   below this one writes to a cluster that may already be running the instance it would be
   writing over. A dry run says what a real run would refuse rather than hiding it.
   It recognises a live instance by its configuration document, unless the instance's
   declaration records that its first bootstrap never finished. In that case it lets the run
   through to finish the instance. If it cannot read the declaration, it stops rather than
   guessing.
4. **Check what other instances hold** — ask the cluster which ingress host and local MQTT
   port other instances already hold, and stop if this instance's host is one of them. Also
   look at the namespace this instance is about to be built in, `dci-<id>`, and stop if it
   exists and is not this instance's, or is still being deleted. The step runs before anything
   is written, so a refusal leaves nothing behind. A local MQTT port another instance holds
   does not stop the run, and is reported. A dry run says what a real run would refuse. See
   [Several instances on one cluster](#several-instances), including the label that hands a
   namespace you created yourself to the instance.
5. **Declare the instance** — write the instance's **declaration** into the cluster: the
   provider and cluster it belongs to, the profile, the image version, and whether its
   databases are being recovered from an archive. It is then read back, and every step below
   works from what came back rather than from the flags that produced it. The cluster, not
   your laptop, is the record of what this instance is. See
   [the instance declaration](./kubernetes-operator.md#instance-declaration).
   When the run is building the instance (step 3 found no configuration document, or found
   that the first bootstrap never finished), the declaration also carries the annotation
   `core.devicechain.io/bootstrap-unfinished: "true"`. A run that ends successfully removes
   it, in the same write that records the instance as `Ready`.
6. **Render configuration** — resolve the instance id, namespace, profile, and every generated
   credential: the broker-auth material (the shared service password and the callout issuer
   key), the certificate authority that signs the broker's own TLS certificate, the
   cross-service auth secret, and the **secret-store root key**. All of them are minted here,
   because step 3 has established there is no live instance to take them from. Finishing a
   half-built instance is the exception: there the step reads back what an earlier run already
   put in the cluster rather than generating a second set. That includes an instance whose
   earlier run got as far as writing its configuration document. The broker's certificate
   authority is the one thing every run issues afresh.
   The step also records the broker's credentials on the machine you run it from, before the
   broker is configured with them. The broker is configured before the instance is, and its
   credentials cannot be recovered from the cluster once they are in it, so this is what lets
   you resume an interrupted run by running it again. The root key is additionally escrowed to
   an encrypted file you keep; see [Disaster Recovery](./disaster-recovery.md).
7. **Apply infrastructure** — `tofu apply` the embedded OpenTofu configuration via
   [terraform-exec](https://github.com/hashicorp/terraform-exec), for this instance only: its
   own broker (NATS) and event store (TimescaleDB) in its namespace, with state kept in
   `~/.devicechain/instances/<instance>/infra`. The step also creates the instance's own login
   and database on the shared relational database. The cluster's shared prerequisites are not
   applied here; [`dcctl install`](#install) put them in place. Subsequent runs are
   incremental.
8. **Install instance (Helm)** — write the instance's **configuration document**, the one
   every service reads its credentials and endpoints from. Then deploy the Helm chart via the
   Helm Go SDK, blocking until the workloads are ready. Step 3 looks for that document on any
   later run. The instance counts as live once a run has also ended successfully.
9. **Wait for readiness** — poll each enabled area's Deployment until it has finished rolling
   onto the configuration this run produced. This is an explicit confirmation gate rather
   than trusting the Helm step's own wait. Having replicas available is not enough: where pods
   are being replaced, that is already true of the ones on their way out. So the step also
   waits for the new template to be observed, for every replica to be recreated on it, and for
   no old replica to still be running. `dcctl upgrade` uses the same gate for the same reason. It then waits, for up to 15 minutes,
   until every instance of the instance's event store has joined, because the services are ready
   on the primary alone. If one has not, the step fails with an error naming the database and how
   many of its instances are ready; nothing is undone, and running the same `dcctl bootstrap`
   again finishes the instance.
10. **Report access info** — print the namespace, the superuser's email and where its password
    is kept (plus the password itself, once: on the run that generated it, or on the run that
    finishes a bootstrap that failed before showing it), and how to reach the instance.

:::tip `Ctrl+C` stops a run cleanly
An interrupted run stops the infrastructure tool gracefully — it finishes what it is doing and
writes its state — and hands the cluster lock back, so re-running is all you need. A second
`Ctrl+C` exits immediately and gives up both. See [Interrupting a run](./cluster-lock.md#interrupt).

If the run had already reached step 8, running it again still finishes it: step 3 lets a
re-run through until a bootstrap of the instance has ended successfully. A **second** `Ctrl+C`
during step 8 can leave the instance's Helm release marked as still in progress. The re-run
then stops at step 8 with `another operation (install/upgrade/rollback) is in progress`, and
dcctl does not clear that for you. Recover with `dcctl destroy`, then bootstrap again.
:::

Because the embedded artifacts are the *same* ones the platform ships, a bootstrapped instance
exercises the real deployment. It cannot drift from a production deploy.

### The default backup destination {#default-backup-destination}

Database backups need somewhere to go. By default, that is a single-replica **MinIO** in the
`dc-system` namespace, installed by `dcctl install`. A stock install then produces instances
whose write-ahead log is genuinely being archived, rather than instances carrying a backup
plugin with nowhere to put anything.

:::info The default backup destination is an AGPL component
MinIO is licensed AGPL-3.0. Community MinIO was archived in April 2026 and its own images are no
longer published, so DeviceChain runs a build of a maintained fork (`cgr.dev/chainguard/minio`),
pinned by digest, which your nodes pull from `cgr.dev`. A patched build reaches your cluster only
when a DeviceChain release moves that pin. To avoid both the licence and that dependency, point
backups at storage outside the cluster.
:::

Neither issue affects DeviceChain's own Apache-2.0 licensing. The image is referenced, never
built, modified or redistributed, and the platform reaches it over the S3 HTTP API. But the
component does run in *your* cluster, and many organisations do not permit AGPL software
regardless of how it is used.

Storage outside the cluster is the recommended production configuration anyway, for a reason
that has nothing to do with licensing: an in-cluster bucket shares the cluster's failure
domain, so it cannot be disaster recovery. Pass `--backup-credentials-file` to `dcctl install`
to name an object store you already own. See [Disaster Recovery](./disaster-recovery.md) and
the OpenTofu configuration's `backup_destination`.

#### Recovery windows {#backup-retention}

Each database keeps its own recovery window: the span of time it can be restored to any point
within. The relational database, which holds tenants, users, devices, rules, secrets and each
device's last-known state, keeps **30 days**; set it with `backup_retention_rdb` in the cluster's
OpenTofu configuration. Each instance's event store keeps **7 days**; set it with
`backup_retention_tsdb` in a `terraform.tfvars` beside the instance's OpenTofu state
(`~/.devicechain/instances/<instance>/infra/instance/`), where every `dcctl` apply of the
instance, an upgrade's included, reads it. The relational database gets the longer
window because an instance cannot be rebuilt without it, and the mistakes it is restored from, such
as a bad migration or a mistaken delete, are often found days later. Event history is bulk, has
its own [data lifecycle](../concepts/architecture.md), and its log is what fills the backup store.

A window is a whole number and a unit: `d` for days, `w` for weeks or `m` for **months** (not
minutes). Anything else is refused before the apply starts. An empty window keeps every backup.
Lengthening a window keeps more in the backup store: see below.

#### Backup store size {#backup-store-size}

The default store is 160 GiB, sized from the default event store so that, under sustained
ingest, the event store's volume fills first. While an instance is quiet, the write-ahead log it
archives costs almost nothing. Under sustained ingest the log grows with the write rate: measured
on Google Kubernetes Engine, up to about 1.9 KB per ingested event for both databases together,
against about 1 KB per event of stored data. (This page used to give about 1 KB per event for
the archive; that figure was low.) 160 GiB holds the archive of a full 32 GiB event store, plus
one full base backup of each database, with more than a third of the store still free. So the
backup store's own alerts stay quiet, and the event store's alerts name the cause.

That holds for one instance whose event store fills within about a day. It does not hold:

- **for several instances that ingest continuously.** The store belongs to the cluster, and each
  instance adds its own archive. Add about 160 GiB for each such instance, or send backups to an
  object store you run (`--backup-credentials-file`), which is the recommended production setup
  anyway.
- **when the event store takes more than about a day to fill.** The store keeps a full base
  backup of each database for every day of that database's [recovery window](#backup-retention),
  plus the log archived since. At the default windows that
  is 32 compressed copies of the relational database and 9 of the event store, and they must fit
  beside the log: 32 times the relational database's compressed size plus 9 times the event
  store's must stay under 160 GiB. The relational database is normally megabytes, so in practice
  the event store must compress to well under about 17 GiB.
- **when an instance's stored data is bounded by a retention window (`retentionDays`).** Its event
  store never fills, but it still sends its log here: a week of the event store's, and 30 days of
  the relational database's, whose log grows with ingest because it records each device's
  last-known state. In the one measurement taken, of a small fleet reporting fast, the relational
  database's backups were about 14% of the store's contents. At that share, about 100 events per
  second sustained fills the default store before any base backup is counted. The share is not measured
  for larger or slower fleets, where it is likely higher: if the relational database's log were
  all of it, the figure would be about 35 events per second.
- **when you grow the event store.** Each GiB added to the event-store volume needs about five
  GiB more here.

In those cases, the alerts described under
[Backups that stop shipping](./observability.md#backup-archiving) are the warning:
`BackupDestinationAlmostFull` at 85% full and `BackupDestinationFillingFast` on a steep rate, with
`PostgresWALArchivingFailing` once archiving has stopped. A base backup lands all at once, so a
store already past 85% can fill at the next nightly backup: act on the first alert. When the store
is full, archiving stops for every instance on the cluster, and each database keeps its unshipped
log on its own volume until that fills too and the database stops.

Each database compresses the log it archives with zstd. The 1.9 KB figure, and the 100 and 35
events per second worked out from it above, were measured before this release compressed the event
store's log, gave it time-led keys and archived with zstd, while the archive was compressed with
gzip, so they err large: in the release benchmark the event store's archive took about 0.47 KB per
stored event. The relational database's share was not measured again. On the same log, zstd's output was 2% to 16% smaller than
gzip's, so the switch does not make the archive larger. A segment closed early on a quiet
database is about 16 KiB, or about 32 KiB on a database that still archives with gzip.

The store's size is set when `dcctl install` first creates it. Re-running install, including as
the first step of an upgrade, keeps the size the store has, and so does a direct `tofu apply` of
the OpenTofu configuration. To grow it, on a StorageClass that allows volume expansion:

```bash
kubectl -n dc-system patch pvc dc-object-store-data \
  -p '{"spec":{"resources":{"requests":{"storage":"320Gi"}}}}'
```

On kind, the volume is not limited to its size: it uses what the host disk has.

Shortening a recovery window (`backup_retention_tsdb` for an instance's event store,
`backup_retention_rdb` for the relational database) is not a fix for ingest: it keeps less
history but still a full day of log, and it gives up recovery range to buy space. The alerts
described under [Backups that stop shipping](./observability.md#backup-archiving) warn before
the store or a database volume fills.

#### Volume-snapshot base backups {#snapshot-base-backups}

On a cluster whose storage driver takes CSI volume snapshots, `dcctl install
--backup-snapshot-class <class>` takes each database's daily base backup as a volume snapshot
of its disks instead of a full copy in the backup store. Nothing changes without the flag.

- **What stays the same.** Every database still archives its write-ahead log to the backup
  store, continuously. A full base backup still goes to the backup store once a week (Sunday
  04:00): the store prunes old log only against the base backups it holds, so without one it
  would keep every segment until it was full, and every restore reads the store.
- **The class.** It must exist, have `deletionPolicy: Delete`, and belong to the storage driver
  that provisions the database volumes (normally the default StorageClass's). `dcctl install`
  checks all three before it changes anything, and so does each `dcctl bootstrap`, so a class
  deleted since the install is caught before an instance is built. The cluster needs a CSI
  snapshot controller: Google Kubernetes Engine and Azure AKS include one with their disk
  drivers; on Amazon EKS, install the snapshot controller add-on first.
- **Retention.** CloudNativePG does not delete old snapshots. The DeviceChain operator does,
  every ten minutes: it keeps every snapshot inside the database's
  [recovery window](#backup-retention) and the newest one before it, and deletes the rest,
  which deletes the provider's copy too. `DatabaseSnapshotPruningStalled` fires when it stops.
  Each pass records its time on the schedule as the
  `devicechain.io/snapshot-retention-checked-at` annotation.
- **What it does not do.** No restore reads a snapshot. A restore (`--restore-rdb-from`,
  `--restore-tsdb-from`) reads the backup store: the newest weekly base backup and the log
  since, so it can replay up to a week of log. An instance's snapshots are deleted with its
  namespace when it is destroyed, and the relational database's when the `dc-system`
  namespace is deleted. Snapshots taken at your cloud provider outlive the cluster, and
  deleting the provider's copy can finish after the namespace is gone. If the cluster is
  deleted before that, they stay at the provider, holding the databases' contents,
  including data a tenant deletion has removed, until you delete them there; nothing
  prunes them any more. Before deleting the cluster, check that
  `kubectl get volumesnapshotcontent` lists none whose snapshot namespace (the
  `VOLUMESNAPSHOTNAMESPACE` column) is `dc-system` or one of your instances' namespaces;
  afterwards, check your provider's snapshot list. The
  [Google Kubernetes Engine guide](https://github.com/devicechain-io/devicechain/blob/main/deploy/gke/README.md#tearing-it-down)
  gives the commands for GKE.
- **What it does to the backup store.** The store now keeps up to a week more log for each
  database: log back to the newest weekly base backup before each window. It holds fewer full
  copies, but where log is most of what it holds, it fills sooner. With the default windows
  and store, and the relational share measured above, volume-snapshot base backups fill the
  store at about 60 events per second sustained, not about 100; and at about 29 if the
  relational database's log were all of it. The alerts above are the warning, as they are
  without snapshots. It does not change the store's default size: the sizing rule
  [above](#backup-store-size) counts one full base backup of each database and the log of every
  event a full event store holds, whatever the base-backup schedule, and a full base backup
  still lands in the store when each database is created and then weekly, so the rule gives the
  same 160 GiB with or without snapshots.
- **It belongs to the cluster.** Every instance follows it, and changing it is refused while
  instances run on the cluster, like the other [install settings](#re-running-install).

The alerts for snapshots are described under
[Backups that stop shipping](./observability.md#backup-archiving).

## Prerequisites {#prerequisites}

- **A Kubernetes cluster, version 1.29 or newer**, and a kube-context pointing at it. The
  floor comes from the CloudNativePG charts, which refuse to install below it.
  `dcctl preflight` checks it up front, because otherwise the failure lands part-way through a
  bootstrap that has already written your root-key escrow file. For the `local` provider this
  is a kind cluster, which `dcctl install local` creates for you (`--cluster <name>`, default
  `devicechain`). Pass `--kube-context <name>` to use a cluster you already have instead
  (kind / minikube / k3d / docker-desktop).
- **Disk for the persistent volumes**, on the cluster's default StorageClass. On a cluster that
  is not local (not kind, minikube, k3d, docker-desktop or rancher-desktop), with the default
  install settings (no `--compact`, `--no-cnpg`, `--no-monitoring` or
  `--backup-credentials-file`), `dcctl install --ha` claims 204 GiB for the cluster: three relational-database volumes, the
  [backup store](#backup-store-size) (160 GiB) and the monitoring stack's Prometheus. Each
  instance claims 144 GiB more, three event-store and three message-broker volumes: 348 GiB for
  a cluster with one instance. Without `--ha`, the cluster claims 188 GiB and each instance 48 GiB.
  Each further instance that ingests continuously also needs the backup store grown by about
  160 GiB, as [Backup store size](#backup-store-size) explains. On a cloud provider, check the
  disk quota first. A new Google Cloud project allows 500 GB of SSD per region, counting each GiB
  of volume as one GB, and both of Google Kubernetes Engine's disk classes count against it. The
  volumes of one instance are under that, and a default `--ha` install with one instance fits on
  the cluster the
  [Google Kubernetes Engine guide](https://github.com/devicechain-io/devicechain/blob/main/deploy/gke/README.md#before-you-start)
  creates because its nodes boot from standard disks, which count against a different quota.
  Balanced or SSD boot disks would count against the SSD quota too. The guide gives the quota to request for
  more instances. On a local cluster the monitoring stack keeps no volume, and on
  kind the sizes are not enforced.
- **CPU and memory for the requests.** Without `--compact`, the five services that handle every
  event request about 4 CPU between them, about 5 under `--ha`, where `event-management` runs two
  pods, and each NATS server (three under `--ha`) requests 500m
  of CPU and 768Mi of memory, on top of the other services, the databases and the cluster's
  own components. A pod that does not fit stays `Pending`, and the install waits out its timeout.
  See [Service sizing](#service-sizing) for each figure. On a cluster that cannot spare them, such
  as a laptop, use [`--compact`](#--compact).
- **OpenTofu 1.9 or later** (the `tofu` binary; `terraform` 1.9 or later also works) on your
  `PATH`. `dcctl` drives it to provision infrastructure. Install it from
  [opentofu.org](https://opentofu.org). Run `dcctl preflight local` to check that it is there,
  and the rest of your environment, up front. The preflight does not check its version; an older
  one fails when it loads the infrastructure configuration.
- **`docker`, `kubectl` and `helm`** on your `PATH`, and **`kind`** for the `local` provider.
  The preflight check fails if any of them is missing. Docker should be a native Docker engine
  rather than Docker Desktop, and its daemon must be reachable.
- **`ko`**, only to build images from source with `--build`. The preflight check warns rather
  than fails when it is missing.

## Image source {#image-source}

By default, bootstrap deploys the **published images** from `ghcr.io/devicechain-io`, with
nothing to build:

```bash
dcctl bootstrap local my-instance
```

If you work from a source checkout, you can build the images from source and deploy those
instead with `--build`. It builds each service and the operator with [`ko`](https://ko.build),
plus the web console with `docker build`, into a local registry, and deploys by reference:

```bash
# from a source checkout; requires Docker + ko
dcctl bootstrap local my-instance --build
```

The only difference between the two paths is the registry the pods pull from. The pipeline,
chart and operator are identical.

## Bootstrap flags {#useful-flags}

| Flag | Purpose |
|------|---------|
| `--cluster <name>` | `local` provider: the kind cluster to create the instance on (default `devicechain`). It must already have been [installed](#install); bootstrap never creates a cluster. |
| `--kube-context <name>` | Target an installed cluster through this kube-context instead. |
| `--profile <profile>` | Functional-area profile: `default` (the standard system, used when omitted), `full` (everything — adds AI inference, outbound connectors, MCP, Sparkplug B ingest, LwM2M ingest, and update management), `telemetry`, or `ingest-only`. |
| `--build` | Build images from source into a local registry (developer path; needs the source tree + Docker + ko). |
| `--registry` / `--version` | Override the image registry / tag (defaults: published `ghcr.io/devicechain-io`, or `localhost:5000` + `dev` with `--build`). |
| `--host <name>` | Ingress host to expose the instance on (default `devicechain.local`). Use `localhost` on a local cluster to reach the console with no `/etc/hosts` edit. |
| `--no-tls` | Serve plain HTTP instead of a self-signed cert. With `--host localhost`, a zero-config `http://localhost/` (no cert warning). On a cluster installed without cert-manager this is on by default, and `--no-tls=false` is refused: there is nothing to issue the certificate. |
| `--dry-run` | Print what each step would do without changing anything. A dry run takes no cluster lock; it does still report whether another operator is holding the cluster. |
| `--skip-preflight` | Skip the environment checks. |
| `--escrow-passphrase-file <path>` | Read the root-key escrow passphrase from a file instead of prompting. See below. |
| `--escrow-file <path>` | Write the escrow artifact somewhere other than `~/.devicechain/escrow/`. |
| `--no-escrow` | Do **not** escrow the root key. For throwaway instances only; implied by `--dev`. An instance created this way can be given an escrow later — see [the escrow reconcile](./disaster-recovery.md#escrow-reconcile). |
| `--restore-root-key <path>` | Disaster recovery: seed this instance's root key from an escrow artifact instead of minting one. Accepted only when there is something for that key to open — a database the relational store already holds for this instance (see [Recovering an instance](./disaster-recovery.md#recover)), or this instance half-built by an earlier run that is being finished. After `dcctl destroy` neither is true and the flag is **refused**: the next instance under that name mints a key of its own, so bootstrap without the flag, and move the old artifact aside first — bootstrap will not overwrite one. `dcctl secrets escrow show <path>` tells you which instance an artifact was written for. |

### The root-key escrow {#escrow}

Bootstrap writes an encrypted copy of the instance's **secret-store root key** to
`~/.devicechain/escrow/<instance>-rootkey.escrow`, sealed under a passphrase you choose. It
prompts for that passphrase, or takes it from `--escrow-passphrase-file` or
`DCCTL_ESCROW_PASSPHRASE`.

Escrow is on by default. A non-interactive run with no passphrase **fails** rather than
proceeding without one:

```bash
# automation
DCCTL_ESCROW_PASSPHRASE="$(pass show devicechain/prod-escrow)" \
  dcctl bootstrap local prod --yes

# a throwaway instance
dcctl bootstrap local scratch --dev
```

:::danger This file is not optional for anything you care about
The root key encrypts every secret the instance stores. It lives only in the cluster's etcd,
and **no DeviceChain backup contains etcd**. Without this file, a database backup restored to
a new cluster rehydrates secrets that nothing can decrypt. Read
[Disaster Recovery](./disaster-recovery.md) before you need it.
:::

The areas that store secrets refuse to start rather than serve credentials they cannot open.
The user-management service is among them: it seals the token-signing key, so no one can sign
in. So you find out immediately, and there is nothing to be done about it by then.
[Disaster Recovery](./disaster-recovery.md) explains the whole procedure.

An instance that has no escrow — one created with `--no-escrow`, or with `--dev` — can be
given one later without being rebuilt. `dcctl upgrade` writes the missing artifact when you
pass it a passphrase, and checks an existing one every time it runs. See
[the escrow reconcile](./disaster-recovery.md#escrow-reconcile).

## Install flags {#install-flags}

These are flags of [`dcctl install`](#install). They describe the cluster, and every instance
bootstrapped on it follows them. None of them is a `dcctl bootstrap` flag.

| Flag | Purpose |
|------|---------|
| `--cluster <name>` | `local` provider: the kind cluster to install into (default `devicechain`), created if it does not exist. |
| `--kube-context <name>` | Install into the existing cluster this kube-context points at. `dcctl` never creates or deletes it. |
| `--compact` | Small-footprint preset — see below. |
| `--ha` | High availability — see below. Needs at least **3 schedulable nodes**. |
| `--no-tls` | With `--compact`: install no cert-manager, and therefore no database backups. `--compact --no-tls=false` keeps both. Refused without `--compact` — use `dcctl bootstrap --no-tls` to serve an instance over plain HTTP. |
| `--no-monitoring` | Skip the monitoring stack (Prometheus and Grafana). |
| `--no-cnpg` | Skip the CloudNativePG operator and the database backup plugin. For a cluster that **already runs CloudNativePG**: Helm cannot adopt objects another installer created, so the install fails without this. |
| `--backup-credentials-file <path>` | Send database backups to an object store you already own, described by a JSON file, instead of the in-cluster one. See [Disaster Recovery](./disaster-recovery.md). |
| `--backup-snapshot-class <class>` | Take each database's daily base backup as a CSI volume snapshot with this VolumeSnapshotClass instead of a full copy in the backup store; a full copy still goes to the store weekly, and restores still read the store. The class must exist, use `deletionPolicy: Delete`, and belong to the driver that provisions the database volumes. Refused with `--no-cnpg` or `--compact --no-tls`, which leave no backups. See [Volume-snapshot base backups](#snapshot-base-backups). |
| `--database-node-selector <key>=<value>` | Run the databases only on nodes with this label (repeatable). Applies to the shared relational store and to the event store of every instance bootstrapped on the cluster. See [Database placement](#database-placement). |
| `--database-toleration <key>[=<value>][:<effect>]` | Let the databases run on nodes with this taint, written as `kubectl taint` writes it (repeatable). Needs `--database-node-selector`. |
| `--restore-rdb-from <archive>` | Disaster recovery: recover the shared relational store from this archive path inside the backup bucket (`dc-rdb` for a store that has never been restored) instead of initialising an empty one. It takes effect only when the store is **created** — against a cluster whose store already exists it moves no data — so it is a rebuild lever, not a repair. Needs the backup plugin, so it is refused on a cluster installed with `--no-cnpg` or `--compact --no-tls`. See [Recovering an instance](./disaster-recovery.md#recover). |
| `--restore-rdb-at <timestamp>` | Stop that recovery at a point in time instead of replaying the whole archive — for data destroyed *correctly*, by a bad migration or a mistaken delete; pick a moment strictly before the damage. Needs `--restore-rdb-from`, and an RFC 3339 timestamp with an explicit offset (`2026-07-27T13:59:00Z`): without one PostgreSQL reads it in the recovering server's own timezone and stops at a different moment than you named. |
| `--max-connections <n>` | The relational database's connection budget (default `600` on a first install; a re-run without it keeps the current budget) — see [the connection budget](#connection-budget). May be raised, but not lowered, while instances run. |
| `--allow-legacy-db-removal` | The relational-database half of the one-time exception described under [What bootstrap does](#what-it-does). |
| `--dry-run` | Print what each step would do without changing anything. A dry run creates no cluster, so checks that need to read one — the `--ha` node-capacity check in particular — report what they could not see rather than failing the rehearsal. What such a check *does* see is still fatal: a cluster that answers and cannot host `--ha` fails a dry run too. On a cluster that answers, a dry run also makes the refusals a re-install makes — an install from a machine without the cluster's state, changed settings under running instances, an unusable `--backup-snapshot-class` — and fails with the same message the install would. On a cluster it cannot reach, it says those were not checked. |
| `--yes` | Do not ask before creating a kind cluster. |
| `--skip-preflight` | Skip the environment checks. |
| `--dev` | Local convenience for a laptop cluster; implies `--build --yes`. |

### `--compact` {#--compact}

`--compact` is a preset for small clusters, chosen at install. It composes levers that
already exist rather than adding a tuning axis of its own:

- lower JetStream and KV per-stream ceilings, and the smaller volumes those permit (3Gi
  JetStream, 2Gi relational Postgres, 4Gi TimescaleDB, and a 20Gi backup store when TLS is
  kept);
- lower scheduling **requests** (25m / 64Mi) for every service and every NATS server, so pods
  fit a small node. Limits are untouched:
  lowering the memory limit converts pressure into OOMKills and lowering the CPU limit
  throttles, and neither shrinks anything. `device-management`, `event-management`,
  `event-sources` and `device-state` keep their higher CPU limits, and the per-service requests
  a default installation uses are turned off so the lower ones apply to every service; see
  [Service sizing](#service-sizing);
- no monitoring stack, the single largest consumer;
- no cert-manager, since with TLS off nothing needs a certificate issued (keep TLS and
  cert-manager stays — see below), and consequently no database backup plugin.

It does **not** change which services run. That stays on each instance's `--profile`, where
it is named and visible. A profile *larger* than `default` — today only `full` — is rejected
on a compact cluster. The published compact numbers are measured on `default`, so they would
not describe an instance running six more services (AI inference, outbound connectors, MCP,
Sparkplug B ingest, LwM2M ingest, and update management). The smaller profiles (`telemetry`, `ingest-only`) are
accepted.

You can keep both TLS and monitoring. An explicit `--no-tls=false` or `--no-monitoring=false`
on `dcctl install` is honoured, and every other compact lever still applies. Keeping TLS also
keeps cert-manager, which is what issues the certificate. On a cluster installed without
cert-manager, every instance is served without TLS: `dcctl bootstrap` defaults `--no-tls` on
and refuses `--no-tls=false`.

:::note Why `--compact --no-tls` drops the backup plugin
The Barman Cloud plugin issues its own certificates through cert-manager, so dropping
cert-manager drops the plugin with it. Turning TLS back on (`--no-tls=false`) restores both.
It takes *both* install flags: `dcctl install --no-tls` without `--compact` is refused, and
`--no-tls` on `dcctl bootstrap`, as in the local-URL example below, only changes how that one
instance is served.
:::

The CloudNativePG operator itself is installed on *every* cluster, compact included: one
Deployment requesting 100m/128Mi, plus its CRDs. That is a footprint cost compact does not
avoid, and it is deliberate. Backup is not a high-availability feature, so the storage tier
has one shape everywhere. Both databases now run on the operator — the relational store and
the event store alike.

:::caution Volume sizes are a time budget, not a capacity budget
The JetStream volume is derived: the per-stream ceilings are reserved up front, so it is sized
to hold their sum. The two database volumes are not. Nothing prunes the command or alarm
tables, and `retentionDays` defaults to `0` — keep data forever. On a compact instance meant
to run indefinitely, set a retention window rather than relying on the volume size.
:::

:::caution Choose it before the first instance
Lowering a ceiling below what a stream or KV bucket already holds succeeds silently, truncates
nothing, and refuses writes until the data ages out. That is why `dcctl install` refuses to
change its settings while any instance exists on the cluster: `--compact` is decided once,
before anything is running under it.
:::

:::tip Zero-config local URL
`dcctl bootstrap local my-instance --build --host localhost --no-tls` exposes the console at
`http://localhost/`, with no hosts-file entry and no certificate warning.
:::

### `--ha` {#ha}

`--ha` is chosen at install, and every instance on the cluster follows it. Each instance runs
its message broker as a 3-node RAFT cluster, one server per node, with **every JetStream
stream and KV bucket replicated across it**. The instance then survives the loss of any one
node without losing messages, device sessions, or live state.

```bash
dcctl install local --ha
dcctl bootstrap local my-instance
```

That one setting sets both halves, and that is the point of it. The broker's size is
infrastructure (OpenTofu); the per-stream replica factor is instance configuration (Helm).
They live in different tools, neither of which can see the other. Raising only the first is
the failure mode this flag exists to prevent: a three-node cluster whose every stream is still
single-replica costs three times the compute, reports three healthy peers, and survives
nothing.

:::caution It survives exactly one node loss
Three servers commit on a majority, so two remain a quorum and one does not. Losing a second
node — including losing one to a rolling node upgrade while another is already down — stops
writes until a node returns. Plan maintenance one node at a time. Surviving two concurrent
losses needs a 5-server cluster, which is not a supported topology today.
:::

**Three schedulable nodes, not three nodes.** The servers carry a hard anti-affinity
constraint. If the cluster cannot place one per node, the surplus stays `Pending` rather than
doubling up, because co-located replicas would cost what replication costs and protect against
nothing. `dcctl` counts schedulable nodes and refuses before provisioning anything. On a local
`kind` cluster this means three workers: kind only removes the control plane's taint on a
single-node cluster, so a control plane plus two workers is a three-node cluster with two
usable nodes.

**`--ha` also runs `event-management` as two pods.** It stores every event. On a three-node
services pool, one of the nodes also runs the NATS server that leads the incoming-event stream,
which uses more CPU than any service. In testing, the scheduler put the single `event-management`
pod there with `device-state`. That node ran at 94 to 95% CPU, and at 6,800 events per second
offered, with one pod, storage fell to 6,592 per second over three minutes, the first stage to fall
behind. A second pod lets storing use another node's CPU. The two pods prefer different nodes, but that is a preference, not a
guarantee. Each pod fills its own batches, so a batch holds about half as many events and the
event store commits about twice as many transactions per event; the event store's node stayed
below 70% CPU. The 6,000 events per second in the last row of [Measured throughput](#measured-throughput) was measured with two pods. Two pods request twice the CPU, 1.8 cores together. Each pod
also holds its own connections to the event store, so these instances keep 80 of its connections
for the platform instead of 40 (see
[Connection cap](../guides/sql-and-bi-access.md#connection-cap)). With `--compact --ha`, and
without `--ha`, `event-management` runs one pod.

#### Databases under `--ha` {#ha-databases}

`--ha` also runs the relational database as three instances with synchronous replication,
behind the same `dc-postgresql` hostname clients already use. The operator maintains that
hostname and moves it to follow the primary across a failover, so no service configuration
changes.

Synchronous replication is what forces *three* instances rather than two. One standby must
confirm every commit, so with only two instances the loss of either one stalls every write:
worse availability than a single node, in exchange for better durability. A third instance
means a standby can be lost without the cluster losing its confirming replica.

The event store is replicated to three instances too, with a deliberate difference: it does
**not** hold a write waiting for a standby. If no standby is available it falls back to
asynchronous replication and catches up when one returns. That trade is right for this store
and wrong for the other one. Events are already held durably upstream in the messaging layer
until they are persisted, so a failover's worth of writes can be replayed. The audit journal
in the relational store has no such upstream, which is why it stalls instead. The cost is that
the event store's recovery point is bounded by replication lag rather than being zero.

`--ha` changes one service's replica count, `event-management`'s (two pods; see above). Every
other service stays at one, and nothing here survives a node loss on its own. Replication is what
makes recovery possible, not what performs it.

:::caution A stalled write is committed, not rejected
This applies to the relational store, the one that stalls. When no standby is available, a
write does not fail — it waits, and the row is already committed locally. A client that gives
up and retries writes twice unless the operation is idempotent. `statement_timeout` does
**not** bound this wait, because the wait happens after the commit rather than during the
statement.
:::

#### When a database primary stops {#ha-database-failover}

A database instance stops when its pod is deleted, when its node is drained, and when a
change to its configuration is rolled out. The instance first writes a checkpoint, then stops
accepting new connections and gives connected clients five seconds to leave. The platform's
services keep their database connections open for as long as they run, so waiting longer for
them would only delay what comes next. After those five seconds the instance ends every open
connection and shuts down. Writes that were in progress fail, and the services retry them.

Under `--ha`, a standby is promoted once the old primary has stopped, and `dc-postgresql` or
`dc-timescaledb-single` moves to it. In testing, the new primary was accepting writes 20 to 25
seconds after the old one ended its connections, about half a minute after the pod was
deleted. On a database that carries these settings, rolling out a configuration change does
not restart the primary in place: the standbys restart first, then the primary role is switched
over to an up-to-date standby, and the old primary restarts as a standby. In testing, that
switchover interrupted writes for about ten seconds. An event store created before these
settings were introduced keeps the in-place restart until `dcctl upgrade` applies its
instance's infrastructure
([What an upgrade applies to the infrastructure](./releases-and-upgrades.md#upgrade-infrastructure)),
or until it is patched as described in the
[release notes](./releases-and-upgrades.md#database-primary-failover-in-seconds).

A single-instance install has no standby to promote. Its database is unavailable until the
instance has restarted, and writes wait for it. In testing, on a small database, writes
resumed about 15 seconds after they stopped; a restart that has more write-ahead log to replay
takes longer.

Either way, events are held by the messaging layer until they are stored. Each one is
delivered up to five times, a minute apart, before it is given up on and
[recorded as undelivered](./observability.md#max-delivery-records). A database outage shorter
than about four minutes therefore leaves no event undelivered.

A stopping instance is given at most two minutes in all. If it has not stopped by then, it is
stopped forcibly and its pod is removed. The most likely reason is that it is still trying to
copy its last write-ahead log to an unreachable backup store. Committed data stays where it was
committed, but part of the backup archive can then be missing: a point-in-time restore may not
reach a moment inside that gap, and the `PostgresWALArchivingFailing` alert is already firing.
Restores to points after the next base backup are unaffected. A base backup that is running
when the primary stops is abandoned, and the next scheduled one runs as usual.

The same two minutes also cover a known issue in the database operator. An instance can shut
PostgreSQL down cleanly and then fail to exit: its log ends with
`failed waiting for all runnables to end within grace period of 30s`, and its pod stays
`Terminating` although the database has stopped. Nothing is wrong with the data, and the pod is
removed when the two minutes are up. A pod that does not yet carry this limit (one created before
it was introduced, or any instance of an event store that predates the limit and has not yet
been reached by an upgrade or by the patch the release notes describe) still carries thirty
minutes; the
[release notes](./releases-and-upgrades.md#database-primary-failover-in-seconds) say how to
recognise that case and clear it.

Under `--ha`, a primary that is being demoted (by a switchover, or because it is failing) is
likewise stopped abruptly if it has not shut down within two minutes. On the relational store
that loses nothing, because every commit is held until a standby has it. The event store does
not wait for a standby when none is available, so a commit made while no standby was attached
exists only on its primary, and is lost if that primary is replaced before a standby catches
up. That is the recovery-point trade described above, and an abrupt stop is one more way to
reach it.

#### Verifying it {#verifying-it}

An HA claim is only worth what the broker actually holds, so check it there rather than in the
rendered configuration:

```bash
dcctl ha verify --instance my-instance
```

This reads the live broker. It asserts that every stream, KV bucket and durable consumer
carries the declared replica factor **with all peers current**, and that the three servers are
on three distinct nodes. It exits non-zero if anything falls short, and prints what it examined
so that a pass over an empty set is not mistaken for a pass.

#### Losing a node, and getting it back {#ha-node-loss}

`--ha` survives the loss of one node. This is what that looks like from outside, in the order it
happens:

- **The broker elects new leaders within seconds.** Streams led from the lost node pick a new
  leader, and in testing acknowledged writes resumed within about ten seconds for services whose
  own broker connection was to a surviving server. Publishes in flight at that moment fail, and a
  device posting over HTTP can see some `503` responses and should retry. A `503` without a
  `Retry-After` header means the publish failed and the event may have been stored anyway, so a
  retry stores it twice unless it carries an `altId` and an `occurredTime` (see
  [quality of service](../guides/connecting-a-device.md#quality-of-service)). New connections
  through the broker's service can keep failing intermittently for about 45 seconds, until the
  server on that node is back or Kubernetes marks the node as lost and stops routing to it.
- **A service connected to the lost server reconnects within about a minute.** A machine that
  stops abruptly closes nothing, so a service whose own broker connection was to the server on
  that node finds out only when that server stops answering. Each service pings its server every
  10 seconds and gives the connection up after three unanswered intervals (30 seconds), or once a
  write to it has made no progress for 10 seconds; together those notice a dead connection within
  40 seconds. The service then reconnects, which takes a few seconds, or longer while the broker's
  service still routes some new connections to the lost server (see the previous point). Until
  then it can neither publish nor receive. If it is `event-sources`, HTTP ingest is refused or
  times out meanwhile, so devices posting over HTTP should retry `503` responses and timeouts. A
  request that timed out may have been stored too, so the same `altId` and `occurredTime` rule
  applies to its retry.
- **Kubernetes may report nothing.** A machine that restarts within the node grace period
  (roughly 40 to 50 seconds) is never marked `NotReady`, so no node event and no Kubernetes alert
  records the loss. When the chart's alerts are installed, `BrokerConnectionDiedSilently` does;
  see [A broker connection that died silently](./observability.md#broker-connection-dead).
- **Event processing can pause for about a minute.** If the lost node's broker server led the
  stream of incoming events, devices' events keep being accepted, but resolving them can stall
  for about a minute, with no error reported, before it resumes and works through the backlog.
  Nothing is lost; alarms and stored events for that minute arrive late.
- **Service pods move after about a minute and a quarter.** Kubernetes first takes roughly 40 to
  50 seconds to decide that the node is lost. Each service pod on it is then evicted after
  `nodeLossTolerationSeconds` (30 by default; `null` restores Kubernetes' own 300) and started on
  another node. At one replica per service, which is the default, a service whose pod was on the
  lost node is unavailable until then. Under `--ha`, `event-management` runs two pods, and when
  they are on different nodes (the scheduler prefers that but does not guarantee it), the other
  keeps storing events meanwhile. The database instances and the database operator use the
  same 30 seconds. The broker's servers do not, because Kubernetes does not recreate them on
  another node while it cannot confirm that the old one has stopped, so a shorter limit would
  gain nothing.
- **A database primary on the lost node fails over.** A standby is promoted once the database
  operator sees that the primary is unreachable, and `dc-postgresql` or `dc-timescaledb-single`
  moves to it. This takes longer than
  [stopping a primary](#ha-database-failover), because nothing tells the operator the primary is
  gone; in testing, with the operator itself on a surviving node, a new primary was writable
  about two minutes after the node was lost. Events wait in the messaging layer meanwhile.
- **Evicted pods on the lost node show `Terminating` until it returns.** Kubernetes cannot confirm that
  they have stopped, so it leaves them there. Do not force-delete a pod whose node is
  unreachable: for a database instance or a broker server, that lets a replacement start while
  the original may still be running on the other side of the fault. If the machine is gone for
  good, confirm that it is powered off and then delete its Node object; Kubernetes then removes
  its pods. A database instance whose node returns rejoins as a standby.
- **Getting the node back is itself a short disruption.** A broker server that was cut off has
  kept holding elections on its own, and when it rejoins, the other servers' streams and
  consumers elect their leaders again. Expect JetStream to answer "temporarily unavailable" for a
  few seconds, about 45 seconds after the node returns, the HTTP ingress to refuse some publishes
  in that window, and some events already in flight to be processed up to a minute late. Nothing
  is lost, and the services re-attach on their own.

Plan a node's return the way you plan its loss, and do not take a second node down until
`dcctl ha verify` passes again.

#### Maintaining a database node {#ha-database-node-maintenance}

Under `--ha` each database runs three instances and never two on one node. On a cluster with
exactly three nodes the databases can use (three database nodes, if you
[set nodes aside for them](#database-placement)), every one of those nodes therefore runs one
instance of every database. This describes cordoning one of them, doing the work and uncordoning
the same node, as for an operating-system patch or a reboot:

- **A primary on the node is switched to a standby.** As soon as the node is cordoned, the
  database operator starts switching the primary to a standby on another node, and the drain can
  evict the old primary while that is under way. Writes to that database pause for the switchover
  (see [When a database primary stops](#ha-database-failover)), and the services retry them. In
  testing a new primary was in place about ten seconds after the cordon.
- **The evicted instances wait for the node to come back.** Each of the other nodes already runs an
  instance of the same database, so the evicted instance of every database stays `Pending` until
  the node is uncordoned. Every database runs on two of its three instances for as long as the node
  is out. That is expected, and there is nothing to fix.
- **Do not let any database lose another instance meanwhile.** If the relational store loses its
  remaining standby, every write to it waits (see [Databases under `--ha`](#ha-databases)). If
  an instance's event store loses its standby, it carries on without replication, and its recovery
  point is then bounded by replication lag. Keep the maintenance short, and take nothing else down
  until the node is back.
- **Uncordon the node when the work is done.** The instances that waited start on it again and
  rejoin as standbys. In testing, after a node that had been out for under a minute, every
  database was back to three ready instances within 45 seconds of the uncordon. A node that is
  replaced rather than returned, as a managed node-pool upgrade does, was not tested: a waiting
  instance can only start where its volume can follow it.

If the databases share nodes with the rest of the instance (you did not set up
[database placement](#database-placement)), draining a node also takes out a broker server and the
service pods on it, and the broker server too stays `Pending` until the uncordon. Treat it as
[losing a node](#ha-node-loss), planned: one node at a time.

Before you drain the next node, check that every database is back to full strength:

```bash
kubectl get clusters.postgresql.cnpg.io -A
```

`READY` should show 3 for every database. If a broker server was on the node, also run
`dcctl ha verify` for the instance, as after [losing a node](#ha-node-loss).

While a node that the instance needs is cordoned, a re-run of `dcctl install` and a new
`dcctl bootstrap` are refused, because a cordoned node does not count toward the nodes they check
for (see [Database placement](#database-placement)); finish the maintenance first.

Without `--ha`, each database has one instance and no standby to switch to. Draining its node is
not held back: the instance is stopped, and the database is unavailable until the instance can
start again. If its volume is tied to that node, as on a local kind cluster, that is not until the
node is uncordoned.

#### Where the database primaries run {#ha-database-primaries}

Each database prefers a node that is not running another DeviceChain database's primary. In
testing on three 8-vCPU nodes, with the relational and the event-store primary on the same node,
that node ran at 94 to 98% CPU while the other two ran at 45 to 51%.

- **It is a preference, not a requirement.** A cluster with fewer nodes still schedules every
  database instance. The one exception is a `ResourceQuota` with the `CrossNamespacePodAffinity`
  scope: it refuses pods whose placement looks at other namespaces, preferred or not, so it
  refuses these database pods in a namespace where it forbids that. The API server's quota
  admission configuration can impose the same limit on every namespace without a matching quota;
  see the [release notes](./releases-and-upgrades.md#v0190-primary-spread).
- **It applies when a database pod is scheduled.** Under `--ha` on three nodes, each node already
  runs one instance of each database, so in practice the preference decides one thing: when an
  instance's event store is created, its first primary goes to a node that is not running the
  relational primary. A [failover](#ha-database-failover), a switchover, or the switchover that
  ends a rolling update of a database can still leave both primaries on one node, and the
  preference does not move them back.
- **An event store created before this preference gains it when its instance is upgraded.**
  Until then its pods carry neither the label the other databases look for nor the preference.
  `dcctl upgrade` applies the instance's event store
  ([What an upgrade applies to the infrastructure](./releases-and-upgrades.md#upgrade-infrastructure))
  and adds both. The label reaches the running pods without a restart. The preference changes the
  pods' specification, so the store's instances restart once for it: under `--ha` the standbys
  first, then a switchover to one of them; without `--ha` the one instance restarts, and events
  wait in the ingest stream until it is back. An upgrade run with `--skip-infrastructure` leaves
  the event store without either until an upgrade without that flag applies it. Gaining the
  preference does not move a primary: under `--ha` that restart ends with a switchover to a
  standby that is already placed, so the primaries can still share a node afterwards. Check, and
  move one if they do, as below.

To see where the primaries are:

```bash
kubectl get pods -A -l cnpg.io/instanceRole=primary -o wide
```

If two of them share a node, switch one database's primary to a standby on another node. With the
CloudNativePG `kubectl` plugin:

```bash
kubectl cnpg promote dc-rdb dc-rdb-2 -n dc-system
```

or, without the plugin:

```bash
kubectl -n dc-system patch cluster dc-rdb --subresource=status --type=merge \
  -p '{"status":{"targetPrimary":"dc-rdb-2"}}'
```

Pick a standby on a node that the first command shows has no primary. A switchover interrupts the
database's writes briefly (see [When a database primary stops](#ha-database-failover)), and the
services retry them. A single-instance installation has no standby to switch to.

### Database placement {#database-placement}

By default the databases run on whichever nodes Kubernetes picks. If your cluster has nodes set aside
for databases, `dcctl install` can put them there:

```bash
dcctl install local --kube-context "$CTX" --ha \
  --database-node-selector example.com/role=database \
  --database-toleration dedicated=database:NoSchedule
```

- `--database-node-selector key=value` runs the databases only on nodes that carry the label.
  Repeat it to require several labels.
- `--database-toleration` lets them onto nodes with a taint, written as `kubectl taint` writes it:
  `dedicated=database:NoSchedule` tolerates that taint with that value, and `dedicated:NoSchedule`
  tolerates it with any value. It needs `--database-node-selector`, because a toleration only allows
  a node; it does not choose one.

A label alone confines the databases but does not keep anything else off those nodes. Only a taint
does that, which is why a pool set aside for databases usually carries both.

**What it places.** The shared relational store, and the event store of every instance bootstrapped
on the cluster. `dcctl bootstrap` has no placement flags: it follows the install. NATS, the backup
object store, the monitoring stack and the services are not placed. NATS sets no tolerations, so on
a cluster whose database nodes are tainted it runs on the other nodes. That is deliberate: a NATS
server uses more than a CPU core under load, and under `--ha` its three servers run on three
different nodes, so on a three-node database pool one of them would share a node with the event
store's primary, the busiest database pod.

**What is checked.**

- Before it installs anything, `install` refuses a placement the nodes cannot take: each database
  runs one instance per node, so it needs as many usable nodes as it has instances (three under
  `--ha`, one otherwise). A node is usable when it carries every label, is not cordoned, and every
  `NoSchedule` or `NoExecute` taint on it is tolerated. The message lists the nodes it found and
  what kept each one out. `bootstrap` checks again for each instance, because nodes can be drained,
  relabelled or tainted after the install.
- The apply checks the same count against the number of instances each database actually runs, and
  refuses before it creates or changes that database.
- A node that is cordoned for maintenance, or not ready, does not count. While a node upgrade has
  one of the database nodes out, a re-run of `install` or a new `bootstrap` is refused; finish the
  upgrade, then re-run. What the databases do while the node is out is under
  [Maintaining a database node](#ha-database-node-maintenance).
- The two databases' primaries still [prefer different nodes](#ha-database-primaries), among the
  nodes you chose. If the selection is a single node, they share it.

To see where the databases run:

```bash
kubectl get pods -A -l cnpg.io/cluster -o wide
```

**Changing it.** Placement is an install setting, so changing it is refused while any instance runs
on the cluster, like the other settings (see [Re-running install](#re-running-install)). That
includes re-running `install` without the flags on a cluster installed with them. Choose it at the
first install: a database's volumes stay where they were created, and storage bound to one node or
one zone cannot follow an instance to a node elsewhere.

With no instance running, nothing refuses the change, and it can still take the relational store
down. The relational store outlives every instance, so a re-run with different placement moves its
pods onto the newly selected nodes. If its volumes are bound to a node (local-path storage, as on
kind) or to a zone outside the new selection, those pods stay `Pending` and the store does not come
back. `dcctl` counts the nodes that match; it does not check where existing volumes live. Change
placement only where the store's storage can follow it, or where you can afford to recreate the
store.

### Service sizing {#service-sizing}

Every backend service requests 128Mi of memory and is limited to 256Mi. CPU is sized per service
from measurement:

| Service | CPU request | CPU limit |
| --- | --- | --- |
| `device-management` | 800m | 2 cores |
| `event-management` | 900m | 2 cores |
| `device-state` | 950m | 2 cores |
| `event-sources` | 1 core | 2 cores |
| `event-processing` | 400m | 1 core |
| every other backend service | 100m | 500m |

Each NATS server requests 500m of CPU and 768Mi of memory, and is limited to 2Gi of memory with no
CPU limit; see [The message broker](#broker-sizing).

Under [`--compact`](#--compact) every backend service and every NATS server requests 25m and 64Mi
instead, and the limits stay as above. The console is sized separately.

The first four services do the per-event work: receiving, resolving and storing every event, and
merging it into each device's live state. `event-processing` runs detection on every event; its
limit is twice what it was measured to use (see below). The first four services' limits are
sized for live device traffic at a tenant's default ingest ceiling of 1000 messages per second,
one reading per message, and for about 4,000 events per second, the rate a default installation
sustained before `event-management`'s persistence defaults were raised (see
[Measured throughput](#measured-throughput)).

- **Requests are what each service used at 6,000 events per second.** A request is the CPU the
  scheduler sets aside for a pod on its node, and nothing else: it decides where the pod goes. Each
  one above is what that service was measured to use at 6,000 events per second, rounded up: the
  rate a default `--ha` installation sustained, with `event-management` at two pods, on a cluster of
  three 4-vCPU database nodes and three 4-vCPU service nodes, with resolution, storage and live
  device state each keeping pace (see [Measured throughput](#measured-throughput)).
  `event-processing` is the exception: its request is what it used with its 1-core limit at 6,800
  events per second offered. With that request and limit, on the release candidate, detection kept
  up at 6,000 (peak backlog under 1,000) and fell behind from 7,600 offered; it is outside the
  stored-exactly-once check in [Measured throughput](#measured-throughput). With the smaller
  requests they had before (those in the superseded row of
  [Measured throughput](#measured-throughput)), these services requested between 15% and 66% of what they used at that rate, and the scheduler put the busiest of them
  together: at 6,000 events per second two service nodes ran at about 80% CPU while the third ran
  at 55%. Memory stays at 128Mi: no event-path service used more than 51Mi in any sample, up to
  9,200 events per second. A request is per pod, so a service scaled to two replicas requests
  twice its figure for the same traffic. Under `--ha`, `event-management` runs two pods, so it
  requests 1.8 cores; see [`--ha`](#ha). Each pod keeps the one-pod figure, measured with one pod
  doing all the work, so there it reserves more than the two use.
- **Limits do not reserve anything.** Kubernetes schedules a pod by its requests, so the higher
  limits need no extra room on a node. They only let a busy service use CPU the node has spare.
  `--compact` lowers every request, and leaves the limits alone.
- **Lowering a CPU limit caps throughput; it does not save capacity.** At 500m, on a four-node
  `--ha` kind cluster, `device-management` resolved at most about 720 events per second, held
  back by its limit in almost every scheduling period, and every event above that waited in the
  inbound stream. Measured there without a limit in the way, it used up to 0.96 millicores of CPU
  per event and `event-management` up to 0.53 per stored event, so the default ceiling needs about
  one core and half a core; each limit is twice that, because a CPU limit is enforced over short
  periods and a busy service reaches it in bursts well before its average does. On a three-node
  cloud cluster, at 500m, `event-sources` was held back by its limit in 84% of scheduling periods
  at 4000 events per second, and its slower responses capped the rate devices could send at about
  4,300 events per second; `device-state` merged live state at no more than about 2,300 events per
  second, so the live device view fell minutes behind. Measured without a limit in the way, they
  use 0.14 and 0.37 millicores per event, so at 4,000 events per second they need about half a
  core and one and a half cores.
- **Detection gets up to one core.** `event-processing` checks every event against the detection
  rules, as one partition. At its earlier 500m limit, on a three-node cloud cluster, it was held
  back in about 5% of scheduling periods at 6,000 events per second and fell behind: its backlog
  reached about 41,000 and 93,000 events in two 10-minute runs. With the 1-core limit and the 400m
  request, on the release candidate, it kept up at 6,000 events per second (peak backlog under
  1,000) and fell behind from 7,600 offered, without being held back by its limit. Detection is
  outside the stored-exactly-once check in [Measured throughput](#measured-throughput). Its limit is
  1 core.
- **The busiest services avoid the event store's primary.** `device-management`,
  `event-management` and `event-sources` prefer a node that is not running the instance's
  event-store primary (on installations using CloudNativePG, the default), which is the busiest
  single process in an installation. It is a preference, not a requirement: on a cluster with
  fewer nodes than busy services they are still scheduled. It applies only when a pod is
  scheduled, so a database failover does not move running pods. With the tuned settings below,
  moving `event-management` off that node raised the sustained rate from about 4,750 to about
  5,600 events per second; its effect at the default settings has not been measured. To turn it
  off for one service, set `functionalAreas.<service>.avoidEventStorePrimary: false`.
- **The five event-path services spread across nodes.** `device-management`, `event-management`,
  `device-state`, `event-sources` and `event-processing` prefer a node running fewer of the five,
  so on three nodes they usually run no more than two to a node. It is a preference the scheduler
  weighs with others, not a guarantee: with fewer nodes they still schedule, and a node with more
  free CPU can win. It applies when a pod is scheduled, so pods already running are not moved.
  Which NATS server leads a stream is decided by NATS, so a service can still share a node with
  the busiest server. A service with a spread of its own no longer gets the cluster's default
  spread, which places one service's replicas on different nodes and in different zones; at one
  replica, the default, that changes nothing. Above one replica the service's own pods still
  prefer different nodes, through a preference to avoid nodes already running one of them, but no
  longer different zones. Under `--ha`, `event-management` runs two pods, so this applies to it. To turn it off for
  one service, set `functionalAreas.<service>.eventPathSpread: false`.
- **The databases' primaries prefer different nodes.** In testing, a node running both the
  relational and the event-store primary ran at 94 to 98% CPU while the others ran at about
  half. See [Where the
  database primaries run](#ha-database-primaries), including how to check after a failover or an
  upgrade.
- **More traffic needs more.** Several tenants each sending at their ceiling, messages that carry
  many readings, or a tenant that is [admitted above its
  ceiling](../concepts/governance.md#ingest-above-ceiling) while it catches up all need more
  than this. Add `replicas` to `device-management`, which scales horizontally (more replicas
  reorder one device's events slightly more; see `watermarkLatenessSeconds` in [the detection
  engine's configuration](./detection-engine.md#configuration)), or raise one service's limit:

  ```yaml
  functionalAreas:
    device-management:
      resources:
        limits:
          cpu: "4"
  ```

  A service's CPU and memory come from three places, and each one wins over the one before it,
  key by key: the top-level `resources`, then the service's measured CPU request
  (`functionalAreas.<service>.measuredRequests`), then the service's own
  `functionalAreas.<service>.resources`. So set only what differs. To guarantee the CPU when the
  node is contended, raise the request under the service's own `resources` as well. A request
  above its service's limit is refused when the chart renders, and the refusal names where each
  value came from.

  The event-path services' own CPU limits and requests are set that way too, so the top-level
  `resources` does not replace them: a top-level limit of 4 cores gives every other backend
  service 4 cores and leaves the five at their own limits (2 cores, and 1 for
  `event-processing`), and a top-level `requests.cpu` reaches none of the five.
  Set theirs under `functionalAreas`, as above. On a chart-only installation,
  `useMeasuredRequests: false` turns the measured requests off, so the top-level requests apply
  to every service; that is what `--compact` does.

The metric that shows a service held back by its limit is
`container_cpu_cfs_throttled_periods_total` for its container.

#### The message broker {#broker-sizing}

Each NATS server requests 500m of CPU and 768Mi of memory, and is limited to 2Gi of memory. Its Go
runtime is given a soft memory limit of 80% of that (`GOMEMLIMIT`), so it collects harder before the
kernel would stop it. Before, the servers requested and were limited to nothing, which put them
first in line for eviction when a node ran short of memory.

- **Memory is sized from measurement.** In the release benchmark no server's resident memory passed
  about 830 MiB (working set about 1.3 GiB) in any run up to 9,200 events per second offered, under
  the 2Gi limit, and none was stopped for memory. The 768Mi request is below that peak: it decides
  where a server is placed, and the limit is what stops one. Those runs were steady ingest: a server rejoining its cluster, or catching up
  a large backlog after a node is lost, was not measured. If a server is ever stopped for running
  out of memory (`OOMKilled` in `kubectl describe pod`), raise its limit.
- **CPU is requested below what a server uses under load**: about 4.4 cores across the three
  servers at 6,000 events per second, 1.6 to 1.8 of them on the server leading the incoming-event
  stream. Under `--ha` on three nodes each node runs exactly one server, so the
  request cannot change where a server runs. What it does is keep the servers out of the class evicted first and
  give them a share of a busy node's CPU. Requesting their full use would take more than 4 cores from
  a three-node cluster without moving anything. Without `--ha`, one server carries every event
  and its use was not measured; 500m understates it. There is no CPU limit: every event passes
  through the broker, and a limit would slow every service at once.
- **Each node needs room for a server.** Under `--ha` the servers must run on three different
  nodes. If one of the three cannot fit 500m and 768Mi more, that server stays `Pending`, and the
  bootstrap waits up to 15 minutes before it fails. `kubectl get pods -n <instance namespace>`
  shows the server `Pending`, and `kubectl describe pod` on it says why.
- **Existing instances get them when they are upgraded.** `dcctl upgrade` applies an instance's
  broker, so an instance created by an earlier release gets these requests and this limit at its
  upgrade to this one, and its servers restart to take them: one at a time under `--ha`. See
  [What an upgrade applies to the infrastructure](./releases-and-upgrades.md#upgrade-infrastructure).
- To change them, set `nats_cpu_request`, `nats_memory_request` or `nats_memory_limit` in a
  `terraform.tfvars` beside the instance's OpenTofu state
  (`~/.devicechain/instances/<instance>/infra/instance/`), which every `dcctl` apply of the
  instance reads, including an upgrade's. Memory takes `Mi` or `Gi`. On an instance installed with
  `--compact`, `dcctl` passes the two requests itself on every apply, which overrides the file, so
  there only `nats_memory_limit` can be changed this way.

#### Measured throughput {#measured-throughput}

| Release | Cluster | Settings | Sustained rate | Result |
| --- | --- | --- | --- | --- |
| v0.18.0 | Google Kubernetes Engine, 3 × n2-standard-8 (8 vCPU each), SSD persistent disks, `--ha` | the defaults v0.18.0 shipped, before the sizing above | about 3,800 events/s | Two 10-minute runs at 4,000 events/s offered each stored 2,399,000 events, as many as were accepted (the totals were compared, not each event). The slowest stage kept 96.5% of the offered rate in one run (resolution, 3,858 per second) and 98% in the other. Live device state kept up only to about 2,300 events per second, at `device-state`'s then 500m limit. |
| v0.18.0 | the same | tuned: see below | about 5,600 events/s | 180-second runs. At 5,600 offered, every stage kept at least 98.9% of the offered rate, the backlog drained in 3 seconds, and the number of events stored equalled the number accepted (the totals were compared, not each event). Live device state kept up. |
| after v0.18.0, before its persistence defaults | the same | the sizing above, with `event-management` at `persistence.writers: 5` and `persistence.maxBatch: 32` | about 3,900 events/s | Two 10-minute runs at 4,000 events/s offered each stored 2,400,000 events, as many as were accepted (the totals were compared, not each event). Storing was the slowest stage, at 96.7% and 95.7% of the offered rate. Live device state stayed within about 40 seconds. |
| after v0.18.0 | the same | tuned: `event-management` at `persistence.writers: 10` and `persistence.maxBatch: 64`; see below | about 6,000 events/s | 180-second runs. At 6,000 offered, every stage kept at least 98% of the offered rate, the backlog drained in 5 seconds, and the number of events stored equalled the number accepted (the totals were compared, not each event). Held for 5 minutes at 6,000, storing kept 96%, so about 5,800 to 6,000 is the sustained figure. |
| after v0.18.0 | Google Kubernetes Engine, 3 × n2-standard-4 database nodes (tainted, databases only) and 3 × n2-highcpu-4 service nodes, SSD persistent disks, `--ha` | the defaults before the requests above (`device-management` 500m, `event-management` and `device-state` 400m, `event-sources` 150m, `event-processing` 100m), with `event-management`'s current persistence defaults | about 6,000 events/s | Superseded by the next row, measured with the requests above and the default volume sizes. Two 10-minute runs at 6,000 events/s offered each stored 3,600,000 events, as many as were accepted (the totals were compared, not each event). Resolution, storage and live device state each kept 99.7% of the offered rate or more, and the backlog drained within 10 seconds. Detection (`event-processing`) fell behind: its backlog peaked at about 93,000 events. The event-store and backup volumes were smaller than the defaults. The requests above were sized from these runs. |
| v0.19.0 release candidate | Google Kubernetes Engine (`us-east4-b`), 3 × n2-standard-4 database nodes (16 GB; tainted, databases only) and 3 × n2-custom-4-8192 service nodes (4 vCPU, 8 GB), standard persistent boot disks, `--ha`, the default volume sizes | the defaults above, with `event-management` at two pods (the `--ha` default) and Go profiling enabled but idle | 6,000 events/s | Two 10-minute runs at 6,000 events/s offered each accepted 5,997 events/s and stored all 3,600,000 accepted events exactly once. That was checked event by event, on each event's device and occurrence time: none missing, none stored twice, none unexpected. Resolution, storage and live device state each kept pace, and the backlog drained in about 2 seconds. Detection (`event-processing`), which that check does not cover, kept up with a peak backlog under 1,000 events, and fell behind from 7,600 offered. |

The v0.18.0 rows were measured on v0.18.0, the last row on the v0.19.0 release candidate, and the
others on the development build between them, all with the load generator on a separate node and a replicated (`--ha`) event store. At
v0.18.0's defaults, what held the rate was `device-management`'s pool of resolvers, and past it the
CPU limits of `event-sources` and `device-state` that the sizing above raises. With those raised,
storing events became the limit: 5 writers committing up to 32 events each filled every batch from
4,400 events per second, and each commit took about 38 milliseconds, so storing stopped near 4,200
per second. That is why `event-management` now defaults to 10 writers and batches of up to 64 (see
[Event persistence](./observability.md#event-persistence)).

The v0.18.0 tuned row used `device-management` at 2 replicas with `resolution.workers: 32` and
`rdbConfiguration.maxOpenConnections: 48`; `event-sources` at 2 replicas; `event-management` with
`persistence.writers: 10` and `persistence.maxBatch: 64`, on a node without the event-store
primary; `device-state` with `projection.writers: 5`, `projection.maxBatch: 64` and
`projection.lingerMillis: 25`; CPU limits of 4 cores (2 for `event-processing`); and memory limits
of 1Gi. The later tuned row kept `device-management` at its defaults and one replica, and otherwise
used the same `event-management` and `device-state` settings, with CPU limits of 4 cores and memory
limits of 1Gi for `device-management`, `event-sources`, `event-management` and `device-state`.
There, `event-management` used at most about 1.7 cores; it was not measured under its default
limit of 2. Its batches averaged below 32 in every run, so the measurement does not show a batch
of 64 helping over 32. Batches averaged about 21 events at 6,000 per second; past it, storing
stopped rising with batches averaging 28 to 30, below the limit, while two of the three nodes, one
of them the event store's, were at 86 to 95% CPU. Which of those held the rate was not isolated,
but more throughput on that three-node cluster needs more nodes before more per-service tuning. A
default installation with the new persistence settings was measured on the split clusters in the
last two rows, where it sustained 6,000 events per second, with `event-management` at two pods in the last row; it
has not been measured on the three-node cluster of the other rows.

#### Event store volume {#event-store-volume}

Each event-store instance has a 32Gi volume, so three of them under `--ha` (4Gi each under
`--compact`). A stored measurement costs about 1.05 KB, and the database's write-ahead log takes
about 1.1 GB more while backups keep up with it, so 32Gi holds about 27 million events: about
seven hours of one tenant sending at its full default ceiling.

The event store compresses the page images in its write-ahead log (`wal_compression = lz4`).
After each checkpoint, the first change to a page writes the whole page into the log, and on this
store most of those pages are index pages. In one comparison on a development build after v0.18.0,
on a cluster of the same shape as in [Measured throughput](#measured-throughput), with tuning
that differs from the rows there, at 5,200 events per second offered, compression cut the log written per
stored event from about 3.0 KB to about 1.7 KB, and the checkpoints forced by the log's size fell
by about the same proportion (from 5.8 to 3.2 per million events stored). It does not change what
this volume holds: the log on it still takes about 1.1 GB while backups keep up with it, so the
figure above stands. The [backup store sizing](#backup-store-size) was measured before this
compression, the time-led keys and zstd archiving, so it errs large: in the release benchmark the
event store's archive took about 0.47 KB per stored event, against the 1.9 KB that sizing assumes
for both databases; the relational database's share was not re-measured. The relational store does not compress its log.

With the [default backup store](#backup-store-size) and one instance, this volume is what fills
first under sustained ingest, and `DatabaseVolumeFillingFast` warns before it does. If the backup
store fills first (several instances on it, a smaller store, or an event store grown past its
default), archiving stops, and the write-ahead log the database cannot ship piles up on this
volume until it fills and the database stops; the
[alerts](./observability.md#backup-archiving) warn before either. A retention window (`retentionDays` in event-management's
`lifecycle` settings) bounds the stored data on an instance meant to run indefinitely, but not
the archive.

The size is fixed when an instance is created. `dcctl upgrade` applies the rest of the event
store's configuration and keeps its volume at the size it has. To grow one, on a StorageClass that
allows volume expansion:

```bash
kubectl -n dci-<instance> patch clusters.postgresql.cnpg.io dc-tsdb --type merge \
  -p '{"spec":{"storage":{"size":"64Gi"}}}'
```

Growing this volume moves the point where the backup store fills first. Grow the backup store by
about five times what you add here; see [Backup store size](#backup-store-size).

## After bootstrap {#after-bootstrap}

The command prints the namespace, the **superuser** credential, and how to reach the instance
through the cluster ingress. The superuser is `superuser@devicechain.local`, and there is no
default password. The bootstrap generates one for the instance, keeps it in the Secret
`dci-<instance>-superuser` in the instance's namespace, and prints it once, at the end of the
run that generated it (or of the run that finishes a bootstrap that failed before printing it).
To read it again:

```bash
kubectl -n dci-my-instance get secret dci-my-instance-superuser -o jsonpath='{.data.password}' | base64 -d
```

### The superuser password {#superuser-password}

The user-management service reads that password only once: to create the superuser the first
time it starts against an empty identity table. After that, the Secret is a record of the
password the superuser was first given. Changing the password in the console does not update
it. When a bootstrap **recovers** an instance and its identities come back with it, the
restored superuser keeps the password it had. The report then does not print the Secret's
value, and says it may not be the superuser's password.

Instances bootstrapped by an earlier release have no such Secret. Their superuser was created
with the default password those releases published. Neither `dcctl upgrade` nor a bootstrap
re-run against the running instance (a restore, or `--allow-legacy-db-removal`) changes it or
generates a Secret for it, and both say so at the end. If that password has not been changed
since, change it in the console.

An install made with Helm alone, without `dcctl`, must create that Secret itself (key
`password`) before user-management first starts, or name another one with the chart value
`instance.superuserSecret`. Without it, user-management refuses to create the superuser.

### Sign in to the console {#sign-in}

The instance includes the **web console**. The ingress serves it at the host root
(`https://<host>/`) and routes `https://<host>/api/<area>/graphql` to each functional-area
service. Open the console in a browser and sign in with the superuser's email and password.

A fresh instance is **tenant-less**, so you land in the admin console (`/admin`) to create
your first tenant and assign memberships. Switch into a tenant to reach the tenant console.
(For a headless/ingest-only instance, deploy with the console disabled — see the chart's
`frontend.enabled` value.)

To inspect the running instance:

```bash
kubectl --context <kube-context> get pods -n dci-my-instance
```

### Run a simulation {#run-a-simulation}

To explore the console against a moving fleet rather than an empty one, run a
**simulation**. `sim create` mints a scoped identity and tenant on the instance and writes the
handshake file the `dc-simulator` process reads to come up:

```bash
dcctl sim create demo --instance my-instance --server localhost
```

The simulator then drives telemetry and alarms in over the same device wire real hardware
uses — see [Trying it with simulated data](../intro.md#trying-it-with-simulated-data).

## Removing an instance {#destroy}

```bash
dcctl destroy local my-instance
```

`dcctl destroy` removes **that instance only**, in this order:

1. Its Helm release. The instance's namespace belongs to that release, so uninstalling it
   deletes the namespace and everything in it, including its NATS broker and its event store.
2. Its infrastructure state, through `tofu destroy`, which removes anything that state still
   holds. Destroy prints OpenTofu's plan of what it removes, without the plan's list of output
   values: a destroy works some of those values out from the configuration's defaults rather
   than the instance's settings, so the list would not describe the instance.
3. Its database and database login on the shared relational database.
4. Its namespace, if it is still there. A bootstrap that stopped before Helm installed
   anything leaves a namespace with no release to uninstall. Destroy waits to see the
   namespace fully gone.
5. Its event-store backups, when they are in the cluster's own backup object store (the
   [default destination](#default-backup-destination)): everything under the path the
   instance's event store was archiving to — its write-ahead log archive and base backups —
   is deleted, and destroy checks that the path is empty afterwards.
6. It checks that what it deleted is really absent, and only after that removes its local
   state under `~/.devicechain/instances/<instance>/`.

The root-key escrow artifact is kept — see
[Disaster Recovery](./disaster-recovery.md#after-destroy).

### What happens to the instance's backups {#destroy-backups}

Destroy reads which path the instance's event store archives to before it changes anything,
prints it, and removes that path only once the instance's namespace is gone, so nothing is
still writing to it. It removes that one path and nothing else: not the shared relational
database's backups, not another instance's, and not an older archive left under the same
instance name.

- **Backups in an object store you supplied** (`dcctl install --backup-credentials-file`) are
  never deleted. They are the copy that outlives the cluster. Destroy prints where they are;
  delete them yourself when you no longer need them.
- **`--keep-backups`** keeps the in-cluster backups too. Use it when you mean to rebuild the
  instance from them with `dcctl bootstrap --restore-tsdb-from` in the same cluster: a destroy
  without it deletes exactly the archive that restore reads. `dcctl destroy --all` accepts
  `--keep-backups`, and applies it to every instance.
- **Volume-snapshot base backups** (`dcctl install --backup-snapshot-class`) of the instance's
  event store are in its namespace, and are deleted with it, `--keep-backups` or not. No restore
  reads them: `--restore-tsdb-from` reads the backup store, which `--keep-backups` keeps.
- **If the object store cannot be reached**, or destroy cannot tell which path is the
  instance's, the destroy still finishes. It says what it left, and its closing line does not
  report the instance as fully destroyed.

A destroy interrupted after the event store is gone, including one interrupted while it removes
the backups, remembers the path it read, so running it again still removes them.

Releases before this one left a destroyed instance's backups in the in-cluster store, where
nothing ever removes them. After it removes the instance's own backups, destroy lists paths in
the bucket that look like earlier archives of the same instance name, and leaves them. It lists
nothing when it leaves the instance's own backups in place: with `--keep-backups`, in an external
store, or when it could not reach the store or delete from it. To find and remove such archives by hand, reach the
store through a port-forward and use any S3 client, for example the AWS CLI:

```bash
kubectl -n dc-system port-forward svc/dc-object-store 9000:9000 &
export AWS_ACCESS_KEY_ID="$(kubectl -n dc-system get secret dc-object-store-credentials \
  -o jsonpath='{.data.MINIO_ROOT_USER}' | base64 -d)"
export AWS_SECRET_ACCESS_KEY="$(kubectl -n dc-system get secret dc-object-store-credentials \
  -o jsonpath='{.data.MINIO_ROOT_PASSWORD}' | base64 -d)"
aws s3 ls s3://devicechain-tsdb/ --endpoint-url http://127.0.0.1:9000
aws s3 rm --recursive s3://devicechain-tsdb/<path>/ --endpoint-url http://127.0.0.1:9000
```

Each running instance archives under the path its event store names; see it with
`kubectl -n dci-<instance> get clusters.postgresql.cnpg.io dc-tsdb -o yaml` (the `serverName`
under `spec.plugins`). Remove only paths no running instance uses and nobody will restore from.

If a step fails or is interrupted — including a namespace that is still terminating when the
wait runs out — destroy exits with an error and keeps the local state. Running the same
command again picks up where it stopped.

If the instance is still running but its local infrastructure state is missing — lost, or the
instance was bootstrapped from another machine — destroy **refuses**, because it cannot run
the infrastructure destroy without that state. `--without-state` removes the instance anyway,
by its Helm release, database and login, and namespace, and reports that the infrastructure
destroy was skipped. An instance whose local state predates the split of the infrastructure
into a cluster part and an instance part is refused the same way, before any change, and needs
`--without-state` too. `dcctl destroy --all` accepts `--without-state`.

Destroy never deletes the cluster or the prerequisites `dcctl install` put there, so the next
`dcctl bootstrap` on the cluster needs no install first. Destroying an instance and
bootstrapping it again under the same name is how you recreate an instance. An instance built
by an older release, before `dcctl install` existed, is the exception: its cluster has to be
recreated too — see [Releases & Upgrades](./releases-and-upgrades.md#pre-declaration-recreate).

If the kind cluster itself is already gone — deleted with `kind delete cluster` — destroy has
nothing to uninstall and says so, clearing local state only. That is the instance's directory,
and the cluster's own directory under `~/.devicechain/clusters/<cluster-uid>/`, which
`dcctl install` created and `kind delete cluster` left behind (see
[Install the cluster](#install)). As it does, it prints:

```text
removing the gone cluster's local state (~/.devicechain/clusters/<cluster-uid>)
```

There is no uninstall command yet. To delete a local cluster that `dcctl install` created, use
kind directly, as shown under [Install the cluster](#install). On a cluster you keep, see
[Removing a cluster](#removing-a-cluster).
