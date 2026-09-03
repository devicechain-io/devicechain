// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// migrationdiff runs each service's gormigrate chain through the real core/rdb path
// against a throwaway Postgres/TimescaleDB and captures a canonical schema snapshot
// per functional area. It underwrites the GA migration-flatten (the plan to collapse
// each service's incremental chain into one frozen baseline before v1.0.0): capture
// the goldens now from the incremental chains, and when a chain is later squashed the
// same tool in `verify` mode proves the baseline reproduces the identical schema.
//
// It is also, today, the ONLY thing that exercises the migrations against real
// Postgres + Timescale — the service unit tests run against SQLite and never touch the
// hypertable / continuous-aggregate DDL.
//
// Usage (see hack/migration-diff.sh, which stands up the container and wires these):
//
//	migrationdiff -mode snapshot -container dc-mdiff -host localhost -port 55432
//	migrationdiff -mode verify   -container dc-mdiff -host localhost -port 55432
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

func main() {
	mode := flag.String("mode", "verify", "snapshot (write goldens) | verify (diff against goldens) | coverage (assert the tenant purge accounts for every table) | replay (assert every migration is individually re-runnable)")
	container := flag.String("container", "", "docker container running Postgres/Timescale, for a version-matched pg_dump via 'docker exec' (host pg_dump is often older than the server)")
	host := flag.String("host", "localhost", "Postgres host the migration chain connects to (TCP)")
	port := flag.Int("port", 5432, "Postgres port")
	user := flag.String("user", "postgres", "Postgres superuser")
	password := flag.String("password", "postgres", "Postgres password")
	db := flag.String("db", "dcmigrationdiff", "database created + migrated into (schema-per-area, mirroring an instance)")
	goldenDir := flag.String("golden-dir", "golden", "directory of <area>.sql golden snapshots")
	only := flag.String("only", "", "comma-separated area filter (default: all)")
	flag.Parse()

	switch *mode {
	case "snapshot", "verify", "replay":
		// replay needs the dump too: "it ran twice without erroring" is not the claim —
		// "the second run changed nothing" is, and only a schema comparison says that.
		if *container == "" {
			fatalf("-container is required (the schema dump runs `docker exec <container> pg_dump` for a version-matched dump)")
		}
	case "coverage":
		// coverage reads the catalog over a normal connection and never shells out to
		// pg_dump, so it needs no container.
	default:
		fatalf("-mode must be snapshot, verify, coverage or replay, got %q", *mode)
	}

	selected, err := selectAreas(*only)
	if err != nil {
		fatalf("%v", err)
	}

	if *mode == "coverage" {
		if err := runCoverage(context.Background(), *host, *port, *user, *password, *db, selected, *only != ""); err != nil {
			fatalf("%v", err)
		}
		return
	}

	if *mode == "replay" {
		if err := runReplay(context.Background(), *container, *host, *port, *user, *password, *db, selected, *only != ""); err != nil {
			fatalf("%v", err)
		}
		return
	}

	// 🔴 A GOLDEN WITH NO AREA IS A SILENT SKIP, and the squash is nine changes to
	// registry.go. run() iterates the areas, so deleting an entry there does not fail
	// anything: the remaining areas all report ok, the exit code is 0, and that area's
	// golden is simply never opened. A misspelled name is caught (the golden read
	// fails); a DELETED one is not. So the areas are cross-checked against the goldens
	// on disk before anything runs — a check that reports "no problems found" has to
	// also say how many things it was supposed to look at.
	if *mode == "verify" {
		if err := assertGoldensCovered(*goldenDir, selected, *only != ""); err != nil {
			fatalf("%v", err)
		}
	}

	if err := run(*mode, *container, *host, *port, *user, *password, *db, *goldenDir, selected); err != nil {
		fatalf("%v", err)
	}
}

func run(mode, container, host string, port int, user, password, db, goldenDir string, selected []area) error {
	ctx := context.Background()
	failures := 0

	for _, a := range selected {
		if err := migrateChain(ctx, a, host, port, user, password, db); err != nil {
			return fmt.Errorf("running %s migrations: %w", a.name, err)
		}
		dump, err := dumpSchema(container, user, db, a.name)
		if err != nil {
			return fmt.Errorf("dumping %s schema: %w", a.name, err)
		}
		// Appended BEFORE normalization so the probe's lines are scrubbed and sorted
		// with everything else — Timescale's internal materialization-hypertable number
		// appears in both the dumped view and the probed catalog row.
		probe, err := probeTimescale(container, user, db, a.name)
		if err != nil {
			return fmt.Errorf("probing %s timescale objects: %w", a.name, err)
		}
		normalized := normalizeDump(dump + "\n" + probe)
		// Parsed back out of the joined form rather than kept as a list, so the live
		// side and the golden side go through the identical parser — see splitNormalized.
		gotStmts := splitNormalized(normalized)

		goldenPath := filepath.Join(goldenDir, a.name+".sql")
		switch mode {
		case "snapshot":
			if err := os.MkdirAll(goldenDir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(goldenPath, []byte(normalized+"\n"), 0o644); err != nil {
				return err
			}
			fmt.Printf("snapshot %-24s %d statements → %s\n", a.name, len(gotStmts), goldenPath)
		case "verify":
			raw, err := os.ReadFile(goldenPath)
			if err != nil {
				return fmt.Errorf("reading golden for %s (run -mode snapshot first?): %w", a.name, err)
			}
			wantStmts := splitNormalized(strings.TrimRight(string(raw), "\n"))
			if diff := statementDiff(wantStmts, gotStmts); diff != "" {
				failures++
				fmt.Printf("DIFF     %-24s schema differs from golden (golden %d statements, got %d):\n%s\n",
					a.name, len(wantStmts), len(gotStmts), indent(diff))
			} else {
				fmt.Printf("ok       %-24s matches golden (%d statements)\n", a.name, len(gotStmts))
			}
		}
	}

	if failures > 0 {
		return fmt.Errorf("%d area(s) diverged from their golden schema", failures)
	}
	return nil
}

// migrateChain runs one area's chain via the real RdbManager path: it creates the
// database (once), the per-area schema, pins search_path + the gorm table prefix, and
// runs gormigrate with the production MigrationOptions — so the snapshot reflects the
// real schema, not a lookalike. Constructed as a struct literal (not NewRdbManager) so
// no lifecycle callbacks are needed; ExecuteInitialize is the migration entrypoint.
func migrateChain(ctx context.Context, a area, host string, port int, user, password, db string) error {
	return migrateSome(ctx, a.name, a.migrations, host, port, user, password, db)
}

// migrateSome is migrateChain over an explicit migration slice, so the replay pass can
// run a PREFIX of a chain. Splitting it out rather than adding a parameter to
// migrateChain keeps every existing caller reading as "run this area's chain", which is
// what they mean; only replay cares that a chain has a middle.
func migrateSome(ctx context.Context, areaName string, migrations []*gormigrate.Migration, host string, port int, user, password, db string) error {
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: db, FunctionalArea: areaName},
		Migrations:   migrations,
		InstanceConfig: config.DatastoreConfiguration{
			Type: "timescaledb",
			Configuration: map[string]interface{}{
				"hostname":       host,
				"port":           port,
				"username":       user,
				"password":       password,
				"maxConnections": 5,
			},
		},
		// Zero → the production pool defaults (poolSizing: 20 open / 2 idle). A small
		// pool deadlocks: WithAdvisoryLock pins one connection for the whole run while
		// event-management's Timescale migrations (continuous aggregate) transiently
		// need several more, so gormigrate blocks acquiring one that never frees.
		MicroserviceConfig: config.MicroserviceDatastoreConfiguration{},
	}
	if err := mgr.ExecuteInitialize(ctx); err != nil {
		return err
	}
	// Close this area's pool so its connections do not accumulate on the shared
	// server across all nine areas (the dump uses its own connection via docker exec).
	if sqldb, err := mgr.Database.DB(); err == nil {
		_ = sqldb.Close()
	}
	return nil
}

// dumpSchema returns the schema-only pg_dump of one area's Postgres schema, run inside
// the server container so the pg_dump version always matches the server. --no-owner /
// --no-privileges drop role noise that is irrelevant to structure.
func dumpSchema(container, user, db, schema string) (string, error) {
	cmd := exec.Command("docker", "exec", container,
		"pg_dump", "-U", user, "-d", db,
		"--schema-only", "--no-owner", "--no-privileges",
		"--schema", schema,
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// assertGoldensCovered fails when the goldens on disk and the areas about to run do not
// account for each other. filtered says an explicit -only subset is in force, in which
// case an unvisited golden is the caller's intent rather than a defect — but the run is
// then NOT full coverage, and the summary says so, because a green from a subset reads
// exactly like a green from everything.
func assertGoldensCovered(goldenDir string, selected []area, filtered bool) error {
	entries, err := filepath.Glob(filepath.Join(goldenDir, "*.sql"))
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, e := range entries {
		have[strings.TrimSuffix(filepath.Base(e), ".sql")] = true
	}
	for _, a := range selected {
		delete(have, a.name)
	}
	if len(have) > 0 && !filtered {
		var orphans []string
		for n := range have {
			orphans = append(orphans, n)
		}
		sort.Strings(orphans)
		return fmt.Errorf("golden schema(s) with no area in registry.go: %s\n"+
			"An area removed from the registry is not verified by anything — it is skipped\n"+
			"silently and the run still exits 0. Restore the entry, or delete the golden if\n"+
			"the area is genuinely gone.", strings.Join(orphans, ", "))
	}
	if filtered {
		fmt.Printf("NOTE     -only is in force: %d of %d area(s) verified. This run is NOT full coverage.\n",
			len(selected), len(areas))
	}
	return nil
}

func selectAreas(only string) ([]area, error) {
	if only == "" {
		return areas, nil
	}
	want := map[string]bool{}
	for _, n := range strings.Split(only, ",") {
		want[strings.TrimSpace(n)] = true
	}
	var out []area
	for _, a := range areas {
		if want[a.name] {
			out = append(out, a)
			delete(want, a.name)
		}
	}
	if len(want) > 0 {
		var unknown []string
		for n := range want {
			unknown = append(unknown, n)
		}
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown area(s): %s", strings.Join(unknown, ", "))
	}
	return out, nil
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "migrationdiff: "+format+"\n", args...)
	os.Exit(1)
}
