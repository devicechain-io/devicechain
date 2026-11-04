// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build purgemeasure

// MEASUREMENT 3: how long does a tenant deletion take for a LARGE tenant, per store.
//
// Measurements 1 and 2 (event-management/model/tenant_purge_{cost,rollup}_test.go) priced the
// telemetry DELETE and the rollup hole in isolation. Neither says what the whole erasure costs
// a customer, which is the number a data-processing agreement quotes. This drives the REAL
// purge stores (the relational sweep, the telemetry sweep, the broker, the key-value buckets
// and the object store) over a seeded large tenant on real servers, and times each one.
//
// It is a measuring instrument, not a pass/fail test, and it is therefore behind a build tag
// that CI never sets. Its one assertion is that the thing it timed actually happened: the
// victim's rows are gone from every store and the bystanders' are untouched. A timing of a
// sweep that deleted nothing, or everything, is the confident wrong number this repository
// has been burned by before (see the harness notes in tenant_purge_cost_test.go).
//
// Run it with hack/purge-measure.sh (go test -tags purgemeasure -run TestPurgeMeasurement), which starts uniquely named throwaway TimescaleDB and
// NATS containers, sets the scale, and removes only what it started.
//
// What it does NOT time:
//
//   - The DETECT store. It asks a running event-processing engine to evict in-memory state
//     and waits up to messaging.DetectPurgeWindow for the answer; there is no engine here.
//     Its cost is bounded by that window, not by tenant size.
//   - The settle window and the token hold. They are configuration (300 s and 43200 s by
//     default), added to the erase time by the report so the projection is explicit.
//   - A cluster. Everything is single-node and on loopback, so network round trips are
//     near zero and replication (R3 for JetStream) is absent. The report says where that
//     understates the cost.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/purge"
)

const (
	pmRdbDB     = "dcmeasure_rdb"
	pmTsdbDB    = "dcmeasure_tsdb"
	pmInstance  = "measure"
	pmVictim    = "victim-big"
	pmBystander = "bystander-" // + 0..2
)

func pmInt(t *testing.T, key string, def int) int {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	require.NoErrorf(t, err, "%s must be an integer", key)
	return n
}

// pmTenant is the SQL expression choosing a row's tenant from its series index g: one row
// in ten belongs to one of three bystanders, interleaved with the victim's. Interleaving is
// the point. A seed that gave the victim its own contiguous range would hand the sweep its
// own pages and chunks and measure a topology production does not have.
const pmTenant = `CASE WHEN g % 10 = 0 THEN 'bystander-' || ((g / 10) % 3) ELSE 'victim-big' END`

type pmStep struct {
	Store    string  `json:"store"`
	Attempt  int     `json:"attempt"`
	Seconds  float64 `json:"seconds"`
	Rows     int64   `json:"rows"`
	WalMiB   float64 `json:"walMiB,omitempty"`
	Deferred string  `json:"deferred,omitempty"`
	Error    string  `json:"error,omitempty"`
	Note     string  `json:"note,omitempty"`
}

type pmReport struct {
	When       time.Time          `json:"when"`
	Go         string             `json:"go"`
	CPUs       int                `json:"cpus"`
	Scale      map[string]int     `json:"scale"`
	Compress   bool               `json:"compressed"`
	Seeded     map[string]int     `json:"victimRowsSeeded"`
	SeedSecs   map[string]float64 `json:"seedSeconds"`
	SizesMiB   map[string]float64 `json:"sizesMiB"`
	Server     map[string]string  `json:"server"`
	Steps      []pmStep           `json:"steps"`
	FirstClean map[string]float64 `json:"firstCleanSeconds"`
	TotalErase float64            `json:"totalEraseSeconds"`
}

func pmExec(t *testing.T, db *gorm.DB, label string, rep *pmReport, sql string, args ...any) {
	t.Helper()
	start := time.Now()
	require.NoErrorf(t, db.Exec(sql, args...).Error, "seeding %s", label)
	rep.SeedSecs[label] = time.Since(start).Seconds()
}

func pmScalar(t *testing.T, db *gorm.DB, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoErrorf(t, db.Raw(sql, args...).Scan(&n).Error, "%s", sql)
	return n
}

func pmWal(t *testing.T, db *gorm.DB) string {
	var s string
	require.NoError(t, db.Raw("SELECT pg_current_wal_lsn()::text").Scan(&s).Error)
	return s
}

func pmWalDiff(t *testing.T, db *gorm.DB, a, b string) float64 {
	var n int64
	require.NoError(t, db.Raw("SELECT pg_wal_lsn_diff(?::pg_lsn, ?::pg_lsn)::bigint", b, a).Scan(&n).Error)
	return float64(n) / (1 << 20)
}

// pmManager opens an RdbManager on a database the way migrateSome does, but keeps it open
// so the event-management lifecycle policies can be applied through the production path.
func pmManager(t *testing.T, ctx context.Context, host string, port int, dbName string, a area) *rdb.RdbManager {
	t.Helper()
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: dbName, FunctionalArea: a.name},
		Migrations:   a.migrations,
		InstanceConfig: config.DatastoreConfiguration{
			Type: "timescaledb",
			Configuration: map[string]interface{}{
				"hostname": host, "port": port,
				"username": "postgres", "password": "postgres", "maxConnections": 10,
			},
		},
		MicroserviceConfig: config.MicroserviceDatastoreConfiguration{},
	}
	require.NoError(t, mgr.ExecuteInitialize(ctx))
	t.Cleanup(func() {
		if mgr.Database != nil {
			if s, err := mgr.Database.DB(); err == nil {
				_ = s.Close()
			}
		}
	})
	return mgr
}

type pmConn struct{ nc *nats.Conn }

func (c pmConn) Conn() *nats.Conn { return c.nc }

// pmTenants is the tenant reader the object store needs. The victim has no branding logo,
// so the object store's work is the prefix sweep alone, which is where the objects are.
type pmTenants struct{}

func (pmTenants) TenantByToken(_ context.Context, token string) (*iam.Tenant, error) {
	return &iam.Tenant{Token: token}, nil
}
func (pmTenants) UpdateTenantFields(context.Context, *iam.Tenant, map[string]any) error { return nil }

func TestPurgeMeasurement(t *testing.T) {
	ctx := context.Background()
	devices := pmInt(t, "PM_DEVICES", 10000)
	events := pmInt(t, "PM_EVENTS", 1000000)
	spanDays := pmInt(t, "PM_SPAN_DAYS", 30)
	brokerMsgs := pmInt(t, "PM_BROKER_MSGS", events)
	kvPer := pmInt(t, "PM_KV_KEYS_PER_DEVICE", 1)
	blobs := pmInt(t, "PM_BLOBS", 20000)
	compress := pmInt(t, "PM_COMPRESS", 1) == 1
	port := pmInt(t, "DC_IT_PGPORT", 5432)
	host := os.Getenv("DC_IT_PGHOST")
	if host == "" {
		host = "localhost"
	}
	natsURL := os.Getenv("PM_NATS_URL")
	require.NotEmpty(t, natsURL, "PM_NATS_URL is required (hack/purge-measure.sh sets it)")

	rep := &pmReport{
		When: time.Now().UTC(), Go: runtime.Version(), CPUs: runtime.NumCPU(),
		Scale: map[string]int{"devices": devices, "events": events, "spanDays": spanDays,
			"brokerMsgs": brokerMsgs, "kvKeysPerDevicePerBucket": kvPer, "blobs": blobs},
		Compress: compress,
		Seeded:   map[string]int{}, SeedSecs: map[string]float64{}, SizesMiB: map[string]float64{},
		Server: map[string]string{}, FirstClean: map[string]float64{},
	}

	// ---- databases: the relational cluster and the telemetry cluster, as two databases ----
	admin, err := openDatabase(host, port, "postgres", "postgres", "postgres")
	require.NoError(t, err)
	t.Cleanup(func() { closeDatabase(admin) })
	for _, d := range []string{pmRdbDB, pmTsdbDB} {
		require.NoError(t, admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, d)).Error)
	}
	var emArea area
	for _, a := range areas {
		if a.name == "event-management" {
			emArea = a
			continue
		}
		require.NoErrorf(t, migrateChain(ctx, a, host, port, "postgres", "postgres", pmRdbDB), "migrating %s", a.name)
	}
	require.NoError(t, migrateChain(ctx, emArea, host, port, "postgres", "postgres", pmTsdbDB))

	rdbConn, err := openDatabase(host, port, "postgres", "postgres", pmRdbDB)
	require.NoError(t, err)
	t.Cleanup(func() { closeDatabase(rdbConn) })
	tsConn, err := openDatabase(host, port, "postgres", "postgres", pmTsdbDB)
	require.NoError(t, err)
	t.Cleanup(func() { closeDatabase(tsConn) })

	for _, q := range []string{"server_version", "shared_buffers", "max_wal_size", "synchronous_commit"} {
		var v string
		require.NoError(t, admin.Raw("SHOW "+q).Scan(&v).Error)
		rep.Server[q] = v
	}
	var tsver string
	require.NoError(t, tsConn.Raw(`SELECT extversion FROM pg_extension WHERE extname='timescaledb'`).Scan(&tsver).Error)
	rep.Server["timescaledb"] = tsver

	// The lifecycle policies are applied BEFORE seeding, through the production function,
	// so the chunk interval governs the chunks the seed creates.
	tsMgr := pmManager(t, ctx, host, port, pmTsdbDB, emArea)
	require.NoError(t, model.ApplyDataLifecyclePolicies(ctx, tsMgr, model.DataLifecyclePolicy{
		ChunkIntervalHours: 24, CompressAfterDays: 7,
	}))

	// ---- seed the relational store ----
	seed := func(db *gorm.DB, label string, n int, body string) {
		pmExec(t, db, label, rep, fmt.Sprintf(body, pmTenant), n)
	}
	seed(rdbConn, "rdb.devices", devices, `
INSERT INTO "device-management".devices (id, tenant_id, token, name, created_at, updated_at)
SELECT g, %s, 'dev-' || g, 'device ' || g, now(), now() FROM generate_series(1, ?) g`)
	seed(rdbConn, "rdb.device_credentials", devices, `
INSERT INTO "device-management".device_credentials (id, tenant_id, token, device_id, credential_type, credential_id, enabled)
SELECT g, %s, 'cred-' || g, g, 'token', 'cid-' || g, true FROM generate_series(1, ?) g`)
	seed(rdbConn, "rdb.entity_attributes", devices*3, `
INSERT INTO "device-management".entity_attributes (id, tenant_id, entity_type, entity_id, scope, attr_key, value_type, value, last_updated)
SELECT g, %s, 'device', g / 3 + 1, 'server', 'attr' || (g %% 3), 'string', 'value-' || g, now() FROM generate_series(1, ?) g`)
	seed(rdbConn, "rdb.device_states", devices, `
INSERT INTO "device-state".device_states (id, tenant_id, device_token, active, inactivity_timeout, presence_source, session_id)
SELECT g, %s, 'dev-' || g, true, 600, 'INFERRED', 1 FROM generate_series(1, ?) g`)
	seed(rdbConn, "rdb.commands", devices*5, `
INSERT INTO "command-delivery".commands (id, tenant_id, token, device_token, name, status, dispatch_failures)
SELECT g, %s, 'cmd-' || g, 'dev-' || (g %% `+strconv.Itoa(devices)+` + 1), 'reboot', 'DELIVERED', 0 FROM generate_series(1, ?) g`)
	seed(rdbConn, "rdb.device_rosters", devices, `
INSERT INTO "event-processing".device_rosters (tenant, device_token, profile_token, expected_since, deleted, last_event_at)
SELECT %s, 'dev-' || g, 'profile', now(), false, now() FROM generate_series(1, ?) g`)
	for _, a := range []string{"device-management", "command-delivery", "device-state"} {
		seed(rdbConn, "rdb.audit_events."+a, devices*2, `
INSERT INTO "`+a+`".audit_events (id, occurred_time, tenant_id, category, actor, table_name, operation, entity_pk, entity_label, rows_affected)
SELECT 1000000000 + g, now() - (g || ' seconds')::interval, %s, 'mutation', 'user-' || (g %% 50) || '@example.test', 'devices', 'create', g::text, 'device ' || g, 1
FROM generate_series(1, ?) g`)
	}

	// ---- seed the telemetry store ----
	step := fmt.Sprintf("(%d * 86400.0 / ?)", spanDays)
	evSeed := func(label string, n int, body string) {
		pmExec(t, tsConn, label, rep, fmt.Sprintf(body, pmTenant, step, devices), n, n)
	}
	evSeed("tsdb.events", events, `
INSERT INTO "event-management".events (tenant_id, event_id, device_token, event_type, occurred_time, source, processed_time)
SELECT %s, sha256(convert_to('e:' || g::text, 'UTF8')), 'dev-' || (g %% %[3]d + 1), 1,
       now() - interval '`+strconv.Itoa(spanDays)+` days' + (g * %[2]s * interval '1 second'), 'mqtt', now()
FROM generate_series(1, ?) g`)
	mm := events * 9 / 10
	pmExec(t, tsConn, "tsdb.measurement_events", rep, fmt.Sprintf(`
INSERT INTO "event-management".measurement_events (tenant_id, event_id, payload_id, device_token, event_type, occurred_time, name, value, unit, data_type)
SELECT %s, sha256(convert_to('e:' || g::text, 'UTF8')), sha256(convert_to('p:' || g::text, 'UTF8')), 'dev-' || (g %% %d + 1), 1,
       now() - interval '`+strconv.Itoa(spanDays)+` days' + (g * %s * interval '1 second'), 'temp' || (g %% 4), 20 + (g %% 150) / 10.0, 'C', 'DOUBLE'
FROM generate_series(1, ?) g`, pmTenant, devices, fmt.Sprintf("(%d * 86400.0 / ?)", spanDays)), mm, mm)
	loc := events / 10
	pmExec(t, tsConn, "tsdb.location_events", rep, fmt.Sprintf(`
INSERT INTO "event-management".location_events (tenant_id, event_id, payload_id, device_token, event_type, occurred_time, latitude, longitude)
SELECT %s, sha256(convert_to('l:' || g::text, 'UTF8')), sha256(convert_to('lp:' || g::text, 'UTF8')), 'dev-' || (g %% %d + 1), 2,
       now() - interval '`+strconv.Itoa(spanDays)+` days' + (g * %s * interval '1 second'), 40.0 + (g %% 100) / 1000.0, -75.0 - (g %% 100) / 1000.0
FROM generate_series(1, ?) g`, pmTenant, devices, fmt.Sprintf("(%d * 86400.0 / ?)", spanDays)), loc, loc)
	anch := events / 10
	pmExec(t, tsConn, "tsdb.event_anchors", rep, fmt.Sprintf(`
INSERT INTO "event-management".event_anchors (tenant_id, event_id, device_token, event_type, occurred_time, anchor_type, anchor_token)
SELECT %s, sha256(convert_to('a:' || g::text, 'UTF8')), 'dev-' || (g %% %d + 1), 1,
       now() - interval '`+strconv.Itoa(spanDays)+` days' + (g * %s * interval '1 second'), 'area', 'area-' || (g %% 200)
FROM generate_series(1, ?) g`, pmTenant, devices, fmt.Sprintf("(%d * 86400.0 / ?)", spanDays)), anch, anch)

	// Quiesce the background jobs. The refresh policy fires on its own schedule and a manual
	// refresh that meets it fails with "concurrent refresh" (it did, on the first 10M run); the
	// compression policy would race the manual compression below. The measurement drives both
	// by hand so that what it times is the purge and nothing else sharing the server.
	// The aggregate's refresh job reports its hypertable as the internal materialization, not
	// the event-management table, so select the jobs by what they run rather than by schema.
	require.NoError(t, tsConn.Exec(`SELECT delete_job(job_id) FROM timescaledb_information.jobs
		WHERE proc_name IN ('policy_refresh_continuous_aggregate', 'policy_compression', 'policy_retention')
		   OR hypertable_schema = 'event-management'`).Error)

	// The rollup hole: materialize the aggregate over the whole range, as the refresh policy
	// would have, so the purge has the buckets measurement 2 showed it must remove.
	start := time.Now()
	require.NoError(t, tsConn.Exec(`CALL refresh_continuous_aggregate('"event-management".measurement_rollups', NULL, NULL)`).Error)
	rep.SeedSecs["tsdb.refresh_rollups"] = time.Since(start).Seconds()

	if compress {
		start = time.Now()
		for _, tbl := range model.LifecycleHypertables {
			require.NoError(t, tsConn.Exec(fmt.Sprintf(
				`SELECT compress_chunk(c, if_not_compressed => true) FROM show_chunks('"event-management".%q', older_than => now() - interval '7 days') c`, tbl)).Error)
		}
		rep.SeedSecs["tsdb.compress"] = time.Since(start).Seconds()
	}

	// ---- seed the broker ----
	nc, err := nats.Connect(natsURL, nats.MaxReconnects(-1))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream(nats.PublishAsyncMaxPending(4096))
	require.NoError(t, err)
	var hot []string
	for _, s := range streams.All {
		if s.Shape == streams.ShapeAdvisory {
			continue
		}
		_, err := js.AddStream(&nats.StreamConfig{
			Name: messaging.StreamName(pmInstance, s.Suffix), Subjects: messaging.StreamSubjects(pmInstance, s.Suffix),
			Storage: nats.FileStorage, Retention: nats.LimitsPolicy,
		})
		require.NoErrorf(t, err, "stream %s", s.Suffix)
		if s.Suffix == streams.InboundEvents || s.Suffix == streams.ResolvedEvents || s.Suffix == streams.DeviceEventsCapture {
			hot = append(hot, s.Suffix)
		}
	}
	sort.Strings(hot)
	start = time.Now()
	payload := []byte(strings.Repeat("x", 220))
	var victimPublished int
	for i := 0; i < brokerMsgs; i++ {
		tenant := pmVictim
		if i%10 == 0 {
			tenant = fmt.Sprintf("%s%d", pmBystander, (i/10)%3)
		} else {
			victimPublished++
		}
		suffix := hot[i%len(hot)]
		subject := messaging.ConcreteSubjectFor(pmInstance, tenant, suffix, fmt.Sprintf("dev-%d", i%devices+1))
		for {
			_, err := js.PublishAsync(subject, payload)
			if err == nil {
				break
			}
			// The client refuses to queue past its pending window rather than block; wait
			// for the broker to catch up and offer the same message again.
			require.ErrorIs(t, err, nats.ErrTooManyStalledMsgs)
			time.Sleep(50 * time.Millisecond)
		}
	}
	select {
	case <-js.PublishAsyncComplete():
	case <-time.After(30 * time.Minute):
		t.Fatal("publishing to the broker did not complete")
	}
	rep.SeedSecs["broker.publish"] = time.Since(start).Seconds()
	rep.Seeded["broker.messages"] = victimPublished

	// ---- seed key-value ----
	start = time.Now()
	var kvKeys, kvTotal int
	for _, stream := range messaging.TenantScopedBucketStreams(pmInstance) {
		bucket := strings.TrimPrefix(stream, "KV_")
		store, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: bucket, History: 1})
		require.NoErrorf(t, err, "bucket %s", bucket)
		for d := 1; d <= devices; d++ {
			for k := 0; k < kvPer; k++ {
				tenant := pmVictim
				if d%10 == 0 {
					tenant = fmt.Sprintf("%s%d", pmBystander, (d/10)%3)
				} else {
					kvKeys++
				}
				kvTotal++
				key := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s|dev-%d|%d", tenant, d, k)))
				_, err := store.Put(key, []byte("v"))
				require.NoError(t, err)
			}
		}
	}
	rep.SeedSecs["kv.put"] = time.Since(start).Seconds()
	rep.Seeded["kv.keys"] = kvKeys

	// ---- seed the object store ----
	blobDir := t.TempDir()
	objects, err := blob.NewFilesystemStore(blob.Config{Backend: blob.BackendFilesystem, Directory: blobDir}, pmInstance)
	require.NoError(t, err)
	start = time.Now()
	var victimBlobs int
	for i := 0; i < blobs; i++ {
		tenant := pmVictim
		if i%10 == 0 {
			tenant = fmt.Sprintf("%s%d", pmBystander, (i/10)%3)
		} else {
			victimBlobs++
		}
		_, err := objects.Put(ctx, blob.Key{Tenant: tenant, Purpose: "firmware", ID: fmt.Sprintf("fw-%d.bin", i)},
			strings.NewReader(strings.Repeat("f", 1024)), blob.PutOptions{})
		require.NoError(t, err)
	}
	rep.SeedSecs["blob.put"] = time.Since(start).Seconds()
	rep.Seeded["blob.objects"] = victimBlobs

	// ---- what was seeded, counted by the database rather than assumed from the loops ----
	count := func(db *gorm.DB, table, col, tenant string) int64 {
		return pmScalar(t, db, fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s = ?`, table, col), tenant)
	}
	type loc2 struct {
		db         *gorm.DB
		table, col string
	}
	locs := []loc2{
		{rdbConn, `"device-management".devices`, "tenant_id"},
		{rdbConn, `"device-management".entity_attributes`, "tenant_id"},
		{rdbConn, `"command-delivery".commands`, "tenant_id"},
		{rdbConn, `"device-management".audit_events`, "tenant_id"},
		{tsConn, `"event-management".events`, "tenant_id"},
		{tsConn, `"event-management".measurement_events`, "tenant_id"},
		{tsConn, `"event-management".location_events`, "tenant_id"},
		{tsConn, `"event-management".event_anchors`, "tenant_id"},
	}
	before := map[string][2]int64{}
	for _, l := range locs {
		v, b := count(l.db, l.table, l.col, pmVictim), count(l.db, l.table, l.col, pmBystander+"0")
		require.NotZerof(t, v, "%s has no victim rows: the timing below would be of nothing", l.table)
		require.NotZerof(t, b, "%s has no bystander rows: an over-broad delete would go unseen", l.table)
		before[l.table] = [2]int64{v, b}
		rep.Seeded[l.table] = int(v)
	}
	mat := materializationOf(t, tsConn, "event-management", "measurement_rollups")
	matVictim := count(tsConn, mat, "tenant_id", pmVictim)
	require.NotZero(t, matVictim, "the rollup materialization holds no victim rows: the purge's rollup step would be unmeasured")
	rep.Seeded["tsdb.measurement_rollups(materialized)"] = int(matVictim)
	for _, sch := range []string{"event-management"} {
		var mib float64
		require.NoError(t, tsConn.Raw(`SELECT coalesce(sum(hypertable_size(format('%I.%I', hypertable_schema, hypertable_name)::regclass)),0)/1048576.0 FROM timescaledb_information.hypertables WHERE hypertable_schema = ?`, sch).Scan(&mib).Error)
		rep.SizesMiB["tsdb.hypertables"] = mib
	}
	var rdbMiB float64
	require.NoError(t, rdbConn.Raw(`SELECT pg_database_size(current_database())/1048576.0`).Scan(&rdbMiB).Error)
	rep.SizesMiB["rdb.database"] = rdbMiB
	var chunks, compressedChunks int64
	chunks = pmScalar(t, tsConn, `SELECT count(*) FROM timescaledb_information.chunks WHERE hypertable_name = 'measurement_events'`)
	compressedChunks = pmScalar(t, tsConn, `SELECT count(*) FROM timescaledb_information.chunks WHERE hypertable_name = 'measurement_events' AND is_compressed`)
	rep.Seeded["tsdb.measurement_events.chunks"] = int(chunks)
	rep.Seeded["tsdb.measurement_events.chunks_compressed"] = int(compressedChunks)

	// ---- the erase: each store, as the coordinator would ask, until it reports clean ----
	stores := []purge.Store{
		purge.NewRelational(purge.StoreRelational, drillDatabase{rdbConn}, nil),
		purge.NewRelational(purge.StoreTelemetry, drillDatabase{tsConn}, nil),
		purge.NewBroker(pmConn{nc}, pmInstance),
		purge.NewKeyValue(pmConn{nc}, pmInstance),
		purge.NewObject(objects, pmTenants{}),
	}
	walDB := map[string]*gorm.DB{purge.StoreRelational: rdbConn, purge.StoreTelemetry: tsConn}
	epoch := time.Now().UTC()
	for _, s := range stores {
		var firstClean bool
		for attempt := 1; attempt <= 8; attempt++ {
			var w0 string
			if db, ok := walDB[s.Name()]; ok {
				w0 = pmWal(t, db)
			}
			t0 := time.Now()
			out, err := s.Erase(ctx, pmVictim, epoch)
			st := pmStep{Store: s.Name(), Attempt: attempt, Seconds: time.Since(t0).Seconds(), Rows: out.Rows,
				Deferred: out.Reason(), Note: out.Note()}
			if db, ok := walDB[s.Name()]; ok {
				st.WalMiB = pmWalDiff(t, db, w0, pmWal(t, db))
			}
			if err != nil {
				st.Error = err.Error()
			}
			rep.Steps = append(rep.Steps, st)
			t.Logf("%-7s attempt %d: %8.2fs rows=%d wal=%.0fMiB deferred=%q err=%q", st.Store, attempt, st.Seconds, st.Rows, st.WalMiB, st.Deferred, st.Error)
			if err == nil && out.Clean() {
				if !firstClean {
					// The pass that first reported clean carries the cost of the erasure. Time
					// from the start of the store's first attempt, so a store that needed
					// retries (a broker request that timed out) is charged for the failures.
					var sum float64
					for _, p := range rep.Steps {
						if p.Store == s.Name() {
							sum += p.Seconds
						}
					}
					rep.FirstClean[s.Name()] = sum
					firstClean = true
					continue // one more pass: what the steady-state tick costs on a clean store
				}
				break
			}
			if attempt == 8 {
				t.Errorf("%s never reported clean in 8 attempts", s.Name())
			}
		}
	}
	for _, d := range rep.FirstClean {
		rep.TotalErase += d
	}

	// ---- the assertion that makes the timings mean something ----
	for _, l := range locs {
		require.Zerof(t, count(l.db, l.table, l.col, pmVictim), "%s still holds victim rows after the purge", l.table)
		require.Equalf(t, before[l.table][1], count(l.db, l.table, l.col, pmBystander+"0"), "%s lost bystander rows", l.table)
	}
	require.Zero(t, count(tsConn, mat, "tenant_id", pmVictim), "victim rollup buckets survived")
	require.NotZero(t, count(tsConn, mat, "tenant_id", pmBystander+"0"), "bystander rollup buckets were deleted")
	for _, stream := range messaging.TenantScopedBucketStreams(pmInstance) {
		info, err := js.StreamInfo(stream, &nats.StreamInfoRequest{SubjectsFilter: ">"})
		require.NoError(t, err)
		for subj := range info.State.Subjects {
			enc := subj[strings.LastIndex(subj, ".")+1:]
			raw, _ := base64.RawURLEncoding.DecodeString(enc)
			require.Falsef(t, strings.HasPrefix(string(raw), pmVictim+"|"), "victim key survived in %s", stream)
		}
		require.NotZero(t, info.State.Msgs, "bystander keys were deleted from %s", stream)
	}
	for _, s := range streams.All {
		if s.Shape == streams.ShapeAdvisory {
			continue
		}
		name := messaging.StreamName(pmInstance, s.Suffix)
		for _, f := range messaging.TenantSubjectFilters(pmInstance, pmVictim, s.Suffix) {
			info, err := js.StreamInfo(name, &nats.StreamInfoRequest{SubjectsFilter: f})
			require.NoError(t, err)
			require.Emptyf(t, info.State.Subjects, "victim messages survived in %s", name)
		}
	}
	remaining, err := objects.DeleteTenant(ctx, pmVictim)
	require.NoError(t, err)
	require.Zero(t, remaining, "victim objects survived the object store's erase")
	left, err := objects.DeleteTenant(ctx, pmBystander+"0")
	require.NoError(t, err)
	require.NotZero(t, left, "bystander objects were deleted (and have now been, by this check)")

	out, _ := json.MarshalIndent(rep, "", "  ")
	t.Logf("REPORT\n%s", out)
	if p := os.Getenv("PM_REPORT"); p != "" {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, out, 0o644))
	}
}
