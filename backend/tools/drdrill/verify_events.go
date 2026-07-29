// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
	"gorm.io/gorm"
)

// measurementEventsQuery is the platform's own historical read. It establishes
// that the restore landed and event-management is serving, so that a later
// database-level failure has only one explanation left — the same job the secret
// half's channel precheck does. It runs second here, behind the check that the
// SERVER is carrying TimescaleDB at all; see CHECK 1 for what that ordering cost
// when it was the other way round.
const measurementEventsQuery = `query($c:EventSearchCriteria!){` +
	`measurementEvents(criteria:$c){results{deviceToken name value occurredTime}}}`

type verifyEventsOptions struct {
	receipt string

	host     string
	port     int
	user     string
	password string
	database string
	sslMode  string

	server string
	scheme string
}

// valueEpsilon is the tolerance on the summed measurement values. The column is
// numeric(20,8) and the API hands them back as GraphQL Float (float64), so the
// comparison is between two representations of the same decimal rather than
// between two floats that were computed differently.
const valueEpsilon = 1e-6

func runVerifyEvents(ctx context.Context, argv []string) error {
	fs := flagSetFor("verify-events")
	var o verifyEventsOptions
	fs.StringVar(&o.receipt, "receipt", "", "receipt written by `drdrill seed-events` (required)")
	fs.StringVar(&o.host, "db-host", "127.0.0.1", "event-store Postgres host (a port-forward to the instance's TimescaleDB)")
	fs.IntVar(&o.port, "db-port", 5432, "event-store Postgres port")
	fs.StringVar(&o.user, "db-user", "devicechain", "event-store Postgres user")
	fs.StringVar(&o.password, "db-password", "", "event-store Postgres password (or set "+EventPasswordEnv+")")
	fs.StringVar(&o.database, "db-name", "", "instance database name (defaults to the receipt's instance)")
	fs.StringVar(&o.sslMode, "db-sslmode", "prefer", "libpq sslmode for the database connection")
	fs.StringVar(&o.server, "server", "localhost", "instance ingress host the API is reachable on")
	fs.StringVar(&o.scheme, "scheme", "http", "http or https for the API check")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if strings.TrimSpace(o.receipt) == "" {
		return failWith(exitSetup, "--receipt is required")
	}

	receipt, err := ReadReceipt(o.receipt)
	if err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if receipt.Events == nil {
		return failWith(exitSetup,
			"%s records no event seed, so this run has nothing to verify. Run `drdrill seed-events` before the disaster",
			o.receipt)
	}
	seed := *receipt.Events
	if o.database == "" {
		o.database = receipt.Instance
	}
	if o.password == "" {
		o.password = eventPassword()
	}

	// CHECK 1 — the SERVER is configured to serve this data at all.
	//
	// 🔴 This runs before the API check, and the order was earned rather than
	// chosen. It used to run second, and a mutation showed what that cost:
	// removing timescaledb from shared_preload_libraries on a live cluster
	// produced a Cluster still reporting "Cluster in healthy state", an API still
	// answering, and HALF the seeded history silently absent — every row in a
	// compressed chunk, returned as nothing at all, with no error anywhere. The
	// API check saw 10 of 20 rows and reported exitMismatch: "the history came
	// back INCOMPLETE or CHANGED", which tells an operator their backup is corrupt
	// and sends them to restore again from an earlier point. It would reproduce
	// exactly, because the data was never the problem.
	//
	// A precondition about the server has to be answered before any verdict about
	// the data, or the symptom outranks the cause.
	db, err := openEventDB(ctx, seedEventsOptions{
		host: o.host, port: o.port, user: o.user, password: o.password,
		database: o.database, sslMode: o.sslMode,
	})
	if err != nil {
		// Already carries its verdict; see openEventDB.
		return err
	}
	defer closeDB(db)

	// CHECK 2 — the platform itself can serve the restored history.
	//
	// Deliberately not skippable, for the reason the secret half's precheck states
	// in full: the rig reads only the exit code, so a flag that quietly removed
	// this check would leave every assertion downstream reporting the same verdict
	// on strictly less evidence.
	apiCount, apiSum, err := readMeasurementsOverAPI(ctx, o, receipt, seed)
	if err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if apiCount == 0 {
		return failWith(exitNotFound,
			"event-management returns no measurements for device %q between %s and %s. The event store was not restored, so nothing below would be a test of it",
			seed.DeviceToken, seed.Start, seed.End)
	}
	if apiCount != seed.RawCount || math.Abs(apiSum-seed.Sum) > valueEpsilon {
		return failWith(exitMismatch,
			"event-management serves %d measurements summing to %g; the seed wrote %d summing to %g. The history came back INCOMPLETE or CHANGED",
			apiCount, apiSum, seed.RawCount, seed.Sum)
	}
	fmt.Printf("ok   event-management serves all %d seeded measurements for %q (sum %g)\n",
		apiCount, seed.DeviceToken, apiSum)

	if verdict := checkHypertables(ctx, db, seed); verdict != nil {
		return verdict
	}
	if verdict := checkMaterialization(ctx, db, seed); verdict != nil {
		return verdict
	}
	if verdict := checkCompression(ctx, db, seed); verdict != nil {
		return verdict
	}
	if verdict := checkJobs(ctx, db); verdict != nil {
		return verdict
	}
	if verdict := checkWritable(ctx, db, seed); verdict != nil {
		return verdict
	}

	fmt.Printf("\nEVENT-STORE RESTORE DRILL PASSED for instance %q: telemetry written by the old cluster is served by this one, and TimescaleDB's partitioning, aggregate materialization, compressed chunks and job scheduler all came back with it.\n",
		receipt.Instance)
	return nil
}

// checkHypertables proves the six event tables came back AS HYPERTABLES holding
// the seeded rows.
//
// The hypertable question is asked separately from, and before, the row counts,
// because the two are different findings. A plain table with every row in it
// reads as a pass on any count-based check, keeps working until the table is
// large enough to matter, and cannot be fixed in place.
func checkHypertables(ctx context.Context, db *gorm.DB, seed EventSeed) error {
	for _, table := range eventHypertables {
		ok, err := isHypertable(ctx, db, areaEvent, table)
		if err != nil {
			return failWith(exitSetup, "%w", err)
		}
		if !ok {
			// Deliberately does not claim the table EXISTS: isHypertable only asks
			// the hypertables view, so a missing table and a de-partitioned one are
			// the same answer here. An earlier version asserted "exists but is NOT a
			// hypertable", which was a false statement in the case the drill's own
			// rename mutation produces.
			return failWith(exitTimescaleBroken,
				"%s.%s is not a hypertable — it is either missing or came back de-partitioned. If the rows are intact, the time partitioning the event store is built on is not, and that cannot be fixed in place",
				areaEvent, table)
		}
	}
	fmt.Printf("ok   all %d event tables are hypertables\n", len(eventHypertables))

	// 🔴 BOTH tables the seed writes, not just the payload one.
	//
	// insertMeasurement writes a parent row in `events` AND a payload row in
	// `measurement_events`. An earlier version counted only the payload table while
	// this function's comment claimed it proved all six came back "holding the
	// seeded rows" — so `TRUNCATE "event-management".events` passed every check,
	// and five of the six hypertables got a shape-only check that the CONTROL
	// cluster's un-restored event store also passes.
	for _, table := range []string{"events", "measurement_events"} {
		chunks, err := hypertableChunks(ctx, db, areaEvent, table)
		if err != nil {
			return failWith(exitSetup, "%w", err)
		}
		if chunks == 0 {
			return failWith(exitTimescaleBroken,
				"%s.%s is a hypertable with ZERO chunks. A hypertable's rows live in chunks, so its shape survived and its data did not",
				areaEvent, table)
		}
		var n int
		if err := db.WithContext(ctx).Raw(`
			SELECT count(*) FROM "`+areaEvent+`".`+table+`
			WHERE tenant_id = ? AND device_token = ?`,
			seed.Tenant, seed.DeviceToken).Row().Scan(&n); err != nil {
			return failWith(exitSetup, "counting restored rows in %s.%s: %w", areaEvent, table, err)
		}
		if n == 0 {
			return failWith(exitNotFound,
				"no rows in %s.%s for device %q under tenant %q — the restore did not bring the event data back",
				areaEvent, table, seed.DeviceToken, seed.Tenant)
		}
		if n != seed.RawCount {
			return failWith(exitMismatch,
				"%s.%s holds %d rows for device %q; the seed wrote %d",
				areaEvent, table, n, seed.DeviceToken, seed.RawCount)
		}
		fmt.Printf("ok   %s.%s: %d rows across %d chunk(s)\n", areaEvent, table, n, chunks)
	}

	// The VALUES, read from the database rather than from the API, because the API
	// applies pagination and tenant scoping of its own — two ways for the two
	// numbers to differ for reasons that are not a restore.
	var sum float64
	if err := db.WithContext(ctx).Raw(`
		SELECT coalesce(sum(value), 0) FROM "`+areaEvent+`".measurement_events
		WHERE tenant_id = ? AND device_token = ? AND name = ?`,
		seed.Tenant, seed.DeviceToken, seed.Name).Row().Scan(&sum); err != nil {
		return failWith(exitSetup, "summing restored measurements: %w", err)
	}
	if math.Abs(sum-seed.Sum) > valueEpsilon {
		return failWith(exitMismatch,
			"the restored measurements for %q sum to %g; the seed wrote %g. The right NUMBER of rows came back carrying different values",
			seed.DeviceToken, sum, seed.Sum)
	}
	fmt.Printf("ok   the restored values sum to %g, matching what the seed wrote\n", sum)
	return nil
}

// checkMaterialization is the aggregate check that can actually fail.
//
// 🔴 Read the comment on materializationTable before changing this to something
// that looks simpler. measurement_rollups sets `materialized_only = false`, so a
// read THROUGH the view — including through the platform's own
// bucketedMeasurements API — recomputes anything above the aggregate's watermark
// live from the raw hypertable and returns correct numbers for it (measured: 5
// buckets summing to 25 out of a materialization holding none). The watermark is
// restored state too, so a view read cannot separate "the materialization came
// back" from "the watermark did not, and this was recomputed". This one goes to
// the physical relation and gets a single answer.
func checkMaterialization(ctx context.Context, db *gorm.DB, seed EventSeed) error {
	table, err := materializationTable(ctx, db, areaEvent)
	if errors.Is(err, errNoMaterialization) {
		return failWith(exitTimescaleBroken,
			"%s.%s has no materialization hypertable at all: %w. The aggregate's definition came back and the object holding its data did not",
			areaEvent, rollupView, err)
	}
	if err != nil {
		// NOT a machinery verdict — the question was not answered. See
		// materializationTable.
		return failWith(exitSetup, "%w", err)
	}
	count, err := materializedRows(ctx, db, table, seed.Tenant, seed.DeviceToken)
	if err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if count == 0 {
		return failWith(exitTimescaleBroken,
			`%s.%s has a materialization hypertable (%s) holding NO rows for tenant %q, and the seed materialized %d.

The raw history is intact — the checks above passed — so what is lost is the
pre-aggregation: every bucketed read scans raw chunks until the refresh policy
catches up. How that presents through the view depends on the aggregate's
watermark, which is why this check does not go through it.

🔴 A refresh does NOT repair this on its own. Measured: with the materialization
emptied and the watermark left where it was, refresh_continuous_aggregate over
the whole range succeeds and materializes NOTHING — the watermark says that range
is already done. Repair means invalidating the range first`,
			areaEvent, rollupView, table, seed.Tenant, seed.MaterializedCount)
	}
	if count != seed.MaterializedCount {
		return failWith(exitTimescaleBroken,
			"%s holds %d materialized rows for tenant %q; the seed materialized %d",
			table, count, seed.Tenant, seed.MaterializedCount)
	}
	fmt.Printf("ok   %d rows in %s — the aggregate's materialization came back, not just its definition\n", count, table)
	return nil
}

// checkCompression proves a compressed chunk survived AS a compressed chunk and
// is still readable.
//
// Both halves matter and they fail differently: a chunk that came back
// decompressed is a silent capacity regression (the data is right and the storage
// it was chosen for is gone), while a compressed chunk that cannot be read is
// data loss that only shows up on the query that touches it.
func checkCompression(ctx context.Context, db *gorm.DB, seed EventSeed) error {
	var compressed int
	if err := db.WithContext(ctx).Raw(`
		SELECT count(*) FROM timescaledb_information.chunks
		WHERE hypertable_schema = ? AND hypertable_name = 'measurement_events' AND is_compressed`,
		areaEvent).Row().Scan(&compressed); err != nil {
		return failWith(exitSetup, "counting compressed chunks: %w", err)
	}
	if compressed == 0 {
		return failWith(exitTimescaleBroken,
			`no chunk of %s.measurement_events is compressed, and the seed compressed %d.

The platform installs a compression policy on every event hypertable, so any
instance more than a week old is mostly compressed chunks. A restore that returns
them decompressed loses the storage the policy exists for, silently`,
			areaEvent, seed.CompressedChunks)
	}
	// 🔴 EXACT, not `> 0`. The old cohort can straddle a weekly chunk boundary, so
	// the seed sometimes compresses two chunks; a restore returning one compressed
	// and one decompressed-with-rows-intact satisfies every count check and a
	// `> 0` assertion — which is precisely the silent capacity regression this
	// function claims to catch.
	if compressed != seed.CompressedChunks {
		return failWith(exitTimescaleBroken,
			"%s.measurement_events has %d compressed chunk(s) and the seed compressed %d. At least one came back DECOMPRESSED with its rows intact, which every row count agrees with",
			areaEvent, compressed, seed.CompressedChunks)
	}

	// Reading them is a separate question from their being marked compressed:
	// decompression happens at query time, out of a different physical relation.
	//
	// Counted through the chunk CATALOG and scoped to this drill's rows. The
	// earlier version used `occurred_time < now() - interval '7 days'`, which
	// counted rows in any chunk that happened to be old, compressed or not, for any
	// tenant and any device — so the number it printed as "rows read back out of
	// the compressed chunks" was neither necessarily compressed nor necessarily
	// this run's.
	readable, err := compressedRowCount(ctx, db, seed.Tenant, seed.DeviceToken)
	if err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if readable == 0 {
		return failWith(exitTimescaleBroken,
			"%d chunk(s) of %s.measurement_events are marked compressed and reading this drill's rows out of them returns nothing",
			compressed, areaEvent)
	}
	if readable != seed.CompressedRows {
		return failWith(exitMismatch,
			"the compressed chunks hold %d of this drill's rows; the seed put %d in them",
			readable, seed.CompressedRows)
	}
	fmt.Printf("ok   %d compressed chunk(s) restored compressed, and all %d of this run's rows read back out of them\n",
		compressed, readable)
	return nil
}

// checkJobs proves the background scheduler came back with something to run.
//
// A job row is data — it is restored with everything else — so its PRESENCE
// proves nothing. next_start is the part the scheduler writes, and a cluster
// whose background workers never started shows every policy present, scheduled,
// and permanently unplanned.
//
// 🔑 It asks only about the event schema's own jobs. The stock image ships a
// telemetry job that the platform deletes at initdb, precisely because
// `telemetry_level = off` leaves it present, scheduled and permanently WITHOUT a
// next_start — the exact shape this check calls stuck, on a healthy cluster,
// forever. There is a second one (policy_job_stat_history_retention) that carries
// no hypertable at all and is likewise none of this drill's business.
func checkJobs(ctx context.Context, db *gorm.DB) error {
	jobs, err := eventStoreJobs(ctx, db, areaEvent)
	if err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if len(jobs) == 0 {
		return failWith(exitTimescaleBroken,
			"the restored event store has NO background jobs on schema %q. The continuous-aggregate refresh and the compression policies are gone, so the aggregate will never advance and nothing will ever be compressed again",
			areaEvent)
	}

	refreshJob := 0
	var unplanned []string
	for _, j := range jobs {
		if j.Proc == "policy_refresh_continuous_aggregate" {
			refreshJob = j.ID
		}
		if !j.Scheduled || j.NextStart == nil {
			unplanned = append(unplanned, fmt.Sprintf("%s (job %d)", j.Proc, j.ID))
		}
	}
	if refreshJob == 0 {
		return failWith(exitTimescaleBroken,
			"the restored event store has %d background job(s) on %q but no policy_refresh_continuous_aggregate. %s would stop advancing, and real-time aggregation would hide that behind correct answers",
			len(jobs), areaEvent, rollupView)
	}
	if len(unplanned) > 0 {
		return failWith(exitTimescaleBroken,
			"%d of %d background job(s) are unscheduled or have no next run recorded: %s",
			len(unplanned), len(jobs), strings.Join(unplanned, ", "))
	}

	// 🔴 AND NOW THE ONLY PART A PHYSICAL RESTORE CANNOT FAKE.
	//
	// Everything above is restored data — the job rows, `scheduled`, and
	// next_start, which lives in the ordinary heap table bgw_job_stat and comes
	// back holding the OLD cluster's numbers. A restored cluster whose scheduler
	// never started passes every assertion above it. So the counter has to be
	// watched MOVING.
	fmt.Printf("     watching job %d's run counter, to separate a live scheduler from restored numbers\n", refreshJob)
	before, after, err := schedulerIsRunning(ctx, db, refreshJob)
	if err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if after <= before {
		return failWith(exitTimescaleBroken,
			`the restored event store's background scheduler is NOT RUNNING. Job %d
(policy_refresh_continuous_aggregate, every 60s) has run %d times and that count
did not move while it was watched.

Every other job signal here is restored DATA and looks healthy: the rows are
present, marked scheduled, and carry a next_start that came back from the old
cluster. Nothing is going to act on any of it — %s will never advance again, and
nothing will ever be compressed.`, refreshJob, after, rollupView)
	}

	fmt.Printf("ok   %d background job(s) restored, and the scheduler is live (job %d ran: %d -> %d)\n",
		len(jobs), refreshJob, before, after)
	return nil
}

// checkWritable proves the restored cluster ACCEPTS WRITES.
//
// Every check above is a read, and all of them pass against a cluster still in
// recovery — which is a real way for a restore to end, and one where the operator
// has an instance that serves history perfectly and silently drops everything
// arriving now. The row is written into the drill's own device so it cannot
// disturb the counts, and removed afterwards.
func checkWritable(ctx context.Context, db *gorm.DB, seed EventSeed) error {
	at := time.Now().UTC().Truncate(time.Second)
	probe := seed.DeviceToken + "-postrestore"
	if err := insertMeasurement(ctx, db, seed.Tenant, probe, seed.Name, at, 1); err != nil {
		return failWith(exitTimescaleBroken,
			`the restored event store will not accept a write: %w.

Every check above this one is a read, and a cluster left in recovery passes all of
them while dropping everything that arrives from now on`, err)
	}
	var n int
	if err := db.WithContext(ctx).Raw(`
		SELECT count(*) FROM "`+areaEvent+`".measurement_events
		WHERE tenant_id = ? AND device_token = ?`, seed.Tenant, probe).Row().Scan(&n); err != nil {
		return failWith(exitSetup, "reading the post-restore probe row back: %w", err)
	}
	if n != 1 {
		return failWith(exitTimescaleBroken,
			"the post-restore probe write reported success and %d rows came back", n)
	}
	// 🔴 BOTH tables. insertMeasurement writes a parent row in `events` and a
	// payload row in `measurement_events`; an earlier version deleted only the
	// payload, so "removed afterwards" was false and the parent row for the probe
	// device persisted forever. resetSeed would not have caught it either — that is
	// scoped to the SEED's device token, not the probe's.
	removed, err := resetSeed(ctx, db, seed.Tenant, probe)
	if err != nil {
		return failWith(exitSetup, "cleaning up the post-restore probe rows: %w", err)
	}
	fmt.Printf("ok   the restored cluster accepts new telemetry (probe written, read back, and %d row(s) removed)\n", removed)
	return nil
}

// readMeasurementsOverAPI asks event-management for the seeded window, as the
// tenant the receipt's scoped identity administers.
func readMeasurementsOverAPI(ctx context.Context, o verifyEventsOptions, receipt Receipt, seed EventSeed) (int, float64, error) {
	base := fmt.Sprintf("%s://%s", o.scheme, o.server)
	httpc := drillHTTPClient(o.scheme)
	session := userclient.NewTenantSession(httpc, base+"/api/user-management/graphql",
		receipt.Identity, receipt.Password, receipt.Tenant)

	var out struct {
		MeasurementEvents struct {
			Results []struct {
				DeviceToken string   `json:"deviceToken"`
				Name        string   `json:"name"`
				Value       *float64 `json:"value"`
			} `json:"results"`
		} `json:"measurementEvents"`
	}
	// pageSize is the seeded count plus headroom rather than the count exactly: a
	// page sized to the expectation returns a full page whether the restore
	// brought back that many rows or more, so an over-restore (a second run's data
	// still present) would be truncated away and read as a clean pass.
	err := session.Query(ctx, fmt.Sprintf("%s/api/%s/graphql", base, areaEvent), measurementEventsQuery,
		map[string]any{"c": map[string]any{
			"pageNumber":  1,
			"pageSize":    seed.RawCount*2 + 10,
			"deviceToken": seed.DeviceToken,
			"eventTypes":  []int{seed.EventType},
			"startTime":   seed.Start,
			"endTime":     seed.End,
		}}, &out)
	if err != nil {
		return 0, 0, fmt.Errorf("querying event-management at %s: %w", base, err)
	}

	count := 0
	sum := 0.0
	for _, r := range out.MeasurementEvents.Results {
		if r.Name != seed.Name {
			continue
		}
		count++
		if r.Value != nil {
			sum += *r.Value
		}
	}
	return count, sum, nil
}
