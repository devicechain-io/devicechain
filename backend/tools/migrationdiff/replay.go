// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// runReplay asserts that every migration in every chain is individually RE-RUNNABLE.
//
// # The rule it enforces
//
// Migrations run with UseTransaction:false, so a migration that fails partway is never
// rolled back and gormigrate replays it from the top on the next boot. (The reason for
// that setting is narrower than it is usually stated: it is not that Timescale forbids
// DDL in a transaction generally — create_hypertable and the policy calls are fine — but
// that CREATE MATERIALIZED VIEW ... timescaledb.continuous and
// refresh_continuous_aggregate refuse a transaction block outright.) Every service's
// migrations.go tells the next maintainer their migration must be individually
// re-runnable. Until now nothing checked it. gormigrate skips an ID it has already
// recorded, so simply running a chain twice is a no-op that proves nothing, and `verify`
// — which compares pg_dump output — cannot see the property at all.
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
// The worked example is the one this gate found on its first run, and it is worth knowing
// because it is not the one anybody predicts. event-management's baseline runs
// `ALTER TABLE ... ALTER COLUMN ... TYPE varchar(128) COLLATE "C"` and then, further
// down, creates a continuous aggregate over those columns. Forward that is fine. On
// replay the aggregate already exists, and Postgres refuses: "cannot alter type of a
// column used by a view or rule". The pod then crash-loops on a message that names a
// view, pointing away from the migration that is actually stuck.
//
// # Why success is not the only thing checked
//
// "It ran twice without erroring" is a weaker claim than it looks, and here is the
// concrete reason rather than a general one. An UNNAMED `CREATE INDEX` does not fail on
// replay — Postgres derives the index name from the column list, finds it taken, and
// quietly picks the next one: `events_tenant_id_occurred_time_idx1`. The migration exits
// 0 and the database now carries a duplicate index that is written on every insert and
// chosen by nothing. Exit status cannot see that. So each replay is bracketed by three
// comparisons, and all three must come back identical:
//
//   - the normalized schema dump, which catches structural change;
//   - the RAW Timescale catalog probe, unnormalized — because normalize scrubs the
//     materialization-hypertable number, which is exactly the digit that moves when a
//     continuous aggregate is dropped and recreated. The re-runnable recipe the baseline
//     itself prescribes for a cagg is DROP + CREATE, so a migration written by the book
//     would silently discard a materialization on every replay and the normalized dump
//     would report it clean;
//   - the per-table row counts, which catch a seed with no ON CONFLICT clause on a table
//     whose surrogate key lets the duplicate through. Rows are this harness's documented
//     blind spot everywhere else, since pg_dump --schema-only carries none.
//
// The pass runs in its OWN database (<db>_replay). Nothing downstream currently reads the
// verify database after this point, so that is defensive rather than load-bearing — but
// this pass drops and rebuilds schemas repeatedly, which destroys the very artefact
// `verify` just finished asserting against the goldens, and a check added after it would
// then be silently running against a rebuilt database.
func runReplay(ctx context.Context, container, host string, port int, user, password, db string, selected []area, filtered bool) error {
	// A filtered run exercises a subset and reports the same "ok" as a full one. The
	// coverage gate refuses a filter for the same reason; so does this.
	if filtered {
		return fmt.Errorf("replay must run over every area: a filtered run reports the areas it " +
			"never exercised as clean")
	}

	// An exemption that matches no live migration is a claim about a defect that may no
	// longer exist. Check the registry against the chains before anything runs.
	if err := assertExemptionsResolve(areas); err != nil {
		return err
	}

	// 🔴 PER-AREA VACUITY. The global "did anything run" check at the bottom is not
	// enough: one area whose chain resolved to a nil slice contributes nothing, prints
	// nothing, and is still counted in "across N areas" — so the run stays green while
	// that area is silently unexamined. An area with no migrations is a registry defect,
	// not a quiet pass.
	for _, a := range selected {
		if len(a.migrations) == 0 {
			return fmt.Errorf("area %q has an empty migration chain, so this run would report it "+
				"as clean without examining anything — check registry.go", a.name)
		}
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

	checked, confirmed, failures := 0, 0, 0
	for _, a := range selected {
		for k := 1; k <= len(a.migrations); k++ {
			id := a.migrations[k-1].ID
			err := replayOne(ctx, admin, a, k, container, host, port, user, password, replayDB)

			if ex, isExempt := replayExemptionFor(a.name, id); isExempt {
				if msg := exemptionVerdict(ex, err); msg != "" {
					failures++
					fmt.Printf("REPLAY   %-24s %s %s\n", a.name, id, msg)
				} else {
					confirmed++
					fmt.Printf("exempt   %-24s %s fails as registered — %s\n", a.name, id, ex.reason)
				}
				continue
			}

			if err != nil {
				failures++
				fmt.Printf("REPLAY   %-24s %s is NOT re-runnable:\n%s\n", a.name, id, indent(err.Error()))
			} else {
				checked++
				fmt.Printf("ok       %-24s %s replays cleanly\n", a.name, id)
			}
		}
	}

	// 🔴 THE NEGATIVE CONTROL. "0 failures" is also what a run that exercised nothing
	// reports. A gate whose green is indistinguishable from a gate that never ran is not
	// a gate.
	// 🔑 `checked+failures`, not `checked`. Now that `checked` counts only passes, a run
	// where every migration FAILED would have checked == 0 — and reporting "we exercised
	// nothing" over a run that exercised everything and found it all broken would hide
	// the finding behind a message about the harness.
	if checked+failures == 0 {
		return fmt.Errorf("the replay pass exercised 0 non-exempt migrations across %d area(s) "+
			"(%d exempt), so a clean result means nothing", len(selected), confirmed)
	}
	// `checked` counts only the ones that PASSED, so this line cannot claim a migration
	// replays cleanly one line above the paragraph explaining that it does not.
	fmt.Printf("replay   %d migration(s) replay cleanly across %d area(s); %d known-bad confirmed still bad; %d failing\n",
		checked, len(selected), confirmed, failures)

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

And two that report as a DIFFERENCE rather than an error, because they do not fail:
  - an UNNAMED CREATE INDEX. Postgres derives the name, finds it taken, and silently
    creates "<name>1" — a duplicate index nothing will ever use. Name your indexes.
  - a continuous aggregate dropped and recreated, which discards its materialization.

Fix the migration if it is one you are adding. A frozen pre-GA baseline cannot be edited
(see CLAUDE.md), so register it in replayExemptions with the reason instead`, failures)
	}
	return nil
}

// exemptionVerdict judges a registered known-bad migration's replay result, returning ""
// when it failed exactly as registered and a message otherwise.
//
// Both non-empty verdicts are failures on purpose. An exemption that starts PASSING is
// not good news to be swallowed — it means the entry is now a lie about the code, and the
// next reader will believe it. An exemption that fails DIFFERENTLY means the defect moved
// and the registered symptom no longer describes what an operator would see.
func exemptionVerdict(ex replayExemption, err error) string {
	// A harness failure is neither "it passed" nor "the defect moved". Say so, rather
	// than reading a container reset as evidence about a migration.
	var he *harnessError
	if errors.As(err, &he) {
		return fmt.Sprintf("could not be checked — the harness itself failed before the replay "+
			"was reached:\n%s\n    This says nothing about the registered defect. Fix the harness "+
			"and re-run.", indent(err.Error()))
	}
	if err == nil {
		return fmt.Sprintf("is registered as NOT re-runnable, but it replayed cleanly.\n"+
			"    Delete its entry from replayExemptions — an exemption that no longer describes\n"+
			"    the code is worse than no exemption, because it is believed.\n"+
			"    Registered reason: %s", ex.reason)
	}
	if !strings.Contains(err.Error(), ex.symptom) {
		return fmt.Sprintf("is registered as failing with %q, but it failed differently:\n%s\n"+
			"    The defect moved. Update the entry's symptom and reason, or remove it if the\n"+
			"    original defect is gone and this is a new one to fix.", ex.symptom, indent(err.Error()))
	}
	return ""
}

// harnessError marks a failure that is the HARNESS's, not the migration's: a dropped
// schema, a forward run that never reached the replay, a pg_dump that could not run.
//
// 🔴 It exists because exemptionVerdict matches on error TEXT, and without this a
// container reset mid-dump on the one registered known-bad migration would print "the
// defect moved — update the entry's symptom and reason". That is an instruction to edit
// a defect registry in response to an infrastructure outage, and whoever followed it
// would be writing down a symptom that describes nothing.
type harnessError struct{ err error }

func (e *harnessError) Error() string { return e.err.Error() }
func (e *harnessError) Unwrap() error { return e.err }

// replayOne runs one (area, migration) pair: fresh schema, chain prefix, forget the last
// migration's bookkeeping, run again, and require success plus an unchanged database.
func replayOne(ctx context.Context, admin *gorm.DB, a area, k int, container, host string, port int, user, password, db string) error {
	// Fresh every time. Without this, migration k+1 would replay onto a schema already
	// carrying the effects of the previous iteration's replay, and a failure could no
	// longer be attributed to one migration.
	if err := admin.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %q CASCADE", a.name)).Error; err != nil {
		return &harnessError{fmt.Errorf("dropping schema %s: %w", a.name, err)}
	}

	prefix := a.migrations[:k]
	if err := migrateSome(ctx, a.name, prefix, host, port, user, password, db); err != nil {
		// Not a replay failure — the chain does not even build forward. Say which, or
		// this gate takes the blame for a broken forward migration.
		return &harnessError{fmt.Errorf("the FIRST run of this prefix failed, so the replay was "+
			"never reached (a forward-migration defect, not a replay one): %w", err)}
	}
	before, err := snapshotFor(admin, container, user, db, a.name)
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
		return &harnessError{fmt.Errorf("forgetting %s in %s.%s: %w", id, a.name, mtable, res.Error)}
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("expected to forget exactly 1 bookkeeping row for %s, deleted %d — the "+
			"replay would have been a no-op and passed vacuously", id, res.RowsAffected)
	}

	if err := migrateSome(ctx, a.name, prefix, host, port, user, password, db); err != nil {
		return err
	}
	after, err := snapshotFor(admin, container, user, db, a.name)
	if err != nil {
		return err
	}
	return before.diff(after)
}

// snapshot is everything compared across a replay: the normalized schema text, the raw
// Timescale catalog probe, and the per-table row counts.
type snapshot struct {
	normalized string
	rawProbe   string
	rows       map[string]int64
}

// diff reports the first difference between two snapshots of the same database, or "" if
// they are identical.
func (s snapshot) diff(other snapshot) error {
	if d := statementDiff(splitNormalized(s.normalized), splitNormalized(other.normalized)); d != "" {
		return fmt.Errorf("it ran a second time without erroring, but CHANGED THE SCHEMA — "+
			"re-running a migration must be a no-op:\n%s", indent(d))
	}
	// Compared RAW, not normalized. normalizeDump scrubs the materialization-hypertable
	// number out of the probe, which is correct for `verify` (the chain and the baseline
	// legitimately number them differently) and exactly wrong here: this compares two
	// probes of the SAME database seconds apart, where that number moves only if
	// something dropped and recreated the object.
	if s.rawProbe != other.rawProbe {
		return fmt.Errorf("it ran a second time without erroring, but CHANGED A TIMESCALE OBJECT "+
			"— a continuous aggregate or hypertable was recreated, which discards its\n"+
			"materialization and re-runs a full refresh on every replay:\n  before: %s\n  after:  %s",
			s.rawProbe, other.rawProbe)
	}
	var changed []string
	for table, was := range s.rows {
		if now, ok := other.rows[table]; !ok {
			changed = append(changed, fmt.Sprintf("%s: table disappeared (had %d rows)", table, was))
		} else if now != was {
			changed = append(changed, fmt.Sprintf("%s: %d rows -> %d rows", table, was, now))
		}
	}
	for table := range other.rows {
		if _, ok := s.rows[table]; !ok {
			changed = append(changed, fmt.Sprintf("%s: table appeared", table))
		}
	}
	if len(changed) > 0 {
		sort.Strings(changed)
		return fmt.Errorf("it ran a second time without erroring, but CHANGED ROWS — a seed "+
			"needs an ON CONFLICT clause to survive a replay:\n%s", indent(strings.Join(changed, "\n")))
	}
	return nil
}

// snapshotFor captures one area's comparable state.
func snapshotFor(admin *gorm.DB, container, user, db, schema string) (snapshot, error) {
	dump, err := dumpSchema(container, user, db, schema)
	if err != nil {
		return snapshot{}, &harnessError{fmt.Errorf("dumping %s schema: %w", schema, err)}
	}
	probe, err := probeTimescale(container, user, db, schema)
	if err != nil {
		return snapshot{}, &harnessError{fmt.Errorf("probing %s timescale objects: %w", schema, err)}
	}
	rows, err := rowCounts(admin, schema)
	if err != nil {
		return snapshot{}, &harnessError{err}
	}
	return snapshot{
		normalized: normalizeDump(dump + "\n" + probe),
		rawProbe:   probe,
		rows:       rows,
	}, nil
}

// rowCounts returns a live count for every base table in the schema.
//
// Two tables are excluded, and the reasons are different:
//
//   - the area's gormigrate bookkeeping table, because the replay deletes a row from it
//     and the re-run puts it back. It nets to zero today, but a count that is only
//     correct by arithmetic coincidence is not a check worth failing on.
//   - audit_events, because gormigrate's own INSERT of that bookkeeping row is an
//     ordinary gorm Create and therefore fires the audit callbacks. A replay writes one
//     more audit row BY DESIGN. Excluding it is not hiding a defect: nothing in a
//     migration writes to it deliberately, and its growth here is a record of this
//     harness's own action.
func rowCounts(admin *gorm.DB, schema string) (map[string]int64, error) {
	// query_to_xml runs a count per table inside one statement, so this is one round trip
	// rather than one per table and needs no dynamic SQL assembled in Go.
	const q = `
SELECT t.table_name,
       (xpath('/row/cnt/text()',
              query_to_xml(format('SELECT count(*) AS cnt FROM %I.%I', t.table_schema, t.table_name),
                           false, true, '')))[1]::text::bigint AS n
  FROM information_schema.tables t
 WHERE t.table_schema = ? AND t.table_type = 'BASE TABLE'`

	var out []struct {
		TableName string
		N         int64
	}
	if err := admin.Raw(q, schema).Scan(&out).Error; err != nil {
		return nil, fmt.Errorf("counting rows in %s: %w", schema, err)
	}
	skip := map[string]bool{
		rdb.MigrationTableName(schema): true,
		"audit_events":                 true,
	}
	counts := make(map[string]int64, len(out))
	for _, r := range out {
		if skip[r.TableName] {
			continue
		}
		counts[r.TableName] = r.N
	}
	return counts, nil
}

// ensureDatabase creates the replay database if it is absent. The RdbManager would create
// it on its first ExecuteInitialize, but the admin connection that drops schemas has to
// exist BEFORE the first area runs — and the first iteration is precisely the one whose
// schema most needs to start clean, since an earlier invocation of this tool will have
// left one behind.
func ensureDatabase(ctx context.Context, host string, port int, user, password, name string) error {
	// Retried, because this is the FIRST connection the replay pass makes and it lands
	// while the container is still settling: the postgres entrypoint starts a temporary
	// server for its init scripts and then restarts it, so a connection accepted a moment
	// earlier is reset. Every other mode reaches the database through the RdbManager,
	// which retries for exactly this reason; reaching it directly meant re-earning that
	// lesson.
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
