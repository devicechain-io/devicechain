---
sidebar_position: 6
title: Disaster Recovery
---

# Disaster Recovery

Restoring a DeviceChain instance takes **two** things: a backup of its databases, and
the instance's **secret-store root key**. Almost every backup procedure captures the
first and silently omits the second.

This page is about the second one.

## Two backups, not one {#two-tiers}

An instance's data sits on **two separate database servers**, and they are worth
backing up and restoring as two separate operations rather than one:

| | **Core data** (PostgreSQL) | **Event data** (TimescaleDB) |
|---|---|---|
| What | Tenants, identities, devices, profiles, detection rules, dashboards, connectors, last-known state — **and every stored secret** | Measurements, event history, rollups |
| Size | Megabytes; it grows with your fleet's *configuration* | The volume tier; it grows with time |
| Losing it | The instance cannot be rebuilt | History is gone; the instance still runs |
| Needs the root key | **Yes** | No |

This is not a policy imposed on one database — it is how the platform already
stores things. `event-management` owns the event store — its schema, its hypertables,
its retention policies — and it is the only service that writes telemetry there; every
other service keeps its own data on the relational server. The one other service that
reaches TimescaleDB at all is `user-management`, through a guest connection that creates
no schema and runs no migrations, and only to delete a purged tenant's rows. No write
spans both stores, so there is nothing to keep transactionally consistent between them.
The one thing to know when the two halves carry different recovery points: the tenant's
row on the relational side is what drives that deletion and is removed once it is done,
so event data restored to a point *before* a tenant was purged, next to core data
restored to a point *after* its row was released, brings back telemetry that nothing
will erase again.

Two consequences worth planning around:

- **Different schedules.** Both stores are archived the same way — a base backup plus
  a continuous stream of write-ahead log — but they do not want the same base-backup
  cadence or the same retention. Core data is small and changes when someone changes
  something. Event data is bulk, append-mostly, and already under a retention policy
  ([data lifecycle](../concepts/architecture.md)) — keeping base backups of chunks the
  lifecycle reconciler is about to drop is paying twice to store the same rows.
- **Different recovery targets.** Restoring core data alone gives you a *working*
  instance: devices reconnect, detection rules run, commands dispatch, secrets
  decrypt. Restoring event data backfills history into it. An instance missing its
  event data is degraded — empty history widgets — not down, so the two halves can
  carry genuinely different recovery-time targets.

**The root key gates the core-data half only.** Event data holds no ciphertext, so a
TimescaleDB restore needs nothing from this page. Everything below is about core
data.

## Why the root key needs its own procedure {#root-key}

Every secret DeviceChain stores — the key that signs every sign-in token,
outbound-connector credentials, SMTP passwords, AI provider keys — is encrypted at rest
under a per-secret data key, and each of those data keys is wrapped by one
instance-wide **root key** (the KEK, see [Architecture](../concepts/architecture.md)).
The token-signing key is stored in every instance, whatever its profile, so every
instance depends on its root key: without it, no one can sign in.

That root key lives in the instance's Kubernetes Secret, which means it lives in
**etcd**, and no database backup contains etcd. A PostgreSQL backup archives
PostgreSQL; a TimescaleDB backup covers TimescaleDB. Neither contains a single byte
of the key.

The consequence is a failure that passes the drill most people actually run:

- **Restore the databases in place** — into the same cluster — and everything works,
  because etcd still holds the key. This is the rehearsal that gives false
  confidence.
- **Restore to a fresh cluster** — the actual disaster — and the encrypted rows
  rehydrate perfectly, the restore reports success, and every one of those secrets is
  permanently unreadable. The new cluster minted a *different* root key, and the old
  one is not derivable from anything you still have.

This used to surface only later, as an unexplained decryption error long after the
backup that could have helped had rotated away. Two checks now catch it, at different
moments and on different evidence, and it is worth knowing which is which:

- **Before anything is built**, `dcctl bootstrap` asks the relational store what it
  already holds for the instance. A database that is there without this cluster having
  built it can only have outlived the cluster that did, so the bootstrap stops rather
  than minting a key over it. This is the one that prevents the mistake.
- **At startup**, a service that stores secrets checks its root key against its own
  stored rows and refuses to serve if the key does not open them. This one catches a
  wrong key rather than a missing one — a recovery pointed at the wrong artifact — which
  no reading of the store can see.

The startup check takes down the **whole API**, not just the integrations.
user-management stores the token-signing key, so it is one of the services that refuses
to start. Every other service waits for user-management's signing keys before it reports
ready, so none of them serve either, and no one can sign in.

Both make the mistake loud and immediate instead of slow and scattered. Neither recovers
anything. If the key is gone, it is gone.

:::danger There is no recovery from a lost root key
The key is 256 bits of randomness and the wrapped data keys are not brute-forceable.
If the key is gone, the secrets are gone — a support ticket cannot recover them. That
includes the token-signing key, so an instance whose root key is lost or wrong **does
not start**: user-management refuses, and nothing else becomes ready without it. This
is the one piece of DeviceChain state with no second chance, which is why the escrow
below is on by default.
:::

## The escrow artifact

`dcctl bootstrap` writes an **encrypted escrow artifact** — a small text file
containing the root key, sealed under a passphrase you choose:

```
~/.devicechain/escrow/<instance>-rootkey.escrow
```

It is a self-describing text file. Opened years later by someone who has never seen
one, it explains what it is, what it protects, what happens if it is lost, and the
exact command to recover with — without needing this page.

Two properties are worth knowing:

- **It is not stored with the instance.** It deliberately does *not* live in
  `~/.devicechain/instances/<instance>/`, because [`dcctl destroy`](#after-destroy) removes that
  directory. `dcctl` refuses an `--escrow-file` path inside it.
- **It carries a key fingerprint in the clear.** That is what makes "is this escrow
  still the right one?" a question you can answer *without* the passphrase — see
  [verifying](#verify).

### Choosing a passphrase

Bootstrap takes the passphrase from the first of these it finds:

| Source | Use it for |
|--------|-----------|
| `--escrow-passphrase-file <path>` | Automation with a secret manager; a trailing newline is stripped. |
| `DCCTL_ESCROW_PASSPHRASE` | CI and scripted installs. Set-but-empty is an error, not a fallback. |
| Interactive prompt | A person at a terminal. Asked twice, to catch a typo now rather than during a recovery. |

If none is available and there is no terminal to ask at, **bootstrap fails**. That is
deliberate: the alternative is quietly producing an instance whose secrets die with
its cluster.

:::caution Store the file and the passphrase separately, and off the cluster
Both in the same place is one compromise away from being no protection at all, and
both on the cluster is one disaster away from being no backup at all.
:::

### Opting out

For a genuinely disposable instance — a CI run, a demo, a local experiment — pass
`--no-escrow`. `--dev` implies it.

```bash
dcctl bootstrap local scratch --dev            # no escrow, disposable by construction
dcctl bootstrap local scratch --yes --no-escrow
```

The bootstrap summary then says so in red. Do not use it for anything whose secrets
you would miss.

## Recovering an instance {#recover}

Recovery **builds** a new cluster and a new instance on it. There is no "restore into
the running instance" step, deliberately: restoring underneath services that have
already created their own schemas means dropping tables they hold open and racing
their migrations. **Recover by rebuilding.**

Two databases, two commands, in that order — because the two stores belong to different
things. The relational database is installed once per cluster and holds **every**
instance's data, so `dcctl install` recovers it. The event store is one instance's, so
`dcctl bootstrap` recovers that one.

**1. Recover the shared relational database**, as the cluster is prepared.

```bash
dcctl install local --restore-rdb-from dc-rdb
```

`--restore-rdb-from` names the folder inside the backup bucket — `dc-rdb` for a store
that has never been restored, since the archive is written under the cluster's own name.
Add `--restore-rdb-at`, an RFC 3339 timestamp strictly before the damage, for the other
kind of disaster: the one where the data was destroyed correctly, by a bad migration or
a mistaken delete, and you want the state just before it.

Pass the same `--backup-credentials-file` the original install used, so the new cluster
reads the archive the old one wrote.

The flag only takes effect when the relational store is *created*, so aiming it at a
cluster that already has one moves no data at all rather than half-working — `dcctl`
says so, before it applies anything. Recover by installing into a cluster whose
relational store is not there.

Where the recovered store archives *afterwards* is chosen by `dcctl`, not by you, and
there is no flag for it: a recovered database that kept archiving to the path it read
would stop on its own safety check and hang on the way up. `dcctl` gives it a path of
its own and then keeps that path across every later run.

:::caution The rows come back; the keys do not
A database backup contains no root keys. Every secret in the recovered store is still
sealed by the key of the instance that wrote it, and that key lived only in the cluster
you just lost. Rebuild each instance with `--restore-root-key` in step 2.

`dcctl bootstrap` refuses to do it any other way — this is the first of the two checks
described above. Before it writes anything it asks the recovered store what it already
holds under that instance's name, and a run with no `--restore-root-key` stops, naming
the flag.

The refusal is the last line, not the first. It can only speak for an instance whose
escrow artifact still exists: one bootstrapped with `--no-escrow` has no second copy of
its key anywhere, and nothing can open those rows again.
:::

**2. Rebuild the instance with its root key**, and with its event data.

```bash
dcctl bootstrap local my-instance \
  --restore-root-key ~/backups/my-instance-rootkey.escrow \
  --restore-tsdb-from dc-tsdb-my-instance-1a2b3c4d
```

The instance's secret-store root key is seeded from the escrow artifact instead of being
minted, so the instance keeps the key its secrets were encrypted with and the rows
recovered in step 1 can be decrypted. You will be asked for the artifact's passphrase
(or supply it with `--escrow-passphrase-file` / `DCCTL_ESCROW_PASSPHRASE`).

`--restore-tsdb-from` is optional and independent: the event store keeps its own
timeline on purpose, so rewinding telemetry to yesterday does not mean the control plane
should be rewound with it, and step 3 does not depend on it. It takes `--restore-tsdb-at`
for a point in time, the same way. Every instance's event store archives under a path of
its own, so read the path off the archive rather than guessing — `dc-tsdb` alone is the
relational-style name and will not be there.

A restore is one of the few things allowed to run against an instance that already
exists — recovery is exactly the situation a run gets interrupted in and has to be
retried, and a sharper guard makes that safe by permitting it only when the escrow
artifact carries the key the instance is already running on.

**3. Confirm the root key is the escrowed one** with `dcctl secrets escrow verify` (see
[Verifying your escrow](#verify)). Being able to sign in at all is the first sign that
the key is right: user-management does not start until the root key opens its sealed
token-signing key. Reading a secret-backed object back — an outbound connector, a
notification channel — is the stronger check, and it is available once step 1 has
recovered the store that object lives in: a restore that returns rows is not proof; a
value that decrypts is.

**4. If you restored event data, check the machinery and not the row count.** A
recovered event store can hold every row and still have quietly stopped being a
time-series database — the tables are there, the queries return, and the thing that is
missing is the background work. That store answers queries perfectly for as long as it
takes the disk to fill.

Open a shell on the event store's primary — under the operator, `psql` needs no
password there. Two names built from the instance id appear in the command below and they
are not the same string: the namespace is `dci-` plus the instance id, while the database
is the instance id on its own, with no prefix.

```bash
kubectl -n dci-<instance-id> exec -it dc-tsdb-1 -c postgres -- psql -U postgres -d <instance-id>
```

Ask it two questions. **First, are the event tables still hypertables?**

```sql
SELECT hypertable_schema, hypertable_name FROM timescaledb_information.hypertables;
```

Your event tables live in the `event-management` schema and should all be listed. A
table that came back as an ordinary table is the failure this catches, and it is
invisible in a schema dump — a plain table and a hypertable look identical there.
(Don't look for `measurement_rollups`. It is a continuous aggregate, and the
hypertable backing it is internal, so its absence from this list is normal.)

**Second — and this is the one that matters — is the job scheduler actually running
_on this cluster_?** Run this, wait a minute or two, and run it again:

```sql
SELECT job_id, proc_name, total_runs, last_run_status
  FROM timescaledb_information.job_stats ORDER BY job_id;
```

**`total_runs` must MOVE.** That is the whole check.

:::danger Do not judge this by `next_start` or by a scheduled flag
It is the obvious field to reach for and it cannot tell you anything here. The table
behind `next_start` is an ordinary table, so a physical restore brings it back holding
the *old* cluster's values. A recovered store whose scheduler never started shows every
job as scheduled with a perfectly plausible future `next_start` — and stays that way
forever. It looks healthy **because the data restored, not because anything will ever
run.** `total_runs` is a counter, so watching it advance observes work happening on the
cluster in front of you.
:::

:::caution The bootstrap finishing is not the database being ready
`dcctl` reports success once the workloads are up, which can be before the recovering
database has finished replaying its archive. If a recovery cannot reach its archive it
sits waiting rather than failing, so check the database itself before you believe a
restore:

```bash
kubectl get clusters.postgresql.cnpg.io --all-namespaces
```

The relational database (`dc-rdb`) is in `dc-system`; the event store (`dc-tsdb`) is in the
instance's own namespace.

You are looking for `Cluster in healthy state`. A cluster stuck in `Setting up
primary` has not recovered — most often the archive is unreachable, or
`--restore-rdb-from` / `--restore-tsdb-from` names a path that does not exist in the
bucket.
:::

:::caution Recover under the instance's own name
A recovery has to use the name the instance had. The relational database is named after
the instance, so the restore in step 1 brings it back under the *old* name, owned by
that instance's own login. A bootstrap run with `--restore-root-key` under a *different*
name looks into the store, finds no database of its own there, and — on a cluster that
holds no half-built instance of that name either — stops before writing anything:

```text
--restore-root-key recovers the key that opens instance "<new-name>"'s stored secrets, and
there is nothing here for it to open: the relational store holds no database for "<new-name>" ...
```

It refuses because the alternative is an *empty* instance sealed under a recovered key —
which looks like a successful recovery from the outside and holds none of the data you
came back for. The yellow `note: ... was escrowed for instance "<old-name>"` printed
earlier in the same run is the warning, not the refusal.

The way out is to run step 2 again under the original name — the one `dcctl secrets
escrow show` prints as `Instance:` for the artifact, and the one the recovered database
carries. The refusal comes *after* the relational restore has already run, so settle the
name before you start rather than during the incident.

The artifact does record the name it was written for, and that name is
authenticated — it cannot be edited without invalidating the file. Renaming an
instance is a migration, not a restore, and the platform does not do it today.
:::

## Verifying your escrow, before you need it {#verify}

An escrow that no longer matches the running key is indistinguishable from a good one
until the day it is used. Check it on an ordinary Tuesday:

```bash
dcctl secrets escrow verify ~/backups/my-instance-rootkey.escrow --instance my-instance
```

This compares the artifact's fingerprint against the key the instance is **actually
running**, needs no passphrase, and exits non-zero on a mismatch — so it composes into
a cron job or a CI gate. A mismatch means the instance has no usable escrow, most
often because it was re-bootstrapped after the file was written.

To see what an artifact is without opening it:

```bash
dcctl secrets escrow show ~/backups/my-instance-rootkey.escrow
```

:::caution What `verify` does not prove
It proves the artifact names the right key. It does not prove the artifact still
*opens* — that needs the passphrase. Rehearse a real recovery periodically; a
fingerprint check is a smoke alarm, not a fire drill.
:::

## Credentials, and the two commands that touch them

`dcctl bootstrap` **mints** every credential an instance has, and it does so because none of
them exists yet. That is why it refuses to run against an instance that is already live: there
is no ordering in which handing a running instance new credentials is safe. Rewriting the root
key makes every stored secret permanently unreadable. Rewriting the broker credentials is
recoverable but disruptive — the broker and the services are updated by different mechanisms
on different schedules, so fresh credentials open a window in which one side rejects the
other, and pods that start inside it fail to reach the broker at all.

`dcctl upgrade` is the command that acts on a live instance, and it **mints nothing**. It
reads back the secret-store root key, the broker's authority and logins (the callout issuer
seed and the service and system passwords), the database passwords (the shared owner, the
provisioner, the instance's own login and the event store's owner), the cross-service auth
secret — and, where monitoring and in-cluster backups are enabled, the Grafana admin password
and the object-store credentials — and keeps every one of them. A version change cannot
become a credential change.

### Finishing a bootstrap that failed partway {#resuming-a-bootstrap}

The refusal is keyed on the instance's **configuration document**, which is written near the
end of the run. Anything short of that is a half-built instance rather than a live one, and
running the bootstrap again is the supported way to finish it.

The broker is why that window has to stay open. It is configured several steps before the
instance itself is, and a bootstrap that fails in between leaves a running broker that no
later run could recognise from the cluster alone: it holds only a public key and two password
hashes, and none of those can be turned back into the credentials the services need. So the
broker's credentials are also recorded on the machine you run `dcctl` from, under
`~/.devicechain/instances/<instance>/`, before the broker is configured with them — and a later run
reuses them from there when there is no instance yet to ask. The file is readable only by you,
and `dcctl destroy` removes it with the rest of the instance's local state.

### The escrow is reconciled on every upgrade {#escrow-reconcile}

Checking an escrow needs **no passphrase**. The artifact records a fingerprint of the key it
protects, so matching it against the key the instance is running opens nothing — which is why
`dcctl upgrade` does it every time, without asking you for anything:

- artifact matches the running key → confirmed, left untouched;
- artifact does **not** match → warned about loudly, and named for what it most likely is: an
  escrow belonging to an earlier instance of the same name. Restoring from it would recover a
  cluster that cannot read its own secrets;
- no artifact → one is written, if you passed `--escrow-passphrase-file` (or set
  `DCCTL_ESCROW_PASSPHRASE`). This is how an instance first created with `--no-escrow` gains
  an escrow later. With no passphrase available it warns instead, and does not prompt: an
  upgrade runs unattended, and by this point it has already moved the instance.

None of those outcomes fails the upgrade. An escrow problem is about a future disaster and the
upgrade in front of it is about the running instance — and an operator who cannot upgrade will
work around the check rather than fix it.

## After `dcctl destroy` {#after-destroy}

`dcctl destroy` removes the instance — its Helm release, its NATS broker and event store,
its database and database login, its namespace — and its local state, but **not** the
escrow artifact, which lives outside
that directory by design, and which destroy names on its way out.

It never deletes the cluster, nor the prerequisites `dcctl install` put on it: the
relational database the other instances use, and the backup object store, stay where they
are.

Keep it for as long as you keep any backup of that instance's databases. It is the
only thing that can still read them. Delete it when those backups are gone, and not
before.
