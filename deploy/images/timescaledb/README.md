<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# TimescaleDB operand image

The PostgreSQL image the `tsdb` CloudNativePG cluster runs (ADR-020 A2.6, ADR-028).

```
ghcr.io/devicechain-io/postgresql-timescaledb:<pg-minor>-ts<timescale-version>
```

> **Nothing deploys this yet.** A2.6 builds, gates and publishes the image, and proves the
> migration chain reproduces the committed goldens on it. The `tsdb` store still runs
> `timescale/timescaledb:latest-pg16` as a StatefulSet until the A2.4 cutover moves it to a
> CloudNativePG `Cluster`.

## Why we build our own

Every alternative was measured, and every one shipped a database we would not choose:

| Candidate | Verdict |
| --- | --- |
| `timescale/timescaledb`, `timescaledb-ha` | **Cannot run under CNPG at all.** Patroni entrypoint, `PGDATA` at `/home/postgres/pgdata`, and CNPG's webhook forbids overriding `PGDATA` (`charts#414`, `cnpg#5500`). |
| `clevyr/cloudnativepg-timescale` | Committed to weekly; published tags sit at TimescaleDB 2.19.3 / PG 16.9. A green pipeline shipping old software. |
| `imusmanmalik/...-timescaledb-postgis` | Genuinely maintained, and the A2 spike ran a 3-instance cluster on it successfully — but measurement on the running cluster showed TimescaleDB **2.22.0**, below the 2.26.4 failover fix. Research had reported it as tracking 2.28.3; that is not true of the PG 16 tag. |
| Official CNPG extensions catalog | Ships the **Apache-2 edition**, where continuous aggregates do not exist. `measurement_rollups` and the whole of ADR-026 would silently vanish. |

So every community option would have shipped us into a known post-failover defect. Owning it is
about ten lines, and makes both pins ours.

## Layout

| File | What it is |
| --- | --- |
| `versions.conf` | The pins. Single source of truth — the Dockerfile, both CI workflows and `hack/migration-diff.sh` all read it. |
| `Dockerfile` | CNPG `standard` base + the pinned TimescaleDB packages. Asserts its own contents. |
| `build.sh` | Build for the local architecture from `versions.conf`. |
| `smoke.sh` / `smoke.sql` | The functional gate: starts a server and asserts the TSL features ADR-026 depends on. |
| `verify-compat.sh` | The rolling-update gate. `--self-test` proves it can fail. |
| `standalone.sh` | Runs the image as a plain throwaway server. **Test harness only.** |

## Everyday use

```bash
./build.sh                 # build dc-tsdb-operand:local
./smoke.sh                 # build, then run the functional gate
./verify-compat.sh --self-test
```

## Bumping TimescaleDB

Edit `versions.conf` and **move the old value into `TIMESCALEDB_COMPAT_VERSIONS` in the same
commit, keeping everything already there**:

```diff
-TIMESCALEDB_VERSION=2.28.3
-TIMESCALEDB_COMPAT_VERSIONS=
+TIMESCALEDB_VERSION=2.29.0
+TIMESCALEDB_COMPAT_VERSIONS=2.28.3
```

You do not have to remember this. `verify-compat.sh` reads the previously committed values out of
git and enforces a **superset** rule: this commit's `{version} ∪ {compat}` must cover the previous
commit's. A one-hop check would not be enough — `2.28.3` → `2.29.0 (compat 2.28.3)` →
`2.30.0 (compat 2.29.0)` keeps "the previous version" at every step while quietly losing `2.28.3`.

Retiring a version is therefore a deliberate act, never a side effect:

```bash
DC_COMPAT_ALLOW_DROP="2.28.3" ./verify-compat.sh
```

**Rebuilding without changing versions** — a base-image CVE rebake, or a Dockerfile change —
requires bumping `IMAGE_REVISION`, because the published tag includes it. Without that the tag
would be mutable: same tag, different bits, and with `imagePullPolicy: IfNotPresent` the nodes
holding a cached copy keep the old image while every new pod pulls the new one. One cluster, two
builds, no signal.

**Why it matters:** `timescaledb.so` is a stub that loads the versioned library matching each
database's catalog version. CNPG updates replicas before the primary — the correct order — but only
if the new image still ships the *old* versioned library. If it does not, the first replica onto the
new image dies with `could not access file "$libdir/timescaledb-2.X.Y"`, mid-rollout, on a cluster
that was healthy a minute earlier. Timescale's own official image did exactly this in December 2025
(`timescale#9072`).

Drop a version from the compat list only once no cluster can still be running it. Note that CNPG
never runs `ALTER EXTENSION timescaledb UPDATE` for you, so "we upgraded" stays false until a human
has run it per database, in a fresh session.

## Things that bit us, recorded so they do not bite twice

**The loader package is pinned, and it is the least obvious line in the Dockerfile.** The `.control`
file that declares `default_version` is owned by `timescaledb-2-loader-postgresql-N`, not by the
versioned packages (`dpkg -S` confirms). The versioned packages depend on it only as `>= <version>`,
so an unpinned install takes the newest loader in the repository. That is fine today and breaks the
day Timescale publishes the next release: the rebuilt image would declare `default_version = 2.29.0`
while carrying only 2.28.3's library, and `CREATE EXTENSION timescaledb` would look for a library
that is not there — with nothing in this directory having changed.

**A CNPG operand image is not a `postgres` image.** It has no `ENTRYPOINT`, no
`docker-entrypoint.sh`, no `/docker-entrypoint-initdb.d`, and its `CMD` is `bash`. In production
that is correct: CNPG's instance manager runs `initdb`. But `docker run -e POSTGRES_PASSWORD=...`
against it starts a shell that exits immediately — which is how a harness ends up green against a
database that never started. `standalone.sh` exists for exactly this and is used by both `smoke.sh`
and `hack/migration-diff.sh`.

**The base image sets no locale, so `initdb` picks `SQL_ASCII`.** This does not present as an
encoding problem. It presents as every query failing with `simple protocol queries must be run with
client_encoding=UTF8`, because pgx refuses the simple protocol against a non-UTF8 server — so the
migration chain dies on its first statement and looks like a broken migration.
⇒ **The A2.4 `Cluster` must set `spec.bootstrap.initdb.encoding` explicitly** rather than letting it
be inferred from an environment that does not define one.

**DeviceChain depends on `template1` carrying the extension, and never says so.** Services
self-provision their own databases at startup (decision D2), so they always land on a database
created *after* the cluster was bootstrapped. The upstream image puts `timescaledb` in `template1`,
so those databases inherit it. Without that, event-management's migrations fail on their first
hypertable with `function create_hypertable(unknown, unknown) does not exist` — which reads like a
broken migration rather than a missing bootstrap step.
⇒ **The A2.4 `Cluster` must set `spec.bootstrap.initdb.postInitTemplateSQL`.**

**The image ships a `policy_telemetry` job that phones home to Timescale.** `standalone.sh` sets
`timescaledb.telemetry_level = off` for throwaway containers; the A2.4 `Cluster` should set it in
`spec.postgresql.parameters` for real ones.

**`shared_preload_libraries` must be set on the `Cluster` itself, never inherited.** CNPG's recovery
bootstrap drops it (`cnpg#10840`), which is cosmetic for most extensions and fatal for this one:
TimescaleDB registers a custom WAL resource manager, so a restore instance dies during WAL replay
when the library was not preloaded at postmaster start.

## Known limits of the gates

Stated rather than left to be discovered:

- **arm64 is built but never executed.** The publish job cross-builds it; nothing runs it. The
  smoke gate is amd64 only, on the published image pulled back by digest.
- **The Debian packaging revision is not pinned.** `versions.conf` pins the upstream version
  (`2.28.3`) and the build resolves the newest Debian revision of it (`~debian12-1710` today). A
  Timescale repackage would change the bits without changing this directory — bump `IMAGE_REVISION`
  when that is known to have happened.
- **`smoke.sql` §7 tests less than it appears to.** It shows that template inheritance carries the
  extension on this image; it cannot verify that the A2.4 `Cluster` sets `postInitTemplateSQL`,
  which is a property of a manifest and needs its own check there.
- **The compatibility loop is inert while `TIMESCALEDB_COMPAT_VERSIONS` is empty.** It becomes live
  at the first bump, and says so when it skips.

## Licensing

The image contains **TimescaleDB Community Edition, which is TSL-licensed, not Apache-2.0**. The
sources in this directory are DeviceChain's own Apache-2.0 files and contain no Timescale code — the
obligation attaches to the published artifact. See [NOTICE](../../../NOTICE) for the full statement,
including the TSL's principal restriction: DeviceChain must never expose the Timescale instance to
tenants as a general-purpose database service.
