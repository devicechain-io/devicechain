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

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

func main() {
	mode := flag.String("mode", "verify", "snapshot (write goldens) | verify (diff against goldens)")
	container := flag.String("container", "", "docker container running Postgres/Timescale, for a version-matched pg_dump via `docker exec` (host pg_dump is often older than the server)")
	host := flag.String("host", "localhost", "Postgres host the migration chain connects to (TCP)")
	port := flag.Int("port", 5432, "Postgres port")
	user := flag.String("user", "postgres", "Postgres superuser")
	password := flag.String("password", "postgres", "Postgres password")
	db := flag.String("db", "dcmigrationdiff", "database created + migrated into (schema-per-area, mirroring an instance)")
	goldenDir := flag.String("golden-dir", "golden", "directory of <area>.sql golden snapshots")
	only := flag.String("only", "", "comma-separated area filter (default: all)")
	flag.Parse()

	if *container == "" {
		fatalf("-container is required (the schema dump runs `docker exec <container> pg_dump` for a version-matched dump)")
	}
	if *mode != "snapshot" && *mode != "verify" {
		fatalf("-mode must be snapshot or verify, got %q", *mode)
	}

	selected, err := selectAreas(*only)
	if err != nil {
		fatalf("%v", err)
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
		normalized := normalizeDump(dump)

		goldenPath := filepath.Join(goldenDir, a.name+".sql")
		switch mode {
		case "snapshot":
			if err := os.MkdirAll(goldenDir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(goldenPath, []byte(normalized+"\n"), 0o644); err != nil {
				return err
			}
			fmt.Printf("snapshot %-24s %d statements → %s\n", a.name, statementCount(normalized), goldenPath)
		case "verify":
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				return fmt.Errorf("reading golden for %s (run -mode snapshot first?): %w", a.name, err)
			}
			if diff := lineDiff(strings.TrimRight(string(want), "\n"), normalized); diff != "" {
				failures++
				fmt.Printf("DIFF     %-24s schema differs from golden:\n%s\n", a.name, indent(diff))
			} else {
				fmt.Printf("ok       %-24s matches golden (%d statements)\n", a.name, statementCount(normalized))
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
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: db, FunctionalArea: a.name},
		Migrations:   a.migrations,
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

func statementCount(normalized string) int {
	if normalized == "" {
		return 0
	}
	return strings.Count(normalized, "\n") + 1
}

// lineDiff returns a compact description of the statements present in one side but not
// the other. Both sides are already normalized (noise-stripped, scrubbed, sorted), so
// a set difference of lines is a faithful structural diff. Returns "" when identical.
func lineDiff(want, got string) string {
	wantSet := map[string]bool{}
	for _, l := range strings.Split(want, "\n") {
		wantSet[l] = true
	}
	gotSet := map[string]bool{}
	for _, l := range strings.Split(got, "\n") {
		gotSet[l] = true
	}
	var only, extra []string
	for l := range wantSet {
		if l != "" && !gotSet[l] {
			only = append(only, "- "+l)
		}
	}
	for l := range gotSet {
		if l != "" && !wantSet[l] {
			extra = append(extra, "+ "+l)
		}
	}
	if len(only) == 0 && len(extra) == 0 {
		return ""
	}
	sort.Strings(only)
	sort.Strings(extra)
	return strings.Join(append(only, extra...), "\n")
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "migrationdiff: "+format+"\n", args...)
	os.Exit(1)
}
