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

Flags: `-mode snapshot|verify`, `-only area1,area2` (subset), `-golden-dir`.

## The squash workflow

1. **Now** (chains still incremental): `hack/migration-diff.sh snapshot` and commit
   `golden/*.sql`. These are the equivalence oracle. `verify` runs in CI to catch any
   schema change that lands without refreshing its golden.
2. **At the GA squash** (per service): replace the chain in the service's migration
   package with a single baseline migration. `registry.go` needs no change — it now
   points at the baseline. Run `hack/migration-diff.sh verify`: if the baseline
   reproduces the golden, the squash is proven schema-equivalent to the whole chain.
   Then refresh the golden (the baseline is the new source of truth) and commit.

## Notes

- **Version sensitivity.** Continuous-aggregate dumps vary by Timescale version, so
  `snapshot` and `verify` must use the same image. The script pins it; override with
  `MDIFF_IMAGE` when the deployed Timescale version is bumped, and re-snapshot.
- **event-management** is the hard case — the only Timescale user (hypertables + a
  continuous aggregate). The other eight areas are plain Postgres.
- Goldens capture **schema only** (no data) and exclude nothing structural; the
  `<area>_migrations` bookkeeping table is identical across chain and baseline, so it
  is left in.
