// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
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
	if verdict := checkCompression(ctx, db); verdict != nil {
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
			return failWith(exitTimescaleBroken,
				"%s.%s exists but is NOT a hypertable. The rows may be intact; the time partitioning the event store is built on is not",
				areaEvent, table)
		}
	}
	fmt.Printf("ok   all %d event tables are hypertables\n", len(eventHypertables))

	chunks, err := hypertableChunks(ctx, db, areaEvent, "measurement_events")
	if err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if chunks == 0 {
		return failWith(exitTimescaleBroken,
			"%s.measurement_events is a hypertable with ZERO chunks. A hypertable's rows live in chunks, so its shape survived and its data did not",
			areaEvent)
	}

	// The raw count, read from the database rather than from the API, because the
	// API applies pagination and tenant scoping of its own — two ways for these
	// two numbers to differ for reasons that are not a restore.
	var count int
	var sum float64
	if err := db.WithContext(ctx).Raw(`
		SELECT count(*), coalesce(sum(value), 0) FROM "`+areaEvent+`".measurement_events
		WHERE tenant_id = ? AND device_token = ? AND name = ?`,
		seed.Tenant, seed.DeviceToken, seed.Name).Row().Scan(&count, &sum); err != nil {
		return failWith(exitSetup, "counting restored measurements: %w", err)
	}
	if count == 0 {
		return failWith(exitNotFound,
			"no measurement rows for device %q under tenant %q — the restore did not bring the event data back",
			seed.DeviceToken, seed.Tenant)
	}
	if count != seed.RawCount || math.Abs(sum-seed.Sum) > valueEpsilon {
		return failWith(exitMismatch,
			"the event store holds %d measurements summing to %g; the seed wrote %d summing to %g",
			count, sum, seed.RawCount, seed.Sum)
	}
	fmt.Printf("ok   %d measurement rows across %d chunk(s), summing to %g\n", count, chunks, sum)
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
	if err != nil {
		return failWith(exitTimescaleBroken, "%w", err)
	}
	count, err := materializedRows(ctx, db, table, seed.Tenant)
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
func checkCompression(ctx context.Context, db *gorm.DB) error {
	var compressed int
	if err := db.WithContext(ctx).Raw(`
		SELECT count(*) FROM timescaledb_information.chunks
		WHERE hypertable_schema = ? AND hypertable_name = 'measurement_events' AND is_compressed`,
		areaEvent).Row().Scan(&compressed); err != nil {
		return failWith(exitSetup, "counting compressed chunks: %w", err)
	}
	if compressed == 0 {
		return failWith(exitTimescaleBroken,
			`no chunk of %s.measurement_events is compressed, and the seed compressed at least one.

The platform installs a compression policy on every event hypertable, so any
instance more than a week old is mostly compressed chunks. A restore that returns
them decompressed loses the storage the policy exists for, silently`, areaEvent)
	}

	// Reading them is a separate question from their being marked compressed:
	// decompression happens at query time, out of a different physical relation.
	var readable int
	if err := db.WithContext(ctx).Raw(`
		SELECT count(*) FROM "` + areaEvent + `".measurement_events
		WHERE occurred_time < now() - interval '7 days'`).Row().Scan(&readable); err != nil {
		return failWith(exitTimescaleBroken,
			"reading the compressed chunks of %s.measurement_events failed: %w", areaEvent, err)
	}
	if readable == 0 {
		return failWith(exitTimescaleBroken,
			"%d chunk(s) of %s.measurement_events are marked compressed and reading them returns nothing",
			compressed, areaEvent)
	}
	fmt.Printf("ok   %d compressed chunk(s) restored compressed, and %d rows read back out of them\n", compressed, readable)
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

	var refresh bool
	var unplanned []string
	for _, j := range jobs {
		if j.Proc == "policy_refresh_continuous_aggregate" {
			refresh = true
		}
		if !j.Scheduled || j.NextStart == nil {
			unplanned = append(unplanned, fmt.Sprintf("%s (job %d)", j.Proc, j.ID))
		}
	}
	if !refresh {
		return failWith(exitTimescaleBroken,
			"the restored event store has %d background job(s) on %q but no policy_refresh_continuous_aggregate. %s would stop advancing, and real-time aggregation would hide that behind correct answers",
			len(jobs), areaEvent, rollupView)
	}
	if len(unplanned) > 0 {
		return failWith(exitTimescaleBroken,
			"%d of %d background job(s) have no next run planned: %s. The rows are restored and the scheduler is not running them",
			len(unplanned), len(jobs), strings.Join(unplanned, ", "))
	}
	fmt.Printf("ok   %d background job(s) restored, all scheduled with a next run\n", len(jobs))
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
	if err := db.WithContext(ctx).Exec(`
		DELETE FROM "`+areaEvent+`".measurement_events WHERE tenant_id = ? AND device_token = ?`,
		seed.Tenant, probe).Error; err != nil {
		return failWith(exitSetup, "cleaning up the post-restore probe row: %w", err)
	}
	fmt.Printf("ok   the restored cluster accepts new telemetry (probe written, read back and removed)\n")
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
