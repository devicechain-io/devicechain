---
sidebar_position: 8
title: SQL and BI Access
---

# SQL and BI Access

DeviceChain stores telemetry in **TimescaleDB**, which is PostgreSQL. You do not have to work around
that — it is the integration. Any tool that speaks Postgres or JDBC can query your telemetry
directly: Metabase, Grafana, Power BI through the PostgreSQL or ODBC driver, `psql`, a notebook, or
your own reporting job. There is no export step, no second data store to keep in sync, and no
separate product to license.

This guide sets up the part that does not come for free: a **read role that is safe to hand out**.
The platform's own database credentials would give a BI tool every tenant's data and write access
to the operational store. The analytics surface exists so you never have to hand those out.

:::note Status
Available. The surface is created on every install; it has no readers until you declare one.
:::

## Analytics views {#what-a-reader-can-see}

Readers connect to the event store and query the **`analytics` schema**. It holds one view per event
relation, plus the pre-aggregated measurement rollup. The rollup is usually the one you want: it is
already bucketed, so it is cheap to scan over long ranges.

| View | What it holds |
| --- | --- |
| `analytics.events` | The base event envelope: device, type, times, source |
| `analytics.measurement_events` | Named numeric readings, with unit and data type |
| `analytics.measurement_rollups` | Per-minute sum / min / max / count per device and metric |
| `analytics.location_events` | Positions — latitude, longitude, elevation, accuracy, speed, heading. Requires the position grant; see [Position is a separate grant](#position-is-a-separate-grant) |
| `analytics.alert_events` | Device-reported alerts |
| `analytics.state_change_events` | The connect/disconnect timeline |
| `analytics.event_anchors` | The relationship anchors stamped on each event at write time |

Every view is filtered to **the reader's own tenant**, automatically. There is no tenant column to
remember to filter on, no view to pick per tenant, and nothing a query can do to widen the result.
See [How the boundary is enforced](#how-the-boundary-is-enforced).

The rollup keeps the current, still-filling bucket live, so a dashboard reading it is not blind up
to the last refresh.

## Declare a reader {#declaring-a-reader}

A reader is a PostgreSQL login role named **`analytics_<tenant id>`**. The tenant is taken from the
role name, so `analytics_acme` reads the tenant `acme` and nothing else.

:::danger The role name is the tenant, and it is the only place the tenant is written
Name a reader `analytics_acmecorp` when your tenant id is `acme` and it reads nothing at all: every
query returns zero rows, with no error. There is no second place to correct the mistake, and no
message that names it. Check the tenant id in the console before you create the role. The tenant id
must be 53 characters or shorter; see [Name limits](#name-limits).
:::

### Name limits {#name-limits}

The tenant id must be **53 characters or shorter**. PostgreSQL caps a role name at 63 bytes and
*truncates* a longer one instead of rejecting it, which would silently produce a reader for a
different tenant. Your deployment refuses a name that would be truncated, so you get a rejected
apply rather than a surprise.

### Steps {#steps}

1. **Create a Kubernetes Secret with the password.** The platform never mints or stores this
   credential; you own it, and the database is reconciled to match it. The Secret goes in the
   instance's own namespace — `dci-` followed by the instance id — beside the event store that
   reads it. Label it `cnpg.io/reload` (see [The reload label](#the-reload-label)):

   ```bash
   kubectl create secret generic analytics-acme-credentials \
     --namespace dci-<instance-id> \
     --type kubernetes.io/basic-auth \
     --from-literal=username=analytics_acme \
     --from-literal=password="$(openssl rand -base64 24)"

   kubectl label secret analytics-acme-credentials \
     --namespace dci-<instance-id> cnpg.io/reload=true
   ```

2. **Declare the role in your deployment variables**, with a connection limit:

   ```hcl
   timescale_analytics_readers = [
     {
       name             = "analytics_acme"
       connection_limit = 5
       password_secret  = "analytics-acme-credentials"
     },
   ]
   ```

3. **Apply.** The role appears, joins the reader group, and can connect. Nothing needs restarting.

:::warning The label is what makes rotation work
Without `cnpg.io/reload`, later changes to the Secret are not noticed promptly, and rotation does not
happen within a predictable time. See [The reload label](#the-reload-label).
:::

### The reload label {#the-reload-label}

Without `cnpg.io/reload`, the database still picks the password up when the role is first created.
Later **changes** to the Secret, though, are not noticed promptly. The label asks the database
operator to watch the Secret for updates, and the rotation step below needs it to take effect within
a predictable time.

Two details catch people out:

- The label's *presence* is what counts, so any value works.
- The `username` in the Secret must match the role name **exactly**, with no trailing newline.

When the database operator cannot reconcile a role, it names the role and the cause under the
database Cluster's `status.managedRolesStatus` — not in DeviceChain's own logs. Look there before
assuming a bad password.

If you created the Secret before the labelling step above was documented, add the label now. Until
you do, a password change may sit unapplied for an unpredictable time.

### Rotate or revoke {#rotate-or-revoke}

To rotate the password, change it in the Secret. The database is reconciled to match, with no
restart.

To revoke access:

1. Remove the entry from `timescale_analytics_readers`, and apply.
2. **Drop the role as a superuser:**

   ```sql
   DROP ROLE analytics_acme;
   ```

Removing the entry only stops your deployment declaring the role. The database operator leaves a
role it no longer manages in place, with its password and its reader-group membership intact.
Dropping the role first does not work either: while it is still declared, the operator recreates it.

## Position is a separate grant {#position-is-a-separate-grant}

A reader declared as above reads telemetry, alerts, the connect/disconnect timeline and the event
envelopes — **but not device positions**. Latitude, longitude, elevation, accuracy, speed and
heading are behind a second grant, which you turn on per reader:

```hcl
timescale_analytics_readers = [
  {
    name             = "analytics_acme"
    connection_limit = 5
    password_secret  = "analytics-acme-credentials"
    reads_location   = true
  },
]
```

Apply, and the reader can query `analytics.location_events`. Without it, that one view returns
`permission denied` and every other view is unaffected.

This is the platform's own line, not extra caution applied to BI. Everywhere in DeviceChain, reading
where a device **is** is a separate permission from reading what it **measures**. A vehicle's or a
person's track is a different kind of fact from a temperature series, so position is deliberately
absent from the read-only baseline and is only ever held by a deliberate grant.

A SQL session cannot be asked what permissions it holds: it authenticates as a role and carries
nothing else. So the permission is expressed the only way a database can express it — as a grant,
held by a second group role that the reader joins when you say so.

An ordinary reader keeps the **envelope**. `analytics.events` carries every event, including location
ones, so the reader can still see that a location event occurred, from which device and when. It
cannot see where.

:::tip Which readers need it
Fleet, logistics, field-service and asset-tracking dashboards do. A metrics or alerting dashboard
usually does not, and a reader without position is one fewer credential whose loss discloses
somebody's movements. Turn it on where the dashboard genuinely plots a map or computes a distance.
:::

:::note Upgrading an existing install
Readers declared before this split existed had position. On the first restart of `event-management`
after upgrading, it takes that grant back from every reader not marked `reads_location = true`, so a
dashboard that plots positions returns `permission denied` until you set it. This is deliberate: the
grant is re-derived from your declaration every time `event-management` starts, not accumulated.
:::

## Connecting a BI tool {#connecting-a-bi-tool}

Point the tool at the event store as an ordinary PostgreSQL database:

| Setting | Value |
| --- | --- |
| Host | the event store service (`dc-timescaledb-single` in-cluster) |
| Port | `5432` |
| Database | your **instance id** |
| Schema | `analytics` |
| User | `analytics_<tenant id>` |
| Password | the one you put in the Secret |

The database name is the instance id rather than a fixed name, because one server hosts one database
per instance.

From outside the cluster, expose the store the way you expose any other database: a port-forward for
a one-off, or a proper ingress with TLS for a standing connection. For a quick check:

```bash
kubectl port-forward -n dci-<instance-id> svc/dc-timescaledb-single 5432:5432
psql "postgres://analytics_acme@localhost:5432/<instance-id>" \
  -c "SELECT device_token, name, bucket, sum_value / count_value AS avg
      FROM analytics.measurement_rollups
      WHERE bucket > now() - interval '1 hour'
      ORDER BY bucket DESC LIMIT 20;"
```

None of these tools needs a DeviceChain plugin:

- **Grafana:** add a **PostgreSQL** data source with those settings.
- **Metabase:** add a **PostgreSQL** database.
- **Power BI:** use **Get Data → PostgreSQL database**.

## How the boundary is enforced {#how-the-boundary-is-enforced}

This section decides what you can safely do with a reader's credentials.

### Tenant filter {#tenant-filter}

**The tenant filter is compiled into the views, and it keys on the authenticated role.** Each view
carries `WHERE tenant_id = <the tenant of the authenticated role>`. That identity is the role the
session logged in as — PostgreSQL's `session_user` — and a reader cannot change it:

- `SET ROLE` moves the *current* role, never the session's.
- `SET SESSION AUTHORIZATION`, the one statement that would, is refused to anyone who is not a
  superuser.
- There is no session setting to override it.

A role whose name carries no recognised tenant resolves to nothing and reads zero rows. The failure
is always "sees nothing", never "sees everything".

### No table privileges {#no-table-privileges}

**A reader holds no privilege on the underlying tables.** It cannot reach the raw hypertables by name
at all. That is also why it is read-only: it has `SELECT` on the views it was granted and nothing
else, so there is no write privilege to exercise. This is a grant, not a setting, so no client can
turn it off.

The same mechanism separates position. A reader without `reads_location` holds no privilege on
`analytics.location_events`, so the coordinates are unreachable rather than filtered.

### Repair on every start {#repair-on-every-start}

**Both layers are re-established every time the `event-management` service starts.** The service does
this, not the database, so restarting the database alone repairs nothing. On each boot,
`event-management`:

- rebuilds the function that resolves a session's tenant;
- verifies every view, and rebuilds it if it is missing, exposes the wrong columns, has lost its
  tenant predicate or is no longer a security barrier;
- re-converges the privileges.

So neither a privilege granted by hand during an investigation nor a view edited during one quietly
outlives it. A restart of `event-management` is a repair.

That covers the position grant in every direction it can be widened. A `GRANT` on
`analytics.location_events` made to a reader by name, to the general reader group, or to `PUBLIC` is
taken back on the service's next boot. What a reader holds is derived from your declaration each
time, never accumulated. That is also why removing `reads_location` genuinely removes access rather
than leaving the last grant in place.

### Connection cap {#connection-cap}

**Connections are capped per role, and the cap binds.** `connection_limit` is enforced at
authentication: past it, the connection is refused. This stops an analytics consumer from exhausting
the platform's own connection pool. That failure would otherwise be silent, because pools open lazily
and the database keeps reporting healthy while the application can no longer reach it. Your
deployment refuses to render a reader with no limit, and refuses a set of readers whose limits do not
fit the server.

:::warning The cap bounds connections, not load
The limit stops analytics from taking *connections* the platform needs. It does not stop queries on
those connections competing for CPU, disk and PostgreSQL's shared parallel-worker pool. So
"analytics cannot interfere with ingest" is **partially** true. See
[Load and read replicas](#load-and-read-replicas).
:::

### Load and read replicas {#load-and-read-replicas}

Queries on a reader's connections compete for CPU, disk and PostgreSQL's shared parallel-worker pool —
the same pool compression, retention and rollup refresh draw on. The connection-exhaustion path is
closed; the resource-contention path is not.

If that matters for your workload, run BI against a **read replica**. A replicated deployment already
exposes a read-only service alongside the primary. Pointing readers at it puts the contention on a
node whose only job is serving them.

On the replica, PostgreSQL resolves a conflict by holding replay back for a bounded time, then
cancelling the long analytics query. The bound is 30 seconds by default — its
`max_standby_streaming_delay`, which the deployment leaves at that default — so the replica never
falls behind indefinitely.

### Query cost is not capped {#query-cost-is-not-capped}

There is no query-time limit on a reader, and adding one would not be the control it looks like.
PostgreSQL's `statement_timeout` can be given to a role as a **default, not a ceiling**: any client
raises it for its own session with a single statement, and there is no way to stop that.

Setting one is still worth doing, as protection against an accidentally expensive dashboard. It is a
superuser operation on the database, not something the platform can do for you:

```sql
ALTER ROLE analytics_acme SET statement_timeout = '60s';
```

The connection limit is the control that actually binds. Size it, and size the store, on the
assumption that every one of those connections may be running a long query.

## Practical notes {#practical-notes}

- **Query the rollup, not the raw table, for anything over a long range.** It is a continuous
  aggregate: the work is already done. Scanning a month of it is cheap; scanning a month of raw
  measurements is not.
- **A tenant gets one reader role, and every tool for that tenant shares it.** The role name *is* the
  tenant, so `acme` has exactly one legal reader name, and a deployment that declares a second is
  refused. Two tools on one tenant share the connection limit and the position decision, and cannot
  be revoked separately. Size `connection_limit` for all of them together, and set `reads_location`
  if *any* of them needs a map.
- **A database restart ends a reader's session after five seconds.** When the event store's primary
  stops (a failover, a node drain, a configuration rollout), connected clients get five seconds
  before their sessions are ended, so a long query running at that moment fails. Reconnect and run
  it again. See [when a database primary stops](../deployment/bootstrap.md#ha-database-failover).
- **A reader survives a schema change but does not automatically gain from one.** The views expose a
  fixed set of columns. A column added to the platform later appears on the analytics surface when it
  is deliberately added there, not before.
- **A reader sees some metadata beyond its own tenant.** PostgreSQL's catalogs are readable by any
  connected role. A reader can list the other role names on the server (and so which tenants have BI
  access), see when those sessions are active, and see internal table and chunk names. It cannot read
  a row of any of it. If that matters, give each customer its own instance.
- **Deleting a tenant does not delete its reader role.** When you decommission it, remove the role
  from your deployment variables, then drop it as a superuser. The telemetry is erased, so the role
  reads nothing — but a login that still exists is a login somebody still holds. **A tenant id can
  also be reused, and the role would then read its successor's data.** Dropping the role closes both
  gaps; removing it from your declaration alone leaves it in the database, as described under
  [Declare a reader](#declaring-a-reader).
