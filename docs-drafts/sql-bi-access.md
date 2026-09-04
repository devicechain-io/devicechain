---
title: SQL / BI Read Access
status: draft
audience: engineering reference — see "Publishing this" at the end
adrs: [ADR-073, ADR-026, ADR-020, ADR-023, ADR-015, ADR-077]
---

# SQL / BI Read Access

Telemetry lives in TimescaleDB, so anything that speaks Postgres or JDBC could always query it.
The work was never the capability; it was producing **a role that is safe to hand a BI tool**.

This document is the traversal. No single file holds it: the migration, the boot reconciler, the
CloudNativePG chart, the golden differ and the published guide each document themselves, and none
of them documents the path from "an operator wants Metabase on this" to "that connection cannot
see another tenant". Every claim below names the file it came from.

## The shape, end to end

```
operator                             database cluster                event-management
────────                             ────────────────                ────────────────
kubectl create secret  ─────────────▶ CNPG managed role
  (kubernetes.io/basic-auth)            analytics_<tenant>
                                        LOGIN, CONNECTION LIMIT n,
timescale_analytics_readers ────────▶   IN ROLE analytics_reader
  (deploy/opentofu/variables.tf)              │
                                              │        migration 20260904000000
                                              │        ├─ CREATE SCHEMA analytics
                                              │        ├─ analytics.reader_tenant()
                                              │        └─ 7 tenant-filtered views
                                              │
                                              │        every boot: ReconcileAnalyticsSurface
                                              ├────────┤  ├─ rebuild the views
                                              │        │  ├─ REVOKE on "event-management"
                                              │        │  └─ GRANT on analytics.*
BI tool ──psql/JDBC──▶ analytics.measurement_rollups
                          WHERE tenant_id = analytics.reader_tenant()
                                              └─ derived from current_user
```

Two arrows carry the whole design and neither is obvious.

**`session_user`, not `current_user`, and not a session setting.** A custom GUC (`SET
analytics.tenant = …`) is USERSET: the reader sets it to any tenant it likes. `current_user` looks
safe and is not — 🔴 **this design hands every reader membership in `analytics_reader`, so `SET ROLE`
succeeds**, and the group role's own name matches the reader prefix, so it resolved to the tenant
`reader`. Measured, before the fix:

```
analytics_acme=> SET ROLE analytics_reader;
analytics_acme=> SELECT tenant_id, count(*) FROM analytics.measurement_events GROUP BY 1;
 reader | 1
```

`session_user` is the authenticated identity: `SET ROLE` cannot move it and `SET SESSION
AUTHORIZATION` is refused to a non-superuser. The group role is ALSO excluded by name, as a second
layer for the day somebody gives it LOGIN.
[`backend/services/event-management/model/analytics.go`]

**Every boot, not once.** The identity function is re-created unconditionally (it takes no relation
lock, so there is no readiness cost); the views are checked and rebuilt only if wrong (`CREATE OR
REPLACE VIEW` takes ACCESS EXCLUSIVE, so an unconditional rebuild would let a long BI query delay
readiness); and the grants — including PUBLIC's — are converged unconditionally.
[`backend/services/event-management/main.go`]

## Why not row-level security

This is the part worth keeping, because RLS is what everybody reaches for first and it cannot be
built here. Measured on BOTH supported majors — PostgreSQL 16 (the image the goldens are captured
against, TimescaleDB 2.28.3) and PostgreSQL 17 (the deployed operand) — both directions refuse:

```
ALTER TABLE <hypertable> SET (timescaledb.compress, …)   -- on an RLS table
  ERROR: columnstore cannot be used on table with row security

ALTER TABLE <hypertable> ENABLE ROW LEVEL SECURITY        -- on a compressed one
  ERROR: operation not supported on hypertables that have columnstore enabled
```

Compression is not optional: `ApplyDataLifecyclePolicies` enables it on all six hypertables on
every boot, and `applyOne` logs a failed compression statement and continues by design. So RLS on a
fresh install would apply first and then **silently disable compression forever** — every health
check green, the telemetry store growing without bound, and nothing pointing at the migration that
did it. [`backend/services/event-management/model/lifecycle.go`]

A second, independent reason it would not have been enough: `measurement_rollups` is a continuous
aggregate, i.e. a **view owned by the platform**, and PostgreSQL checks a view's underlying reads as
the view's OWNER. No policy on `measurement_events` could ever have constrained a read arriving
through it. The rollup is also the relation a BI tool actually wants. Its filter has to live in a
view body, which then becomes the uniform mechanism for all seven rather than a special case.

## What actually enforces the boundary

| Layer | Mechanism | Binds? |
| --- | --- | --- |
| Tenant isolation | `WHERE tenant_id = analytics.reader_tenant()` compiled into each view, keyed on `current_user` | yes |
| Qual ordering | `security_barrier` on every view | yes — see below |
| Reachability of the raw tables | the reader holds **no** privilege on `"event-management"`, not even USAGE; re-revoked every boot, **from PUBLIC as well as from every named role** | yes |
| Read-only | only `SELECT` is granted; there is no write privilege to exercise | yes |
| Connection cap | `CONNECTION LIMIT` on the role, refused at authentication | yes |
| Query cost | `statement_timeout` — USERSET, so a reader raises it in one statement | **no**, and the guide says so |

`security_barrier` is not decorative, and the measurement is worth recording because the default
plan hides it. On this schema the tenant predicate normally becomes an **index condition**, which
applies it before any filter — so a naive leak test passes and is really testing the index. The
planner GUCs are USERSET: a reader runs `SET enable_indexscan = off` and gets a sequential scan,
where a cheap non-leakproof predicate of its own is evaluated first. Measured through a barrierless
clone of the same view, a reader's predicate was handed another tenant's `tenant_id`; through the
real view it was not. [`backend/services/event-management/model/analytics_integration_test.go`,
`TestIntegrationAnalyticsViewsAreSecurityBarriers`]

## Why the platform does not create the role

The application role is an unprivileged owner with `CREATEDB` and `pg_monitor` and nothing else. It
is not given `CREATEROLE`, because a role that can mint logins is a role a compromised pod can mint
logins with, and the platform needs that exactly once, at install. So:

- the migration creates **no roles and grants nothing** — it is pure owner DDL
  [`backend/services/event-management/model/migration_analytics_surface.go`];
- the roles are declared on the Cluster, and CloudNativePG keeps each password in step with a
  Secret the operator owns, so the credential never passes through OpenTofu state, dcctl, or any
  DeviceChain process
  [`deploy/opentofu/modules/cnpg-cluster/chart/templates/cluster.yaml`];
- the grants are converged at boot, which also makes the cluster's asynchronous role creation an
  ordering that heals itself rather than a race a migration would record as done.

🔴 That last point has a sharp edge worth stating plainly for the next maintainer: **every gate in
this repo migrates as a superuser** — `hack/migration-diff.sh`, the replay pass, and the integration
harness all connect as `postgres`. A migration needing one more privilege than production's role has
passes all of them and crash-loops on the first real boot.
`TestIntegrationChainRunsAsTheLeastPrivilegeRole` is what turns "this migration creates no roles"
into a checked property.

## What the golden differ can and cannot see

| | Visible to `hack/migration-diff.sh verify`? |
| --- | --- |
| the `analytics` schema, function and views | **yes**, but only because `area.extraSchemas` was added — the dump is scoped `--schema <area>`, so a schema built under another name was invisible to the only gate that exercises migrations |
| `SET search_path` on the function | **no** — `normalizeDump` strips `SET ` lines (they are pg_dump's own preamble), so the pin is absent from the committed golden. Look at `golden/event-management.sql`: the function is dumped as `LANGUAGE sql STABLE` straight into `AS $$`, with no `SET` clause between them. A re-creation that drops the pin moves nothing. `pg_proc.proconfig` is checked at boot for exactly this reason |
| roles | no — `pg_dump` emits none; that is `pg_dumpall --roles-only` |
| grants | no — the dump passes `--no-privileges`, so no ACL can move a golden |

The two "no" rows are why the boundary is covered by tests that connect **as a reader**, never by a
schema diff. [`backend/tools/migrationdiff/registry.go`, `backend/tools/migrationdiff/main.go`]

## The connection budget

Analytics sessions come out of the same `max_connections` as event-management's own pool, and
exhausting it is silent: pools open lazily, the Cluster keeps reporting Ready, and the exporter's
metrics vanish along with the application's. So the chart refuses, at render time, both a login role
with no `connectionLimit` (PostgreSQL's `-1` is unlimited, and it is also what an omitted field
renders as) and a set of roles whose limits plus the platform's reservation exceed
`max_connections` less the superuser reserve. The event store's reservation defaults to 40 — one
pool of 20, doubled for a RollingUpdate.
[`deploy/opentofu/variables.tf`, `timescale_analytics_reserved_connections`]

Both refusals are exercised by name in `hack/check-cnpg-chart-schema.sh`, whose coverage control
requires every `fail` in the templates to have been tripped by a case.

## Three holes review found, all in the perimeter

Recorded because each was invisible to a different gate, and because two of them were closed by
tests that already existed and were asking the wrong question.

1. **`SET ROLE analytics_reader`** — see above. The comment asserting it was impossible was written
   by the same change that granted the membership making it possible. A test pinned `current_user`
   and passed.
2. **The identity function was built once.** The view reconciler existed precisely because "the one
   thing enforcing isolation was the one thing built once" — and that argument then applied, one
   level down, to `reader_tenant()` itself, which no check re-asserted. Three re-creations leak and
   pass a view-only check: a replaced body, a body with the `search_path` pin dropped, and a
   `SECURITY DEFINER` one. 🔴 The pin is doubly invisible: `normalizeDump` strips `SET ` lines, so it
   never reached the golden either. The check now reads `pg_proc.prosrc/proconfig/prosecdef/
   provolatile`, and the function is rebuilt every boot regardless.
3. **`WHERE … reader_tenant() OR true`** satisfied a `strings.Contains` check while showing every
   tenant. The comparison is now against the exact predicate as `pg_get_viewdef` reconstructs it,
   anchored at the END of the definition so nothing can be appended. Measured identical on both
   majors: `WHERE ((tenant_id)::text = analytics.reader_tenant());`.

Plus two smaller ones: **PUBLIC is not a role**, so revoking from everything in `pg_roles` missed
it entirely and a `GRANT … TO PUBLIC` on the area schema survived every boot; and the
least-privilege test **ran the reconciler with zero roles present**, so the GRANT/REVOKE path it
claimed to measure as the unprivileged owner never executed.

## The two governance rulings

**"An analytics consumer cannot starve ingest" is PARTIALLY met.** The connection cap binds and
closes the connection-exhaustion path. It does not bound CPU, IO, or the shared parallel-worker
pool that compression, retention and cagg refresh draw on — and `max_parallel_workers_per_gather`
is USERSET, so a reader can raise its own. Documented as partial rather than claimed. The cheap
improvement, not taken here: CloudNativePG already creates a `-ro` service, so under `--ha` BI can
point at a standby, where `max_standby_streaming_delay` cancels the conflicting long query — the
reader loses rather than ingest.

**BI access is operator-declared, not tier-governed.** Not merely "no consumer for the value": the
application role holds no `CREATEROLE`, so it **cannot** `ALTER ROLE … CONNECTION LIMIT` at all. A
tier cascade could resolve a number and would have nothing able to act on it. Recorded here so this
does not become two governance stories by accident.

## Loose ends

- **Deleting a tenant does not delete its reader role, and a token can be REUSED.** The purge
  erases the telemetry, so the role reads nothing — until a successor tenant is created at the same
  token, which the token-release step permits. The predecessor's `analytics_<token>` login then
  reads the successor's data. The docs now say so; nothing enforces it and the platform cannot,
  because it can neither drop the role nor see that one exists. The real answer is a
  platform-owned `analytics.readers(tenant_id)` allow-list that `reader_tenant()` consults and the
  purge clears, which is filed rather than built here.
- **The reader can enumerate chunk names.** `_timescaledb_internal` carries `USAGE` for PUBLIC, so
  a reader can see relation names in the catalog. It cannot read any of them — measured — but the
  names are visible.
- **No `dcctl` surface.** Provisioning is declarative on purpose; there is nothing for a CLI to do
  that an apply does not already do.

## Publishing this

The user-facing contract lives at `docs/docs/guides/sql-and-bi-access.md` and its Spanish mirror.
This file is the mechanism behind it and is not published: it names ADRs, internal file paths and
measurements that belong to maintainers.
