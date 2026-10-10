// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// valuesWrite is the event store's batch write as it was before the column-array insert:
// one gorm multi-row INSERT per table, split by rdb.CreateChunked, every statement with
// its ON CONFLICT arbiter, the parents deduplicated on their event id first. It is kept
// here, verbatim in behaviour, as the ORACLE the new path is compared against — so the
// comparison does not depend on the code it is checking.
func valuesWrite(ctx context.Context, db *gorm.DB, rows *EventRows, anchors []*EventAnchor) error {
	db = db.WithContext(ctx)
	if len(rows.Parents) > 0 {
		seen := map[string]struct{}{}
		var distinct []*Event
		for _, e := range rows.Parents {
			if _, ok := seen[string(e.EventId)]; ok {
				continue
			}
			seen[string(e.EventId)] = struct{}{}
			distinct = append(distinct, e)
		}
		if err := rdb.CreateChunked(db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "event_id"}, {Name: "occurred_time"}},
			DoNothing: true,
		}), distinct).Error; err != nil {
			return err
		}
	}
	payload := clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "payload_id"}, {Name: "occurred_time"}},
		DoNothing: true,
	}
	if len(rows.Locations) > 0 {
		if err := rdb.CreateChunked(db.Clauses(payload), &rows.Locations).Error; err != nil {
			return err
		}
	}
	if len(rows.Measurements) > 0 {
		if err := rdb.CreateChunked(db.Clauses(payload), &rows.Measurements).Error; err != nil {
			return err
		}
	}
	if len(rows.Alerts) > 0 {
		if err := rdb.CreateChunked(db.Clauses(payload), &rows.Alerts).Error; err != nil {
			return err
		}
	}
	if len(anchors) > 0 {
		return rdb.CreateChunked(db.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "tenant_id"}, {Name: "event_id"}, {Name: "occurred_time"},
				{Name: "anchor_type"}, {Name: "anchor_token"},
			},
			DoNothing: true,
		}), anchors).Error
	}
	return nil
}

// columnarBatch is one randomized batch of requests, built fresh on every call to rows so
// each path writes its own structs (both paths stamp the tenant onto what they are given).
type columnarBatch struct {
	measurements [][]*MeasurementEventCreateRequest // per event
	locations    [][]*LocationEventCreateRequest
	alerts       [][]*AlertEventCreateRequest
	anchors      [][]*EventAnchor
}

func ptr[T any](v T) *T { return &v }

// awkwardValues are the measurement values whose stored form depends on exactly how the
// value reached the server: rounding at the column's scale (half-way cases, a subnormal,
// a value below the scale), the largest magnitudes the column holds, and signs. (NaN and
// the infinities never reach the store: a payload's identity cannot be derived for them.)
var awkwardValues = []*float64{
	nil, ptr(0.0), ptr(math.Copysign(0, -1)), ptr(-1.5), ptr(123456789012.12345678),
	ptr(-999999999999.9), ptr(0.123456785), ptr(0.123456775), ptr(-0.000000015), ptr(5e-324),
	ptr(math.SmallestNonzeroFloat64 * 1e10), ptr(1.0 / 3), ptr(2.0 / 3), ptr(1e-9), ptr(99.999999995),
	ptr(42.0),
}

func newColumnarBatch(rng *rand.Rand, base time.Time) *columnarBatch {
	b := &columnarBatch{}
	events := 1 + rng.Intn(64)
	for e := 0; e < events; e++ {
		at := base.Add(time.Duration(rng.Int63n(int64(time.Hour)))).Truncate(time.Microsecond)
		device := fmt.Sprintf("dev-%d", rng.Intn(8))
		var altId *string
		if rng.Intn(3) == 0 {
			altId = ptr(fmt.Sprintf("alt-%d-%d", e, rng.Int63()))
		}
		parent := func(kind esmodel.EventType) Event {
			id := sha256.Sum256([]byte(fmt.Sprintf("%d/%d/%d", e, kind, rng.Int63())))
			return Event{EventId: id[:], DeviceToken: device, EventType: kind, OccurredTime: at,
				Source: []string{"mqtt", "", "nats"}[rng.Intn(3)], AltId: rdb.NullStrOf(altId),
				ProcessedTime: at.Add(time.Duration(rng.Intn(5000)) * time.Microsecond)}
		}
		switch rng.Intn(3) {
		case 0:
			ev := parent(esmodel.Measurement)
			var reqs []*MeasurementEventCreateRequest
			for m := 0; m < 1+rng.Intn(21); m++ {
				var classifier *uint
				if rng.Intn(2) == 0 {
					classifier = ptr(uint(rng.Intn(1000)))
				}
				var unit, dataType *string
				if rng.Intn(2) == 0 {
					unit, dataType = ptr("°C"), ptr("float")
				}
				reqs = append(reqs, &MeasurementEventCreateRequest{Event: ev,
					EntryOccurredTime: at.Add(-time.Duration(m) * time.Second),
					Name:              fmt.Sprintf("m%d", m), Value: awkwardValues[rng.Intn(len(awkwardValues))],
					Classifier: classifier, Unit: unit, DataType: dataType})
			}
			// A duplicate reading inside the batch: the same row twice in one statement.
			if rng.Intn(4) == 0 {
				dup := *reqs[0]
				reqs = append(reqs, &dup)
			}
			b.measurements = append(b.measurements, reqs)
		case 1:
			ev := parent(esmodel.Location)
			f := func() *float64 {
				if rng.Intn(4) == 0 {
					return nil
				}
				return ptr(rng.Float64()*170 - 85)
			}
			b.locations = append(b.locations, []*LocationEventCreateRequest{{Event: ev, EntryOccurredTime: at,
				Latitude: f(), Longitude: f(), Elevation: f(), Accuracy: f(), Speed: f(), Heading: f()}})
		default:
			ev := parent(esmodel.Alert)
			b.alerts = append(b.alerts, []*AlertEventCreateRequest{{Event: ev, EntryOccurredTime: at,
				Type: "overheat", Level: uint32(rng.Intn(5)), Message: "hot <&>", Source: "rule"}})
		}
		var anchors []*EventAnchor
		for a := 0; a < rng.Intn(3); a++ {
			anchors = append(anchors, &EventAnchor{EventId: []byte(fmt.Sprintf("anchor-ev-%d", e)),
				DeviceToken: device, EventType: esmodel.Measurement, OccurredTime: at,
				AnchorType: []string{"customer", "area", "asset"}[a], AnchorToken: fmt.Sprintf("tok-%d", rng.Intn(4))})
		}
		b.anchors = append(b.anchors, anchors)
	}
	return b
}

// copied returns fresh copies of the batch's structs, so a write stamping its tenant onto
// them cannot reach the next one.
func copied[T any](in []*T) []*T {
	out := make([]*T, len(in))
	for i, v := range in {
		c := *v
		out[i] = &c
	}
	return out
}

// rows builds the batch's rows afresh, as the persistence writer does.
func (b *columnarBatch) rows(t *testing.T) (*EventRows, []*EventAnchor) {
	t.Helper()
	rows := &EventRows{}
	for _, reqs := range b.measurements {
		var cp []*MeasurementEventCreateRequest
		for _, r := range reqs {
			c := *r
			cp = append(cp, &c)
		}
		parents, ms, err := BuildMeasurementRows(cp)
		require.NoError(t, err)
		rows.Parents, rows.Measurements = append(rows.Parents, parents...), append(rows.Measurements, ms...)
	}
	for _, reqs := range b.locations {
		var cp []*LocationEventCreateRequest
		for _, r := range reqs {
			c := *r
			cp = append(cp, &c)
		}
		parents, ls, err := BuildLocationRows(cp)
		require.NoError(t, err)
		rows.Parents, rows.Locations = append(rows.Parents, parents...), append(rows.Locations, ls...)
	}
	for _, reqs := range b.alerts {
		var cp []*AlertEventCreateRequest
		for _, r := range reqs {
			c := *r
			cp = append(cp, &c)
		}
		parents, as, err := BuildAlertRows(cp)
		require.NoError(t, err)
		rows.Parents, rows.Alerts = append(rows.Parents, parents...), append(rows.Alerts, as...)
	}
	var anchors []*EventAnchor
	for _, as := range b.anchors {
		for _, a := range as {
			c := *a
			anchors = append(anchors, &c)
		}
	}
	return rows, anchors
}

var columnarTables = []string{
	"events", "location_events", "measurement_events", "alert_events", "event_anchors",
}

// storedFor returns every row of table stored for tenant, each column but tenant_id
// rendered by the server as text (numeric::text is the stored decimal, exactly), sorted.
func storedFor(t *testing.T, db *gorm.DB, table, tenant string) []string {
	t.Helper()
	sys := db.Session(&gorm.Session{NewDB: true}).WithContext(core.WithSystemContext(context.Background()))
	var cols []string
	require.NoError(t, sys.Raw(`SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'event-management' AND table_name = ? AND column_name <> 'tenant_id'
		ORDER BY ordinal_position`, table).Scan(&cols).Error)
	require.NotEmpty(t, cols, "table %s has no columns", table)
	exprs := make([]string, len(cols))
	for i, c := range cols {
		exprs[i] = fmt.Sprintf(`coalesce(%q::text, '<null>')`, c)
	}
	var out []string
	require.NoError(t, sys.Raw(fmt.Sprintf(`SELECT concat_ws(' | ', %s) FROM "event-management".%q WHERE tenant_id = ?`,
		strings.Join(exprs, ", "), table), tenant).Scan(&out).Error)
	sort.Strings(out)
	return out
}

func requireStoredEqual(t *testing.T, db *gorm.DB, want, got string) {
	t.Helper()
	for _, table := range columnarTables {
		a, b := storedFor(t, db, table, want), storedFor(t, db, table, got)
		require.Equalf(t, a, b, "table %s: tenant %s (the gorm VALUES write) and tenant %s stored different rows",
			table, want, got)
	}
}

func requireStampedWith(t *testing.T, tenant string, rows *EventRows, anchors []*EventAnchor) {
	t.Helper()
	seen := map[string]bool{}
	for _, p := range rows.Parents {
		if !seen[string(p.EventId)] { // only the first of a duplicated parent is written, and stamped
			require.Equal(t, tenant, p.TenantId, "a written parent was not stamped")
			seen[string(p.EventId)] = true
		}
	}
	for _, r := range rows.Measurements {
		require.Equal(t, tenant, r.TenantId)
	}
	for _, r := range rows.Locations {
		require.Equal(t, tenant, r.TenantId)
	}
	for _, r := range rows.Alerts {
		require.Equal(t, tenant, r.TenantId)
	}
	for _, a := range anchors {
		require.Equal(t, tenant, a.TenantId)
	}
}

// TestColumnarWriteStoresWhatValuesWriteStores writes the same randomized batches through
// the old multi-row VALUES write (valuesWrite, under one tenant) and through the event
// store's own write paths (under two others: the grouped CreateEventRows and the
// per-message Create*Events), each batch TWICE — the redelivery the ON CONFLICT arbiters
// exist for — and requires every column of every row, except the tenant, to be stored
// byte for byte the same by all three.
func TestColumnarWriteStoresWhatValuesWriteStores(t *testing.T) {
	api := newPostgresApi(t, "itcolumnar")
	db := api.RDB.Database
	seed := time.Now().UnixNano()
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewSource(seed))
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	const viaValues, viaGrouped, viaEach = "via-values", "via-grouped", "via-each"

	for round := 0; round < 12; round++ {
		batch := newColumnarBatch(rng, base.Add(time.Duration(round)*time.Hour))
		for delivery := 0; delivery < 2; delivery++ {
			rows, anchors := batch.rows(t)
			ctx := core.WithTenant(context.Background(), viaValues)
			require.NoError(t, db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				return valuesWrite(ctx, tx, rows, anchors)
			}), "round %d delivery %d: the VALUES write", round, delivery)
			requireStampedWith(t, viaValues, rows, anchors)

			rows, anchors = batch.rows(t)
			ctx = core.WithTenant(context.Background(), viaGrouped)
			require.NoError(t, api.PersistInTx(ctx, func(tx *gorm.DB) error {
				if err := api.CreateEventRows(ctx, tx, rows); err != nil {
					return err
				}
				return api.CreateEventAnchors(ctx, tx, anchors)
			}), "round %d delivery %d: CreateEventRows", round, delivery)
			requireStampedWith(t, viaGrouped, rows, anchors)

			// The per-message path: one Create*Events call per event, as PersistEvent makes.
			ctx = core.WithTenant(context.Background(), viaEach)
			each := api.RDB.DB(ctx)
			for _, reqs := range batch.measurements {
				created, err := api.CreateMeasurementEvents(ctx, each, copied(reqs))
				require.NoError(t, err)
				for _, c := range created {
					require.Equal(t, viaEach, c.TenantId, "a returned measurement row was not stamped")
				}
			}
			for _, reqs := range batch.locations {
				_, err := api.CreateLocationEvents(ctx, each, copied(reqs))
				require.NoError(t, err)
			}
			for _, reqs := range batch.alerts {
				_, err := api.CreateAlertEvents(ctx, each, copied(reqs))
				require.NoError(t, err)
			}
			for _, as := range batch.anchors {
				require.NoError(t, api.CreateEventAnchors(ctx, each, copied(as)))
			}
		}
	}
	requireStoredEqual(t, db, viaValues, viaGrouped)
	requireStoredEqual(t, db, viaValues, viaEach)
	for _, table := range columnarTables {
		require.NotEmptyf(t, storedFor(t, db, table, viaValues), "precondition: table %s got rows", table)
	}
}

// A value the column cannot hold is refused by the server the same way on both paths, and
// the batch carrying it stores nothing.
func TestColumnarWriteRefusesWhatValuesWriteRefuses(t *testing.T) {
	api := newPostgresApi(t, "itcolumnarrefuse")
	db := api.RDB.Database
	at := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	// numeric(20,8) holds twelve integer digits.
	for _, bad := range []float64{1e15 + 0.123456789, 1e12, -1e12} {
		build := func() *EventRows {
			ev := Event{EventId: []byte(fmt.Sprintf("bad-%v", bad)), DeviceToken: "d", EventType: esmodel.Measurement,
				OccurredTime: at, ProcessedTime: at}
			parents, ms, err := BuildMeasurementRows([]*MeasurementEventCreateRequest{
				{Event: ev, EntryOccurredTime: at, Name: "ok", Value: ptr(1.0)},
				{Event: ev, EntryOccurredTime: at, Name: "bad", Value: ptr(bad)},
			})
			require.NoError(t, err)
			return &EventRows{Parents: parents, Measurements: ms}
		}
		ctx := core.WithTenant(context.Background(), "via-values")
		oldErr := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return valuesWrite(ctx, tx, build(), nil) })
		ctx = core.WithTenant(context.Background(), "via-grouped")
		newErr := api.PersistInTx(ctx, func(tx *gorm.DB) error { return api.CreateEventRows(ctx, tx, build()) })
		require.Errorf(t, oldErr, "precondition: the VALUES write refuses %v", bad)
		require.Errorf(t, newErr, "the event store's write accepted %v, which the VALUES write refuses (%v)", bad, oldErr)
	}
	for _, table := range columnarTables {
		require.Empty(t, storedFor(t, db, table, "via-grouped"), "a refused batch stored rows in %s", table)
	}
}

// A row filed under another tenant is refused on both paths, and nothing of its batch is
// stored.
func TestColumnarWriteRefusesAMismatchedRow(t *testing.T) {
	api := newPostgresApi(t, "itcolumnarmismatch")
	db := api.RDB.Database
	at := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	build := func() *EventRows {
		ev := Event{EventId: []byte("mixed"), DeviceToken: "d", EventType: esmodel.Measurement,
			OccurredTime: at, ProcessedTime: at}
		parents, ms, err := BuildMeasurementRows([]*MeasurementEventCreateRequest{
			{Event: ev, EntryOccurredTime: at, Name: "a", Value: ptr(1.0)},
			{Event: ev, EntryOccurredTime: at, Name: "b", Value: ptr(2.0)},
		})
		require.NoError(t, err)
		ms[1].TenantId = "globex"
		return &EventRows{Parents: parents, Measurements: ms}
	}
	ctx := core.WithTenant(context.Background(), "acme")
	oldErr := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return valuesWrite(ctx, tx, build(), nil) })
	newErr := api.PersistInTx(ctx, func(tx *gorm.DB) error { return api.CreateEventRows(ctx, tx, build()) })
	require.ErrorIs(t, oldErr, rdb.ErrTenantMismatch, "precondition: the VALUES write refuses a mixed batch")
	require.ErrorIs(t, newErr, rdb.ErrTenantMismatch)
	for _, tenant := range []string{"acme", "globex"} {
		for _, table := range columnarTables {
			require.Empty(t, storedFor(t, db, table, tenant), "a refused batch stored rows in %s for %s", table, tenant)
		}
	}
}

// A write without a tenant in its context is refused before anything reaches the store —
// and so is one under a system context, which names no tenant either: the event store has
// no write that is legitimately instance-scoped, and a gorm Create under a system context
// would store the rows with an empty tenant.
func TestColumnarWriteRefusesNoTenant(t *testing.T) {
	api := newPostgresApi(t, "itcolumnarnotenant")
	at := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	ev := Event{EventId: []byte("orphan"), DeviceToken: "d", EventType: esmodel.Measurement, OccurredTime: at}
	parents, ms, err := BuildMeasurementRows([]*MeasurementEventCreateRequest{
		{Event: ev, EntryOccurredTime: at, Name: "a", Value: ptr(1.0)}})
	require.NoError(t, err)
	for _, ctx := range []context.Context{context.Background(), core.WithSystemContext(context.Background())} {
		err := api.PersistInTx(ctx, func(tx *gorm.DB) error {
			return api.CreateEventRows(ctx, tx, &EventRows{Parents: parents, Measurements: ms})
		})
		require.True(t, errors.Is(err, core.ErrNoTenant), "err = %v; want core.ErrNoTenant", err)
	}
}

// insertRecorder records, for every INSERT the event store issues, how many parameters it
// bound — from gorm's Create processor (a multi-row VALUES statement) and its Raw
// processor (a statement handed over as SQL) alike.
type insertRecorder struct {
	mu   sync.Mutex
	vars map[string][]int // table -> parameters bound, per statement
}

func recordInserts(t *testing.T, db *gorm.DB) *insertRecorder {
	t.Helper()
	r := &insertRecorder{vars: map[string][]int{}}
	record := func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if !strings.HasPrefix(sql, "INSERT INTO ") {
			return
		}
		for _, table := range columnarTables {
			if strings.HasPrefix(sql, `INSERT INTO "event-management"."`+table+`"`) {
				r.mu.Lock()
				r.vars[table] = append(r.vars[table], len(tx.Statement.Vars))
				r.mu.Unlock()
			}
		}
	}
	require.NoError(t, db.Callback().Create().After("gorm:create").Register("test:record_create", record))
	require.NoError(t, db.Callback().Raw().After("gorm:raw").Register("test:record_raw", record))
	return r
}

// TestEventStoreInsertsBindAFixedNumberOfParameters pins the point of the column-array
// insert: what a batch statement binds does not grow with the batch. A multi-row VALUES
// statement binds one parameter per value — 64 events of one metric bound 512 for the
// parents alone.
func TestEventStoreInsertsBindAFixedNumberOfParameters(t *testing.T) {
	api := newPostgresApi(t, "itcolumnarparams")
	rec := recordInserts(t, api.RDB.Database)
	rng := rand.New(rand.NewSource(1))
	var batch *columnarBatch
	for batch == nil || len(batch.measurements) == 0 || len(batch.locations) == 0 || len(batch.alerts) == 0 {
		batch = newColumnarBatch(rng, time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC))
	}
	rows, anchors := batch.rows(t)
	ctx := core.WithTenant(context.Background(), "acme")
	require.NoError(t, api.PersistInTx(ctx, func(tx *gorm.DB) error {
		if err := api.CreateEventRows(ctx, tx, rows); err != nil {
			return err
		}
		return api.CreateEventAnchors(ctx, tx, anchors)
	}))
	// location_events is the widest event-store table: twelve columns, the tenant among them.
	const most = 12
	for _, table := range columnarTables {
		vars := rec.vars[table]
		require.NotEmptyf(t, vars, "precondition: the batch wrote %s", table)
		for _, n := range vars {
			require.LessOrEqualf(t, n, most, "an INSERT into %s bound %d parameters; a column-array insert binds "+
				"one per column, at most %d, whatever the batch size", table, n, most)
		}
	}
}

// A classifier above the bigint range is refused on Postgres, as the VALUES write refused
// it, and nothing of its batch is stored — rather than wrapped into a negative number.
func TestColumnarWriteRefusesAClassifierAboveTheBigintRange(t *testing.T) {
	api := newPostgresApi(t, "itcolumnarclassifier")
	db := api.RDB.Database
	at := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	big := uint(math.MaxInt64) + 5
	build := func() *EventRows {
		ev := Event{EventId: []byte("classified"), DeviceToken: "d", EventType: esmodel.Measurement,
			OccurredTime: at, ProcessedTime: at}
		parents, ms, err := BuildMeasurementRows([]*MeasurementEventCreateRequest{
			{Event: ev, EntryOccurredTime: at, Name: "m", Value: ptr(1.0), Classifier: &big}})
		require.NoError(t, err)
		return &EventRows{Parents: parents, Measurements: ms}
	}
	ctx := core.WithTenant(context.Background(), "via-values")
	oldErr := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return valuesWrite(ctx, tx, build(), nil) })
	require.Error(t, oldErr, "precondition: the VALUES write refuses the classifier")
	ctx = core.WithTenant(context.Background(), "via-grouped")
	newErr := api.PersistInTx(ctx, func(tx *gorm.DB) error { return api.CreateEventRows(ctx, tx, build()) })
	require.ErrorIs(t, newErr, rdb.ErrColumnValue)
	for _, table := range columnarTables {
		require.Empty(t, storedFor(t, db, table, "via-grouped"), "a refused batch stored rows in %s", table)
	}
}
