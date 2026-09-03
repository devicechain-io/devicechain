// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// runReplay asserts that every migration in every chain is individually RE-RUNNABLE.
//
// # The rule it enforces
//
// Migrations run with UseTransaction:false — Timescale forbids DDL in a transaction —
// so a migration that fails partway is never rolled back, and gormigrate replays it
// from the top on the next boot. Every service's migrations.go says so in as many
// words: "Anything appended must be individually re-runnable." Until now nothing
// checked it. gormigrate skips IDs it has already recorded, so simply running a chain
// twice is a no-op that proves nothing, and `verify` — which compares pg_dump output —
// cannot see the property at all.
//
// # What it actually simulates
//
// For each migration M in each chain, on a FRESH schema: run the chain up to and
// including M, delete M's bookkeeping row, and run again. gormigrate then finds M
// unrecorded and re-executes it against the schema M itself just produced.
//
// That models one failure exactly: M applied fully and the process died before (or
// while) recording it. It is a proper subset of the general case — a migration that
// half-applied leaves a state somewhere between "before M" and "after M", and those
// states cannot be enumerated from outside. So a green run here is NOT proof that a
// partially-applied M replays cleanly. It IS proof against the class that actually
// bites: a statement that is not idempotent against its own output.
//
// The worked example is the one this gate found on its first run, and it is worth
// knowing because it is not the one anybody predicts. event-management's baseline runs
// `ALTER TABLE ... ALTER COLUMN ... TYPE varchar(128) COLLATE "C"` and then, further
// down, creates a continuous aggregate over those columns. Forward that is fine. On
// replay the aggregate already exists, and Postgres refuses: "cannot alter type of a
// column used by a view or rule". The pod then crash-loops on a message that names a
// view, pointing away from the migration that is actually stuck.
//
// # Why the schema is compared before and after
//
// "It ran twice without erroring" is a weaker claim than it looks, and here is the
// concrete reason rather than a general one. An UNNAMED `CREATE INDEX` does not fail on
// replay — Postgres derives the index name from the column list, finds it taken, and
// quietly picks the next one: `events_tenant_id_occurred_time_idx1`. The migration exits
// 0 and the database now carries a duplicate index that is written on every insert and
// chosen by nothing. Exit status cannot see that; a schema comparison can.
//
// So each replay is bracketed by a schema snapshot and the two must be identical. A
// migration that changes the database by running a second time is not idempotent,
// whatever its exit status said.
//
// The pass runs in its OWN database (<db>_replay) because it drops schemas repeatedly,
// and the `verify` database is afterwards read by the coverage gate and the purge drill.
func runReplay(ctx context.Context, container, host string, port int, user, password, db string, selected []area, filtered bool) error {
	// A filtered run exercises a subset and reports the same "ok" as a full one. The
	// coverage gate refuses a filter for the same reason; so does this.
	if filtered {
		return fmt.Errorf("replay must run over every area: a filtered run reports the areas it " +
			"never exercised as clean")
	}

	// Before anything runs: an exemption that matches no live migration is a claim
	// about a defect that may no longer exist. Check the registry against the chains.
	if err := assertExemptionsResolve(areas); err != nil {
		return err
	}

	replayDB := db + "_replay"
	if err := ensureDatabase(ctx, host, port, user, password, replayDB); err != nil {
		return err
	}
	admin, err := openDatabase(host, port, user, password, replayDB)
	if err != nil {
		return err
	}
	defer closeDatabase(admin)

	checked, exempted, failures := 0, 0, 0
	for _, a := range selected {
		for k := 1; k <= len(a.migrations); k++ {
			id := a.migrations[k-1].ID
			if reason, ok := replayExemptionFor(a.name, id); ok {
				fmt.Printf("exempt   %-24s %s — %s\n", a.name, id, reason)
				exempted++
				continue
			}
			if err := replayOne(ctx, admin, a, k, container, host, port, user, password, replayDB); err != nil {
				failures++
				fmt.Printf("REPLAY   %-24s %s is NOT re-runnable:\n%s\n", a.name, id, indent(err.Error()))
			} else {
				fmt.Printf("ok       %-24s %s replays cleanly\n", a.name, id)
			}
			checked++
		}
	}

	// 🔴 THE NEGATIVE CONTROL. "0 failures" is also what a run that exercised nothing
	// reports — an empty registry, a chain that resolved to a nil slice, an exemption
	// list that grew to cover everything. A gate whose green is indistinguishable from
	// a gate that never ran is not a gate.
	if checked == 0 {
		return fmt.Errorf("the replay pass exercised 0 migrations across %d area(s) (%d exempt), so "+
			"a clean result means nothing — check registry.go still lists the chains",
			len(selected), exempted)
	}
	fmt.Printf("replay   %d migration(s) exercised across %d area(s), %d exempt\n",
		checked, len(selected), exempted)

	if failures > 0 {
		return fmt.Errorf(`%d migration(s) are not individually re-runnable

Migrations run with UseTransaction:false, so one that fails partway is not rolled back
and gormigrate replays it from the top on the next boot. A migration that cannot survive
that replay turns a transient failure into a crash-loop on a half-built schema.

The usual causes:
  - ALTER TABLE ... ALTER COLUMN ... TYPE once a view or continuous aggregate reads that
    column. Forward it is fine; on replay the view already exists and Postgres refuses.
  - CREATE INDEX / CREATE TABLE / ADD COLUMN without IF NOT EXISTS
  - a seed INSERT with no ON CONFLICT clause
  - raw DDL where a gorm AutoMigrate call would have been idempotent for free

And one that reports as a schema DIFFERENCE rather than an error, because it does not
fail: an UNNAMED CREATE INDEX. Postgres derives the name, finds it taken, and silently
creates "<name>1" — leaving a duplicate index nothing will ever use. Name your indexes.

Fix the migration if it is one you are adding. A frozen pre-GA baseline cannot be edited
(see CLAUDE.md), so register it in replayExemptions with the reason instead`, failures)
	}
	return nil
}

// replayOne runs one (area, migration) pair: fresh schema, chain prefix, forget the last
// migration's bookkeeping, run again, and require both success and an unchanged schema.
func replayOne(ctx context.Context, admin *gorm.DB, a area, k int, container, host string, port int, user, password, db string) error {
	// Fresh every time. Without this, migration k+1 would replay onto a schema already
	// carrying the effects of the previous iteration's replay, and a failure could no
	// longer be attributed to one migration.
	if err := admin.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %q CASCADE", a.name)).Error; err != nil {
		return fmt.Errorf("dropping schema %s: %w", a.name, err)
	}

	prefix := a.migrations[:k]
	if err := migrateSome(ctx, a.name, prefix, host, port, user, password, db); err != nil {
		// Not a replay failure — the chain does not even build forward. Say which, or
		// this gate takes the blame for a broken forward migration.
		return fmt.Errorf("the FIRST run of this prefix failed, so the replay was never reached "+
			"(a forward-migration defect, not a replay one): %w", err)
	}
	before, err := snapshotFor(container, user, db, a.name)
	if err != nil {
		return err
	}

	// Forget that the last migration in the prefix ran. gormigrate keys on the ID column
	// of the area's own bookkeeping table, which search_path puts in the area schema.
	// Assert exactly one row went: a silent zero-row DELETE (a renamed table, a changed
	// ID column) would make the "replay" a no-op that passes vacuously.
	mtable := rdb.MigrationTableName(a.name)
	id := a.migrations[k-1].ID
	res := admin.Exec(fmt.Sprintf("DELETE FROM %q.%q WHERE id = ?", a.name, mtable), id)
	if res.Error != nil {
		return fmt.Errorf("forgetting %s in %s.%s: %w", id, a.name, mtable, res.Error)
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("expected to forget exactly 1 bookkeeping row for %s, deleted %d — the "+
			"replay would have been a no-op and passed vacuously", id, res.RowsAffected)
	}

	if err := migrateSome(ctx, a.name, prefix, host, port, user, password, db); err != nil {
		return err
	}
	after, err := snapshotFor(container, user, db, a.name)
	if err != nil {
		return err
	}
	if diff := statementDiff(splitNormalized(before), splitNormalized(after)); diff != "" {
		return fmt.Errorf("it ran a second time without erroring, but CHANGED the schema — "+
			"re-running a migration must be a no-op:\n%s", indent(diff))
	}
	return nil
}

// snapshotFor returns the normalized schema of one area, dump plus Timescale catalog
// probe — the same pair `verify` compares against a golden, so a replay that disturbs a
// hypertable or a continuous aggregate is visible here too.
func snapshotFor(container, user, db, schema string) (string, error) {
	dump, err := dumpSchema(container, user, db, schema)
	if err != nil {
		return "", fmt.Errorf("dumping %s schema: %w", schema, err)
	}
	probe, err := probeTimescale(container, user, db, schema)
	if err != nil {
		return "", fmt.Errorf("probing %s timescale objects: %w", schema, err)
	}
	return normalizeDump(dump + "\n" + probe), nil
}

// ensureDatabase creates the replay database if it is absent. The RdbManager would
// create it on its first ExecuteInitialize, but the admin connection that drops schemas
// has to exist BEFORE the first area runs — and the first iteration is precisely the one
// whose schema most needs to start clean, since an earlier invocation of this tool will
// have left one behind.
func ensureDatabase(ctx context.Context, host string, port int, user, password, name string) error {
	// Retried, because this is the FIRST connection the replay pass makes and it lands
	// while the container is still settling: the postgres entrypoint starts a temporary
	// server for its init scripts and then restarts it, so a connection accepted a moment
	// earlier is reset. Every other mode reaches the database through the RdbManager,
	// which retries for exactly this reason (core.RetryInfraConnect); reaching it
	// directly meant re-earning that lesson.
	var root *gorm.DB
	if err := core.RetryInfraConnect(ctx, "postgres", func(context.Context) error {
		var oerr error
		root, oerr = openDatabase(host, port, user, password, "postgres")
		if oerr != nil {
			return oerr
		}
		// gorm.Open is lazy — it can hand back a handle for a server that is gone. Force
		// a round trip so the retry is driven by the connection actually working.
		sqldb, oerr := root.DB()
		if oerr != nil {
			return oerr
		}
		return sqldb.PingContext(ctx)
	}); err != nil {
		return err
	}
	defer closeDatabase(root)

	var count int64
	if err := root.Raw("SELECT count(*) FROM pg_database WHERE datname = ?", name).Scan(&count).Error; err != nil {
		return fmt.Errorf("looking for database %s: %w", name, err)
	}
	if count > 0 {
		return nil
	}
	// CREATE DATABASE takes no bind parameters. The name is built here rather than
	// supplied, but quote it anyway so a dash never becomes a syntax error the day the
	// database name gains one.
	if err := root.Exec(fmt.Sprintf("CREATE DATABASE %q", name)).Error; err != nil &&
		!strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("creating database %s: %w", name, err)
	}
	return nil
}
