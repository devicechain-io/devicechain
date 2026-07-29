// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// seedEventsOptions is the event-store half's seed. It shares the receipt with
// the secret half rather than writing its own file: the two halves drill two
// databases of ONE instance, and a single receipt is what stops a run from
// verifying event data against a receipt from a different disaster.
type seedEventsOptions struct {
	receipt string

	host     string
	port     int
	user     string
	password string
	database string
	sslMode  string

	device string
	name   string
	count  int
	reset  bool
}

// The two cohorts, and why there are two.
//
// recentAge is inside the compression policy's window, so those chunks stay
// uncompressed. oldAge is well outside it, and seed-events compresses those
// chunks explicitly.
//
// 🔴 The old cohort is not thoroughness for its own sake. The platform installs a
// compression policy on EVERY event hypertable at `compress_after => 7 days`
// (measured: six policy_compression jobs on a stock instance), so every instance
// older than a week has compressed chunks, and a compressed chunk is a different
// physical object — columnar, in a separate internal relation, reached through
// the catalog. A drill that only ever seeds fresh data restores the one shape
// production mostly is not.
const (
	recentAge = 2 * time.Hour
	oldAge    = 30 * 24 * time.Hour
)

func runSeedEvents(ctx context.Context, argv []string) error {
	fs := flagSetFor("seed-events")
	var o seedEventsOptions
	fs.StringVar(&o.receipt, "receipt", "", "receipt written by `drdrill seed`, updated in place (required)")
	fs.StringVar(&o.host, "db-host", "127.0.0.1", "event-store Postgres host (a port-forward to the instance's TimescaleDB)")
	fs.IntVar(&o.port, "db-port", 5432, "event-store Postgres port")
	fs.StringVar(&o.user, "db-user", "devicechain", "event-store Postgres user")
	fs.StringVar(&o.password, "db-password", "", "event-store Postgres password (or set "+EventPasswordEnv+")")
	fs.StringVar(&o.database, "db-name", "", "instance database name (defaults to the receipt's instance)")
	fs.StringVar(&o.sslMode, "db-sslmode", "prefer", "libpq sslmode for the database connection")
	fs.StringVar(&o.device, "device", "drdrill-sensor", "device token the seeded telemetry is attributed to")
	fs.StringVar(&o.name, "measurement", "temperature", "measurement name")
	fs.IntVar(&o.count, "count", 10, "measurements per cohort (two cohorts are seeded: recent and pre-compression-window)")
	fs.BoolVar(&o.reset, "reset", false, "delete this device's existing rows before seeding, so a run that died mid-seed can be resumed instead of costing a whole cluster rebuild")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if strings.TrimSpace(o.receipt) == "" {
		return failWith(exitSetup, "--receipt is required")
	}
	if o.count < 1 {
		return failWith(exitSetup, "--count must be at least 1")
	}

	receipt, err := ReadReceipt(o.receipt)
	if err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if o.database == "" {
		o.database = receipt.Instance
	}
	if o.password == "" {
		o.password = eventPassword()
	}

	db, err := openEventDB(ctx, o)
	if err != nil {
		// Already carries its verdict; see openEventDB.
		return err
	}
	defer closeDB(db)

	// The schema has to be the platform's, not this tool's. seed-events writes
	// rows; it does not create tables, and it refuses rather than creating them,
	// because a drill that builds its own schema proves a restore of a schema
	// nothing in production has.
	if err := assertEventSchema(ctx, db); err != nil {
		return failWith(exitSetup, "%w", err)
	}

	// Refuse a second seed onto the same device.
	//
	// measurement_events carries no unique constraint (a hypertable's payload
	// tables relate to the parent by natural key, not by a declared one), so a
	// re-run does not conflict — it INSERTS AGAIN. The receipt would then record
	// 40 rows where the first 20 came from a run whose disaster never happened,
	// and every count downstream would still agree with itself. Found by re-running
	// after a mid-seed failure, which is exactly how an operator meets it.
	var existing int
	if err := db.WithContext(ctx).Raw(`
		SELECT count(*) FROM "`+areaEvent+`".measurement_events
		WHERE tenant_id = ? AND device_token = ?`, receipt.Tenant, o.device).Row().Scan(&existing); err != nil {
		return failWith(exitSetup, "checking for an earlier seed: %w", err)
	}
	if existing > 0 {
		if !o.reset {
			return failWith(exitSetup,
				`device %q under tenant %q already has %d measurement(s) in this event store.

Seeding again would ADD to them, not replace them, and the receipt would record a
total that includes rows this run did not write. Re-run with --reset to clear this
device's rows first, or drill a fresh instance`, o.device, receipt.Tenant, existing)
		}
		// Scoped to (tenant, device) and nothing else. A seed that could clear more
		// than it wrote would be a strange thing to put in a tool whose entire job
		// is proving data survives.
		removed, err := resetSeed(ctx, db, receipt.Tenant, o.device)
		if err != nil {
			return failWith(exitSetup, "%w", err)
		}
		fmt.Printf("ok   --reset removed %d leftover row(s) for device %q\n", removed, o.device)
	}

	// Truncated to the second: the receipt round-trips these through RFC3339 and
	// the API filters on them, so sub-second precision would make the boundary
	// rows depend on how the API parses a fractional timestamp.
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-oldAge)
	end := now.Add(-recentAge).Add(time.Duration(o.count-1) * time.Minute)

	var sum float64
	rows := 0
	for _, cohort := range []struct {
		base  time.Time
		first float64
	}{
		{base: now.Add(-oldAge), first: 100},
		{base: now.Add(-recentAge), first: 20},
	} {
		for i := 0; i < o.count; i++ {
			at := cohort.base.Add(time.Duration(i) * time.Minute)
			value := cohort.first + float64(i)
			if err := insertMeasurement(ctx, db, receipt.Tenant, o.device, o.name, at, value); err != nil {
				return failWith(exitSetup, "%w", err)
			}
			sum += value
			rows++
		}
	}
	fmt.Printf("ok   %d measurements written for device %q across two cohorts (age %s and %s)\n",
		rows, o.device, oldAge, recentAge)

	if err := refreshRollup(ctx, db); err != nil {
		return failWith(exitSetup, "%w", err)
	}

	matTable, err := materializationTable(ctx, db, areaEvent)
	if err != nil {
		return failWith(exitSetup, "%w", err)
	}
	matCount, err := materializedRows(ctx, db, matTable, receipt.Tenant)
	if err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if matCount == 0 {
		return failWith(exitSetup,
			"the refresh materialized nothing into %s for tenant %q. Recording zero here would make verify-events pass against a restore that lost the aggregate entirely",
			matTable, receipt.Tenant)
	}
	fmt.Printf("ok   %d rows materialized into %s (read directly, NOT through the view)\n", matCount, matTable)

	// Compress the old cohort's chunks. See the cohort comment: this is the shape
	// production data mostly has, and it is a different physical representation.
	compressed, err := compressOldChunks(ctx, db)
	if err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if compressed == 0 {
		return failWith(exitSetup,
			"no chunk of %s.measurement_events was compressed, so the drill would only ever restore uncompressed chunks — which is not the shape a week-old instance is in",
			areaEvent)
	}
	fmt.Printf("ok   %d chunk(s) compressed, so the archive carries compressed data as well as raw\n", compressed)

	receipt.Events = &EventSeed{
		Tenant:            receipt.Tenant,
		DeviceToken:       o.device,
		EventType:         int(esmodel.Measurement),
		Name:              o.name,
		Start:             start.Format(time.RFC3339),
		End:               end.Format(time.RFC3339),
		RawCount:          rows,
		Sum:               sum,
		MaterializedCount: matCount,
	}
	if err := WriteReceipt(o.receipt, receipt); err != nil {
		return failWith(exitSetup, "write receipt: %w", err)
	}

	fmt.Printf("receipt updated at %s\n", o.receipt)
	return nil
}

// refreshRollup materializes the continuous aggregate over the whole seeded
// range, retrying while the aggregate's OWN refresh policy holds the lock.
//
// Explicit rather than waiting for that policy: it runs on a one-minute schedule
// and leaves the still-filling bucket to real-time aggregation, so waiting would
// make the count seed-events records depend on when the run happened to start.
//
// 🔴 THE RETRY IS NOT DEFENSIVE PADDING — it is the fix for a measured failure.
// The policy's schedule_interval is 60 seconds, and a manual refresh that lands
// while it is running is refused outright:
//
//	ERROR: could not refresh continuous aggregate "measurement_rollups" due to a
//	concurrent refresh (SQLSTATE 55P03)
//
// That killed a full drill run at the seed, after the cluster was already built.
// Earlier runs passed only because they missed the window. A one-in-N race that
// destroys an hour of setup is not something to leave to timing.
//
// Only 55P03 is retried. Retrying every error would turn a genuine refresh
// failure — a broken aggregate, a lost materialization hypertable — into a slow
// timeout with the cause buried, which is the opposite of what the seed is for.
//
// It cannot run inside a transaction, which is why this is a bare Exec on a
// connection gorm is not wrapping.
func refreshRollup(ctx context.Context, db *gorm.DB) error {
	const attempts = 20
	stmt := fmt.Sprintf(`CALL refresh_continuous_aggregate('%s.%s', NULL, NULL);`, areaEvent, rollupView)
	for i := 1; ; i++ {
		err := db.WithContext(ctx).Exec(stmt).Error
		if err == nil {
			return nil
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != pgErrLockNotAvailable {
			return fmt.Errorf("refreshing %s: %w", rollupView, err)
		}
		if i >= attempts {
			return fmt.Errorf(
				"refreshing %s was blocked by a concurrent refresh on all %d attempts. The aggregate's own refresh policy runs every minute; a refresh that never wins in %d tries means something is holding it far longer than one should: %w",
				rollupView, attempts, attempts, err)
		}
		fmt.Printf("     %s is being refreshed by its own policy; retrying (%d/%d)\n", rollupView, i, attempts)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// pgErrLockNotAvailable is SQLSTATE 55P03, which is what TimescaleDB raises for a
// refresh that collides with a concurrent one.
const pgErrLockNotAvailable = "55P03"

// assertEventSchema refuses a database that is not carrying the platform's event
// schema, naming what is missing.
//
// The set is stated rather than discovered, and the count is cross-checked. A
// check written as "every hypertable I found is healthy" passes against a
// database that has none — and the migration set has grown twice already
// (event_anchors, state_change_events), so a stale hardcoded list is the likelier
// mistake and the count is what catches it.
func assertEventSchema(ctx context.Context, db *gorm.DB) error {
	var found int
	for _, table := range eventHypertables {
		ok, err := isHypertable(ctx, db, areaEvent, table)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s.%s is not a hypertable in this database. seed-events writes into the schema event-management's own migrations create; it does not create one",
				areaEvent, table)
		}
		found++
	}
	if found != len(eventHypertables) {
		return fmt.Errorf("expected %d event hypertables, confirmed %d", len(eventHypertables), found)
	}

	var total int
	if err := db.WithContext(ctx).Raw(`
		SELECT count(*) FROM timescaledb_information.hypertables
		WHERE hypertable_schema = ?`, areaEvent).Row().Scan(&total); err != nil {
		return fmt.Errorf("counting the event schema's hypertables: %w", err)
	}
	if total != len(eventHypertables) {
		return fmt.Errorf(`this database has %d hypertables in %q and the drill knows about %d (%s).

A hypertable the drill does not know about is one it will not check after the
restore. Add it to eventHypertables`, total, areaEvent, len(eventHypertables), strings.Join(eventHypertables, ", "))
	}
	return nil
}

// insertMeasurement writes one measurement the way the persistence pipeline does
// — a parent row in `events` and a payload row in `measurement_events`, keyed by
// the natural key they share.
//
// Written by SQL rather than pushed through HTTP ingest, and that is a deliberate
// difference from the secret half. The secret half must go through the real API
// because its claim is about PROVENANCE: the ciphertext has to have been sealed
// by the deployed service under the KEK the chart handed it, or the drill says
// nothing about the escrow. An event row has no equivalent property — nothing a
// restore can break depends on which client inserted it. What does matter is that
// the rows land in the real hypertables, with the real keys, feeding the real
// continuous aggregate, and that holds because event-management built the schema.
//
// Going through ingest instead would add a device type, a device profile, a
// device, a credential, a second port-forward and an async broker hop between the
// write and the assertion — five ways for a RESTORE drill to fail for reasons
// that have nothing to do with a restore.
func insertMeasurement(ctx context.Context, db *gorm.DB, tenant, device, name string, at time.Time, value float64) error {
	if err := db.WithContext(ctx).Exec(`
		INSERT INTO "`+areaEvent+`".events
			(tenant_id, device_token, event_type, occurred_time, source, processed_time)
		VALUES (?, ?, ?, ?, 'drdrill', now())
		ON CONFLICT DO NOTHING`,
		tenant, device, int64(esmodel.Measurement), at).Error; err != nil {
		return fmt.Errorf("inserting the parent event at %s: %w", at.Format(time.RFC3339), err)
	}
	if err := db.WithContext(ctx).Exec(`
		INSERT INTO "`+areaEvent+`".measurement_events
			(tenant_id, device_token, event_type, occurred_time, name, value)
		VALUES (?, ?, ?, ?, ?, ?)`,
		tenant, device, int64(esmodel.Measurement), at, name, value).Error; err != nil {
		return fmt.Errorf("inserting the measurement at %s: %w", at.Format(time.RFC3339), err)
	}
	return nil
}

// compressOldChunks compresses exactly the measurement_events chunks the
// platform's OWN compression policy would compress, and returns how many.
//
// It calls compress_chunk directly instead of waiting for the policy, which next
// runs hours away on a fixed daily schedule — a drill that waited for it would
// not be a drill.
//
// 🔴 The cutoff is READ FROM THE POLICY rather than restated here. Two reasons,
// and the first was measured: a cutoff picked to sit "just after" the old cohort
// selects nothing, because show_chunks compares against a chunk's END and chunks
// are a week wide, so the cohort's own chunk extends past it. Restating the
// interval as a literal instead would make the drill compress a set the platform
// would not, which is a different test than the one being claimed.
func compressOldChunks(ctx context.Context, db *gorm.DB) (int, error) {
	var compressAfter string
	if err := db.WithContext(ctx).Raw(`
		SELECT config->>'compress_after' FROM timescaledb_information.jobs
		WHERE proc_name = 'policy_compression'
		  AND hypertable_schema = ? AND hypertable_name = 'measurement_events'`,
		areaEvent).Row().Scan(&compressAfter); err != nil || compressAfter == "" {
		return 0, fmt.Errorf(`no compression policy on %s.measurement_events, so there is no cutoff to compress at.

The platform installs one on every event hypertable; its absence here means this
database is not carrying the schema the drill is written against`, areaEvent)
	}

	rows, err := db.WithContext(ctx).Raw(
		`SELECT show_chunks(?::regclass, older_than => ?::interval)`,
		areaEvent+".measurement_events", compressAfter).Rows()
	if err != nil {
		return 0, fmt.Errorf("listing chunks older than %s: %w", compressAfter, err)
	}
	var chunks []string
	for rows.Next() {
		var chunk string
		if err := rows.Scan(&chunk); err != nil {
			rows.Close()
			return 0, fmt.Errorf("reading a chunk name: %w", err)
		}
		chunks = append(chunks, chunk)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, chunk := range chunks {
		if err := db.WithContext(ctx).Exec(`SELECT compress_chunk(?::regclass)`, chunk).Error; err != nil {
			return 0, fmt.Errorf("compressing chunk %s: %w", chunk, err)
		}
	}
	return len(chunks), nil
}

// resetSeed clears the drill's own telemetry for one device, so a run that failed
// part-way through seeding can be resumed.
//
// It exists because the refusal above used to end with "delete the rows for that
// device first", which is a tool telling an operator to go and hand-write DELETEs
// against a database mid-drill. The failure it recovers from is real and was met:
// a concurrent-refresh error killed a seed after the rows were written but before
// the receipt was, leaving an instance that could neither be verified nor re-seeded.
func resetSeed(ctx context.Context, db *gorm.DB, tenant, device string) (int64, error) {
	var removed int64
	for _, table := range []string{"measurement_events", "events"} {
		tx := db.WithContext(ctx).Exec(
			`DELETE FROM "`+areaEvent+`".`+table+` WHERE tenant_id = ? AND device_token = ?`,
			tenant, device)
		if tx.Error != nil {
			return removed, fmt.Errorf("clearing %s.%s for device %q: %w", areaEvent, table, device, tx.Error)
		}
		removed += tx.RowsAffected
	}
	return removed, nil
}
