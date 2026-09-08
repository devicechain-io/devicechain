# migrationdiff — migration-flatten equivalence harness

Runs every service's `gormigrate` chain through the **real** `core/rdb` path against a
throwaway TimescaleDB and captures a canonical schema snapshot per functional area.

It exists to underwrite the **GA migration-squash**: the plan to collapse each
service's incremental migration chain into a single frozen baseline before `v1.0.0`.
The risk in a squash is that the baseline silently produces a *different* schema than
the chain it replaces (a dropped index, a different physical column order, a
table that was created-then-dropped by the chain but simply never exists in the
baseline). This harness makes that provable instead of hopeful.

It is also, today, the **only** thing that exercises the migrations against real
Postgres + Timescale — the service unit tests run on SQLite and never touch the
hypertable / continuous-aggregate DDL.

## How it works

For each area (`registry.go`), the tool:

1. Constructs the production `rdb.RdbManager` and calls `ExecuteInitialize` — the same
   code path a service runs at startup: create the database, create the per-area
   Postgres schema, pin `search_path` + the gorm table prefix, and run the chain with
   the production `rdb.MigrationOptions` (so it is the real thing, not a lookalike).
2. `pg_dump --schema-only` that area's schema, run **inside the container** so the
   pg_dump version always matches the server.
3. Normalizes the dump (`normalize.go`): strips psql/`SET`/comment noise, scrubs
   TimescaleDB's sequential internal-hypertable numbers and job ids, then splits into
   statements and **sorts** them — so creation *order* does not matter, but every
   object's own definition (column order, types, constraints, indexes) still does.
4. Compares the golden and the fresh dump **statement by statement, count-aware**
   (`diff.go`).

That last step is not a detail. It used to be a set difference over *lines*, and a
pg_dump writes one column per line, so the comparison knew neither which table a column
line belonged to nor how many tables had it. Removing `description` from one of four
tables that share a `description character varying(1024),` line left the line present in
the schema, so the difference was empty and the harness printed `ok ... matches golden`
and exited 0 — measured, on the real thing, against a schema a plain `diff` showed to be
one line short. Between 23% and 54% of the lines in every golden are duplicated somewhere
else in the same schema and were invisible to removal the same way; `device-management`,
the largest chain and the hardest flatten, was the worst at 54%.

A statement carries its own object name, which fixes *which table*. Comparing as a
multiset rather than a set fixes *how many*.

## Usage

```bash
# Capture golden schemas from the current incremental chains (do this now, pre-squash):
hack/migration-diff.sh snapshot

# Assert the chains still reproduce the goldens (CI guard; and the squash proof):
hack/migration-diff.sh verify
```

`hack/migration-diff.sh` stands up a throwaway `timescale/timescaledb` container
(matching `deploy/opentofu`'s pinned image), runs the tool, and tears the container
down. Requires Docker + Go.

Direct invocation (if you manage the container yourself):

```bash
go run . -mode verify -container dc-mdiff -host localhost -port 55432 \
  -user postgres -password postgres -db dcmigrationdiff -golden-dir golden
```

Flags: `-mode snapshot|verify|coverage|replay`, `-only area1,area2` (subset), `-golden-dir`.

The four modes answer four different questions about one migration chain:

| mode | question |
| --- | --- |
| `snapshot` | capture what the chains build now, as the oracle everything else compares to |
| `verify` | do the chains still build it? (also runs `coverage` and `replay`) |
| `coverage` | can the tenant purge account for every table the chains created? |
| `replay` | can each migration survive being run a second time, as it is after a partial failure? |

`coverage` and `replay` refuse `-only`: a run that skipped areas reports them clean, which
reads exactly like a run that checked them.

## The squash workflow

1. **Now** (chains still incremental): `hack/migration-diff.sh snapshot` and commit
   `golden/*.sql`. These are the equivalence oracle. `verify` runs in CI to catch any
   schema change that lands without refreshing its golden.
2. **At the GA squash** (per service): replace the chain in the service's migration
   package with a single baseline migration. `registry.go` needs no change — it now
   points at the baseline. Run `hack/migration-diff.sh verify`: if the baseline
   reproduces the golden, the squash is proven schema-equivalent to the whole chain.

   🔴 **Do not re-snapshot after a squash.** The golden is the oracle the flatten is
   being judged against, and a squash that passes leaves it byte-identical anyway — so a
   `snapshot` step here can only ever do one thing, which is overwrite the evidence with
   whatever the new baseline happens to produce and turn a failing flatten into a green
   one. When `verify` reports a diff, the baseline is wrong. Fix the baseline.

## Notes

- **Version sensitivity.** Continuous-aggregate dumps vary by Timescale version, so
  `snapshot` and `verify` must use the same image. The script pins it; override with
  `MDIFF_IMAGE` when the deployed Timescale version is bumped, and re-snapshot.
- **event-management** is the hard case — the only Timescale user (hypertables + a
  continuous aggregate). The other nine areas are plain Postgres and their probe output is
  empty.
- Goldens capture **schema only** (no data); the `<area>_migrations` bookkeeping table is
  identical across chain and baseline, so it is left in.
- **TimescaleDB objects come from a catalog probe, not from `pg_dump`** (`timescale.go`).
  A hypertable's identity lives in `_timescaledb_catalog`, which a `--schema <area>` dump
  never visits, so a plain table and a hypertable dump identically apart from the
  `<table>_occurred_time_idx` index Timescale creates for you. That made a false green
  trivial to reach: a flatten author working *from the golden* writes that index as a
  plain `CREATE INDEX`, omits `create_hypertable`, and `verify` reports `ok` for six plain
  tables that should be six hypertables. Hypertables (with their dimension and chunk
  interval), continuous aggregates and the aggregate's refresh policy are now captured as
  pseudo-DDL `TIMESCALE ...` lines. Compression and retention policies are deliberately
  **not** captured — those are reconciled at runtime from configuration
  (`event-management/model/lifecycle.go`), never installed by a migration.
- 🔴 **A LOST SEED IS INVISIBLE HERE, and that is the sharp edge of a flatten.**
  `pg_dump --schema-only` captures no rows, so a baseline that creates every table
  correctly and forgets the data a migration inserted passes with `ok`. Measured:
  stubbing out `seedTenantTiers` in user-management's baseline left all ten areas green
  and the exit code 0 — while a tenant cannot be created at all without a tier row,
  because the FK is required. The only gate that catches it is `dcctl bootstrap` failing
  in the kind e2e, which is slow and names the symptom rather than the cause.

  So a flatten must account for every data-bearing migration in the chain EXPLICITLY,
  and the two kinds are not treated alike:

  - a **seed** (rows the schema is unusable without) folds into the baseline and needs a
    unit test asserting the migration wrote them — running without error is not enough;
  - a **backfill** (rows derived from rows already present) is *dropped*, because a
    baseline runs against an empty database where there is nothing to backfill. Confirm
    it really only rewrites existing rows before dropping it.
