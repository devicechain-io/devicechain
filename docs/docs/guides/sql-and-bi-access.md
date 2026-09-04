---
sidebar_position: 8
title: SQL and BI Access
---

# SQL and BI Access

DeviceChain stores telemetry in **TimescaleDB**, which is PostgreSQL. That is not an
implementation detail you have to work around — it is the integration. Any tool that speaks
Postgres or JDBC can query your telemetry directly: Metabase, Grafana, Power BI through the
PostgreSQL or ODBC driver, `psql`, a notebook, your own reporting job. There is no export step, no
second data store to keep in sync, and no separate product to license.

What this guide sets up is the part that does not come for free: a **read role that is safe to hand
out**. Pointing a BI tool at the platform's own database credentials would give it every tenant's
data and write access to the operational store. The analytics surface exists so you never have to.

:::note Status
Available. The surface is created on every install; it has no readers until you declare one.
:::

## What a reader can see

Readers connect to the event store and query the **`analytics` schema**. It holds one view per
event relation, plus the pre-aggregated measurement rollup — which is usually the one you actually
want, because it is already bucketed and is cheap to scan over long ranges.

| View | What it holds |
| --- | --- |
| `analytics.events` | The base event envelope: device, type, times, source |
| `analytics.measurement_events` | Named numeric readings, with unit and data type |
| `analytics.measurement_rollups` | Per-minute sum / min / max / count per device and metric |
| `analytics.location_events` | Positions, with elevation, accuracy, speed and heading |
| `analytics.alert_events` | Device-reported alerts |
| `analytics.state_change_events` | The connect/disconnect timeline |
| `analytics.event_anchors` | The relationship anchors stamped on each event at write time |

Every one of them is filtered to **the reader's own tenant**, automatically. There is no tenant
column to remember to filter on, no view to pick per tenant, and nothing a query can do to widen
the result — see [How the boundary is enforced](#how-the-boundary-is-enforced) below.

The rollup keeps the current, still-filling bucket live, so a dashboard reading it is not blind up
to the last refresh.

## Declaring a reader

A reader is a PostgreSQL login role named **`analytics_<tenant id>`**. The tenant is taken from the
role name, so `analytics_acme` reads the tenant `acme` and nothing else.

:::danger The role name is the tenant, and it is the only place the tenant is written
Name a reader `analytics_acme` and it reads `acme`. Name it `analytics_acmecorp` when your tenant
id is `acme` and it reads nothing at all — every query returns zero rows, with no error. There is
no second place to correct the mistake, and no message that names it. Check the tenant id in the
console before you create the role.

**The tenant id must be 53 characters or shorter.** PostgreSQL caps a role name at 63 bytes and
*truncates* a longer one instead of rejecting it, which would silently produce a reader for a
different tenant. Your deployment refuses a name that would be truncated, so this is a rejected
apply rather than a surprise — but it is why the limit exists.
:::

**1. Create a Kubernetes Secret with the password.** The platform never mints or stores this
credential; you own it, and the database is reconciled to match it.

```bash
kubectl create secret generic analytics-acme-credentials \
  --namespace dc-system \
  --type kubernetes.io/basic-auth \
  --from-literal=username=analytics_acme \
  --from-literal=password="$(openssl rand -base64 24)"
```

**2. Declare the role in your deployment variables**, with a connection limit:

```hcl
timescale_analytics_readers = [
  {
    name             = "analytics_acme"
    connection_limit = 5
    password_secret  = "analytics-acme-credentials"
  },
]
```

Apply. The role appears, joins the reader group, and can connect. Nothing needs restarting.

To rotate the password, change it in the Secret; to revoke access, remove the entry and apply.

## Connecting a BI tool

Point the tool at the event store as an ordinary PostgreSQL database:

| Setting | Value |
| --- | --- |
| Host | the event store service (`dc-timescaledb-single` in-cluster) |
| Port | `5432` |
| Database | your **instance id** |
| Schema | `analytics` |
| User | `analytics_<tenant id>` |
| Password | the one you put in the Secret |

The database name is the instance id rather than a fixed name, because one server hosts one
database per instance.

From outside the cluster, expose the store the way you expose any other database — a port-forward
for a one-off, or a proper ingress with TLS for a standing connection. For a quick check:

```bash
kubectl port-forward -n dc-system svc/dc-timescaledb-single 5432:5432
psql "postgres://analytics_acme@localhost:5432/<instance-id>" \
  -c "SELECT device_token, name, bucket, sum_value / count_value AS avg
      FROM analytics.measurement_rollups
      WHERE bucket > now() - interval '1 hour'
      ORDER BY bucket DESC LIMIT 20;"
```

In Grafana, add a **PostgreSQL** data source with those settings. In Metabase, add a **PostgreSQL**
database. In Power BI, use **Get Data → PostgreSQL database**. None of them needs a DeviceChain
plugin.

## How the boundary is enforced

Worth understanding, because it decides what you can safely do with a reader's credentials.

**The tenant filter is compiled into the views, and it keys on the authenticated role.** Each view
carries `WHERE tenant_id = <the tenant of the authenticated role>`. That identity is the role the
session logged in as — PostgreSQL's `session_user` — and a reader cannot change it: `SET ROLE`
moves the *current* role but never the session's, and the one statement that would (`SET SESSION
AUTHORIZATION`) is refused to anyone who is not a superuser. There is no session setting to
override either. A role whose name carries no recognised tenant resolves to nothing and reads zero
rows — the failure direction is always "sees nothing", never "sees everything".

**A reader holds no privilege on the underlying tables.** It cannot reach the raw hypertables by
name at all, which is also why it is read-only: it has `SELECT` on seven views and nothing else, so
there is no write privilege to exercise. That is a grant, not a setting — nothing a client can turn
off.

**Both layers are re-established every time the event store starts.** The views are rebuilt and the
privileges re-converged on each boot, so neither a privilege granted by hand during an investigation
nor a view edited during one quietly outlives it. A restart is a repair.

**Connections are capped per role, and the cap binds.** `connection_limit` is enforced at
authentication: past it, the connection is refused. This is what stops an analytics consumer from
exhausting the platform's own connection pool — a failure that would otherwise be silent, because
pools open lazily and the database keeps reporting healthy while the application can no longer
reach it. Your deployment refuses to render a reader with no limit, and refuses a set of readers
whose limits do not fit the server.

:::caution The cap bounds connections, not load
A connection limit stops an analytics consumer from taking *connections* the platform needs. It
does not stop the queries on those connections from competing for CPU, disk and PostgreSQL's shared
parallel-worker pool — the same pool compression, retention and rollup refresh draw on. So
"analytics cannot interfere with ingest" is **partially** true: the connection-exhaustion path is
closed and the resource-contention path is not.

If that matters for your workload, run BI against a **read replica**. A replicated deployment
already exposes a read-only service alongside the primary; pointing readers at it puts the
contention on a node whose only job is serving them, and PostgreSQL resolves a conflict there by
cancelling the long analytics query rather than by delaying replay.
:::

:::caution What is *not* capped: query cost
There is no query-time limit on a reader, and adding one would not be the control it looks like.
PostgreSQL's `statement_timeout` can be given to a role as a **default, not a ceiling** — any
client raises it for its own session with a single statement, and there is no way to stop that.
Setting one is still worth doing as protection against an accidentally expensive dashboard, and it
is a superuser operation on the database rather than something the platform can do for you:

```sql
ALTER ROLE analytics_acme SET statement_timeout = '60s';
```

The connection limit is the control that actually binds. Size it, and size the store, on the
assumption that every one of those connections may be running a long query.
:::

## Practical notes

- **Query the rollup, not the raw table, for anything over a long range.** It is a continuous
  aggregate: the work is already done, and scanning a month of it is cheap where scanning a month
  of raw measurements is not.
- **Give each consumer its own role.** Two tools sharing one role share its connection limit, and
  you cannot revoke one without revoking both.
- **A reader survives a schema change but does not automatically gain from one.** The views expose
  a fixed set of columns; a column added to the platform later appears on the analytics surface
  when it is deliberately added there, not before.
- **A reader sees some metadata beyond its own tenant.** PostgreSQL's catalogs are readable by any
  connected role, so a reader can list the other role names on the server (and therefore which
  tenants have BI access), see when those sessions are active, and see internal table and chunk
  names. It cannot read a row of any of it. If that matters, give each customer its own instance.
- **Deleting a tenant does not delete its reader role.** Remove the role from your deployment
  variables as part of decommissioning it. The telemetry is erased, so the role reads nothing — but
  a login that still exists is a login somebody still holds, **and a tenant id can be reused, in
  which case that role would read its successor's data.** Removing the role is the step that closes
  both.
