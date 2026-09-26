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
as it is, and what is missing is added.

**Changing its settings** — `--ha`, `--compact`, monitoring, backups — is refused while any
instance exists on the cluster. Every instance was built to the settings in place when it was
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

### The connection budget {#connection-budget}

The relational database has a fixed number of connections, set by `--max-connections`
(default `600`). Each instance reserves a connection limit on its database login, sized from
the areas it enables. `dcctl bootstrap` refuses an instance whose reservation does not fit in
what is left.

That check runs **before anything of the instance is written** — no namespace, database or
login — so a refused bootstrap leaves nothing to clean up. A cluster meant to hold many
instances, or instances with many areas enabled, needs a larger budget.

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
repair it. Step 3 refuses only a **live** instance, which it recognises by the configuration
document written in step 8. Everything short of that is a half-built instance, and running
the bootstrap again is the supported way to finish it.

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
6. **Render configuration** — resolve the instance id, namespace, profile, and every generated
   credential: the broker-auth material (the shared service password and the callout issuer
   key), the certificate authority that signs the broker's own TLS certificate, the
   cross-service auth secret, and the **secret-store root key**. All of them are minted here,
   because step 3 has established there is no live instance to take them from. Finishing a
   half-built instance is the exception: there the step reads back what an earlier run already
   put in the cluster rather than generating a second set.
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
   Helm Go SDK, blocking until the workloads are ready. That document is what makes the
   instance live, and what step 3 looks for on any later run.
9. **Wait for readiness** — poll each enabled area's Deployment until it has finished rolling
   onto the configuration this run produced. This is an explicit confirmation gate rather
   than trusting the Helm step's own wait. Having replicas available is not enough: where pods
   are being replaced, that is already true of the ones on their way out. So the step also
   waits for the new template to be observed, for every replica to be recreated on it, and for
   no old replica to still be running. `dcctl upgrade` uses the same gate for the same reason.
10. **Report access info** — print the namespace, the superuser's email and where its password
    is kept (plus the password itself, once, on the run that generated it), and how to reach
    the instance.

:::tip `Ctrl+C` stops a run cleanly
An interrupted run stops the infrastructure tool gracefully — it finishes what it is doing and
writes its state — and hands the cluster lock back, so re-running is all you need. A second
`Ctrl+C` exits immediately and gives up both. See [Interrupting a run](./cluster-lock.md#interrupt).

If the run had already reached step 8, the instance exists and the bootstrap will refuse the
next time you run it. That is not a dead end: the instance is built, and `dcctl upgrade` is how
you move it from there.
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

## Prerequisites {#prerequisites}

- **A Kubernetes cluster, version 1.29 or newer**, and a kube-context pointing at it. The
  floor comes from the CloudNativePG charts, which refuse to install below it.
  `dcctl preflight` checks it up front, because otherwise the failure lands part-way through a
  bootstrap that has already written your root-key escrow file. For the `local` provider this
  is a kind cluster, which `dcctl install local` creates for you (`--cluster <name>`, default
  `devicechain`). Pass `--kube-context <name>` to use a cluster you already have instead
  (kind / minikube / k3d / docker-desktop).
- **OpenTofu** (the `tofu` binary; `terraform` also works) on your `PATH`. `dcctl` drives it
  to provision infrastructure. Install it from [opentofu.org](https://opentofu.org). Run
  `dcctl preflight local` to check this and the rest of your environment up front.
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
| `--profile <profile>` | Functional-area profile: `default` (the standard system, used when omitted), `full` (everything — adds AI inference, outbound connectors, MCP, Sparkplug B ingest, and LwM2M ingest), `telemetry`, or `ingest-only`. |
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
| `--restore-rdb-from <archive>` | Disaster recovery: recover the shared relational store from this archive path inside the backup bucket (`dc-rdb` for a store that has never been restored) instead of initialising an empty one. It takes effect only when the store is **created** — against a cluster whose store already exists it moves no data — so it is a rebuild lever, not a repair. Needs the backup plugin, so it is refused on a cluster installed with `--no-cnpg` or `--compact --no-tls`. See [Recovering an instance](./disaster-recovery.md#recover). |
| `--restore-rdb-at <timestamp>` | Stop that recovery at a point in time instead of replaying the whole archive — for data destroyed *correctly*, by a bad migration or a mistaken delete; pick a moment strictly before the damage. Needs `--restore-rdb-from`, and an RFC 3339 timestamp with an explicit offset (`2026-07-27T13:59:00Z`): without one PostgreSQL reads it in the recovering server's own timezone and stops at a different moment than you named. |
| `--max-connections <n>` | The relational database's connection budget (default `600` on a first install; a re-run without it keeps the current budget) — see [the connection budget](#connection-budget). May be raised, but not lowered, while instances run. |
| `--allow-legacy-db-removal` | The relational-database half of the one-time exception described under [What bootstrap does](#what-it-does). |
| `--dry-run` | Print what each step would do without changing anything. A dry run creates no cluster, so checks that need to read one — the `--ha` node-capacity check in particular — report what they could not see rather than failing the rehearsal. What such a check *does* see is still fatal: a cluster that answers and cannot host `--ha` fails a dry run too. |
| `--yes` | Do not ask before creating a kind cluster. |
| `--skip-preflight` | Skip the environment checks. |
| `--dev` | Local convenience for a laptop cluster; implies `--build --yes`. |

### `--compact` {#--compact}

`--compact` is a preset for small clusters, chosen at install. It composes levers that
already exist rather than adding a tuning axis of its own:

- lower JetStream and KV per-stream ceilings, and the smaller volumes those permit (3Gi
  JetStream, 2Gi relational Postgres, 4Gi TimescaleDB);
- lower scheduling **requests** (25m / 64Mi), so pods fit a small node. Limits are untouched:
  lowering the memory limit converts pressure into OOMKills and lowering the CPU limit
  throttles, and neither shrinks anything;
- no monitoring stack, the single largest consumer;
- no cert-manager, since with TLS off nothing needs a certificate issued (keep TLS and
  cert-manager stays — see below), and consequently no database backup plugin.

It does **not** change which services run. That stays on each instance's `--profile`, where
it is named and visible. A profile *larger* than `default` — today only `full` — is rejected
on a compact cluster. The published compact numbers are measured on `default`, so they would
not describe an instance running five more services (AI inference, outbound connectors, MCP,
Sparkplug B ingest, and LwM2M ingest). The smaller profiles (`telemetry`, `ingest-only`) are
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

`--ha` does not change the number of service replicas, and nothing here survives a node loss
on its own. Replication is what makes recovery possible, not what performs it.

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
settings were introduced keeps the in-place restart until it is patched as described in the
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
it was introduced, or any instance of an event store that has not been patched as the release notes
describe) still carries thirty minutes; the
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
  leader, and in testing acknowledged writes resumed within about ten seconds. Publishes in flight
  at that moment fail, and a device posting over HTTP can see some `503` responses and should
  retry. New connections through the broker's service can keep failing intermittently for about
  45 seconds, until Kubernetes marks the node as lost and stops routing to the server on it.
- **Event processing can pause for about a minute.** If the lost node's broker server led the
  stream of incoming events, devices' events keep being accepted, but resolving them can stall
  for about a minute, with no error reported, before it resumes and works through the backlog.
  Nothing is lost; alarms and stored events for that minute arrive late.
- **Service pods move after about a minute and a quarter.** Kubernetes first takes roughly 40 to
  50 seconds to decide that the node is lost. Each service pod on it is then evicted after
  `nodeLossTolerationSeconds` (30 by default; `null` restores Kubernetes' own 300) and started on
  another node. At one replica per service, which is the default, a service whose pod was on the
  lost node is unavailable until then. The database instances and the database operator use the
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

## After bootstrap {#after-bootstrap}

The command prints the namespace, the **superuser** credential, and how to reach the instance
through the cluster ingress. The superuser is `superuser@devicechain.local`, and there is no
default password. The bootstrap generates one for the instance, keeps it in the Secret
`dci-<instance>-superuser` in the instance's namespace, and prints it once, at the end of the
run that generated it. To read it again:

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
   holds.
3. Its database and database login on the shared relational database.
4. Its namespace, if it is still there. A bootstrap that stopped before Helm installed
   anything leaves a namespace with no release to uninstall. Destroy waits to see the
   namespace fully gone.
5. It checks that what it deleted is really absent, and only after that removes its local
   state under `~/.devicechain/instances/<instance>/`.

The root-key escrow artifact is kept — see
[Disaster Recovery](./disaster-recovery.md#after-destroy).

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
kind directly, as shown under [Install the cluster](#install).
