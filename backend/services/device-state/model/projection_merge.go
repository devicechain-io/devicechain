// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The latest-value and last-known-position projections, and the batch merge.
//
// 🔴 THE PROJECTION MUST NOT GO BACKWARDS. The resolved-events stream redelivers (an unacked
// message comes back) and does not guarantee order across the parallel projection writers,
// so "last write wins" would let a redelivered old reading overwrite a newer one, or an old
// fix teleport a device back to where it used to be — silently, and indistinguishably from
// the device actually having reported it. The guard is therefore on the reading's OCCURRED
// time, not on arrival: a stored value is replaced only by one STRICTLY newer, compared at
// stored precision (storedTime), which makes redelivery of the current value a no-op, and
// leaves the first-stored of two readings with equal times in place.
//
// Both are written as ONE conditional upsert per transaction —
// INSERT … ON CONFLICT (key) DO UPDATE … WHERE stored.occurred_time < excluded.occurred_time
// — rather than a locked read and a write per key. A conditional upsert is atomic per row, so
// there is no read-then-write window for a concurrent writer to land in and nothing to lock
// in advance; and the same statement serves one event (MergeLatestMeasurements /
// MergeLatestLocations) and a whole batch (MergeProjectionBatch), so both paths apply one rule.
// The device rows a batch has locked and folded are written back the same way, as one
// INSERT … ON CONFLICT DO UPDATE per tenant (writeFoldedStates).
//
// Soft delete: the upsert would update a soft-deleted row and leave it deleted. Nothing
// soft-deletes these rows (the tenant purge hard-deletes with raw SQL), so that state is not
// reachable today.

// upsertChunk is the most rows one upsert statement carries. 500 rows of the widest row
// (latest_locations, twelve columns) is 6000 parameters, far below either database's limit.
const upsertChunk = 500

// latestMeasurementGuard applies the DO UPDATE only when the incoming reading is STRICTLY
// newer than the stored one. The unqualified table name refers to the insert's target on
// both databases, even though the insert names it with the area's schema prefix.
var latestMeasurementGuard = clause.Where{Exprs: []clause.Expression{
	clause.Expr{SQL: "latest_measurements.occurred_time < excluded.occurred_time"},
}}

// latestLocationGuard is latestMeasurementGuard for positions.
var latestLocationGuard = clause.Where{Exprs: []clause.Expression{
	clause.Expr{SQL: "latest_locations.occurred_time < excluded.occurred_time"},
}}

// deviceStateKey is device_states' conflict key: the columns both of the batch's device-row
// statements arbitrate on (createFolded's first-sight insert, writeFoldedStates' write-back),
// and therefore the columns the write-back never rewrites. One definition for every use.
var deviceStateKey = []string{"tenant_id", "device_token"}

// deviceStateConflict is deviceStateKey as an ON CONFLICT target.
func deviceStateConflict() []clause.Column {
	cols := make([]clause.Column, len(deviceStateKey))
	for i, k := range deviceStateKey {
		cols[i] = clause.Column{Name: k}
	}
	return cols
}

// foldedStateColumns is every column of device_states the batch writes back over a row it has
// locked and folded: all of them but the row's identity — its primary key, created_at, and
// deviceStateKey. It is derived from the model and not listed, because it must stay the set of
// columns tx.Save writes on the per-event path (MergeDeviceState, which updates every field): a
// column applyEvent learns to set is then written by both paths, with no second list to
// remember. A column gorm would not insert (not Creatable) is left out too, because its
// excluded.<col> would be the column's default rather than the folded value.
//
// The NAMES match Save's; the values can differ in one way, which writeFoldedStates refuses
// rather than writes: see zeroUnderDefault.
func foldedStateColumns(db *gorm.DB) ([]string, error) {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&DeviceState{}); err != nil {
		return nil, err
	}
	key := map[string]bool{}
	for _, k := range deviceStateKey {
		key[k] = true
	}
	cols := make([]string, 0, len(stmt.Schema.DBNames))
	for _, name := range stmt.Schema.DBNames {
		f := stmt.Schema.FieldsByDBName[name]
		if f == nil || f.PrimaryKey || f.AutoCreateTime > 0 || !f.Updatable || !f.Creatable || key[name] {
			continue
		}
		cols = append(cols, name)
	}
	return cols, nil
}

// zeroUnderDefault names the first written column in which a row holds its type's zero while
// the model declares a different default (a `default:` tag). A gorm Create replaces such a zero
// with the default, where Save wrote the zero — so writing that row back would persist a value
// the fold never produced. No column can do that today: PresenceSource's default is INFERRED and
// every writer sets it, and SessionId's default is its zero. A column that later could is
// refused loudly here instead of written wrong in batches only.
func zeroUnderDefault(db *gorm.DB, cols []string, rows []DeviceState) error {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&DeviceState{}); err != nil {
		return err
	}
	ctx := db.Statement.Context
	for _, name := range cols {
		f := stmt.Schema.FieldsByDBName[name]
		if f == nil || f.DefaultValueInterface == nil {
			continue
		}
		zero := reflect.Zero(f.FieldType).Interface()
		if reflect.DeepEqual(f.DefaultValueInterface, zero) {
			continue
		}
		for i := range rows {
			if _, isZero := f.ValueOf(ctx, reflect.ValueOf(&rows[i]).Elem()); isZero {
				return fmt.Errorf("device %s: column %s holds its zero value, which a batch write-back "+
					"would replace with the declared default %v", rows[i].DeviceToken, name, f.DefaultValueInterface)
			}
		}
	}
	return nil
}

// writeFoldedStates writes back every existing row the batch has locked and folded for one
// tenant, as one multi-row INSERT … ON CONFLICT (tenant_id, device_token) DO UPDATE per
// rdb.RowsPerInsert rows (rdb.CreateChunked): one statement for any batch the processor forms,
// where a Save per device was one each. Every row in rows was read FOR UPDATE by this
// transaction, so for every one the conflict arm is taken — it is an UPDATE of a row the
// transaction already holds, the statement Save would have sent, and it takes no lock that was
// not already held. The explicit id in VALUES is therefore never inserted.
//
// It is a gorm Create, not raw SQL, so the erasure fence and the tenant stamp run on it as they
// ran on Save (raw SQL runs on gorm's Raw processor, where neither is registered). UpdatedAt is
// set here because a Create stamps it only when it is zero, and a row read back never is; Save
// stamps it on every write, and the GraphQL updatedAt shows it. (gorm's
// gorm:update_track_time setting is no substitute: it is cleared after the first statement, so
// under a chunked write only the first chunk would be stamped.) Moving from an update to a
// create changes nothing else a callback sees: the fence is still read at the transaction's
// first write for the tenant (now this statement, or the first createFolded), and DeviceState
// is exempt from the audit journal on both paths.
//
// 🔴 Not OnConflict{UpdateAll: true}: gorm binds its updated_at assignment after
// rdb.RowsPerInsert has counted, and core/rdb refuses that combination (ErrRowWidthUnknown).
func writeFoldedStates(tdb *gorm.DB, rows []DeviceState) error {
	if len(rows) == 0 {
		return nil
	}
	cols, err := foldedStateColumns(tdb)
	if err != nil {
		return err
	}
	if err := zeroUnderDefault(tdb, cols, rows); err != nil {
		return err
	}
	now := tdb.NowFunc()
	for i := range rows {
		rows[i].UpdatedAt = now
	}
	return rdb.CreateChunked(foldedStatesUpsert(tdb, cols), &rows).Error
}

// foldedStatesUpsert is tdb with writeFoldedStates' conflict clause: on the device's key,
// overwrite cols from the incoming row.
func foldedStatesUpsert(tdb *gorm.DB, cols []string) *gorm.DB {
	return tdb.Clauses(clause.OnConflict{
		Columns:   deviceStateConflict(),
		DoUpdates: clause.AssignmentColumns(cols),
	})
}

// coalesceMeasurements reduces one device's readings, in arrival order, to one row per name:
// the reading with the latest time at stored precision, and among readings with equal times
// the one that ARRIVED FIRST. That is exactly what applying them one at a time under
// latestMeasurementGuard leaves behind, and it is required rather than an optimisation:
// PostgreSQL refuses an INSERT … ON CONFLICT DO UPDATE that touches one row twice. Rows are
// returned sorted by name, so every writer takes the rows' locks in the same order. TenantId
// is left empty: the tenant-scope callback stamps it from the statement's context, and
// refuses a row naming any other tenant.
func coalesceMeasurements(deviceToken string, inputs []LatestMeasurementInput) []LatestMeasurement {
	byName := make(map[string]int, len(inputs))
	rows := make([]LatestMeasurement, 0, len(inputs))
	for _, in := range inputs {
		at := storedTime(in.OccurredTime)
		if i, ok := byName[in.Name]; ok {
			if at.After(rows[i].OccurredTime) {
				rows[i].Value, rows[i].Classifier, rows[i].Unit, rows[i].DataType = in.Value, in.Classifier, in.Unit, in.DataType
				rows[i].OccurredTime = at
			}
			continue
		}
		byName[in.Name] = len(rows)
		rows = append(rows, LatestMeasurement{
			DeviceToken:  deviceToken,
			Name:         in.Name,
			Value:        in.Value,
			Classifier:   in.Classifier,
			Unit:         in.Unit,
			DataType:     in.DataType,
			OccurredTime: at,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

// coalesceLocations is coalesceMeasurements for fixes: one row per device, the latest fix,
// the first-arriving among equals. nil when there are none. Every field of the kept fix is
// kept together — a fix is one atomic observation, so carrying one fix's speed beside
// another's position would synthesize a reading no device ever reported.
func coalesceLocations(deviceToken string, inputs []LatestLocationInput) *LatestLocation {
	var row *LatestLocation
	for _, in := range inputs {
		at := storedTime(in.OccurredTime)
		if row != nil && !at.After(row.OccurredTime) {
			continue
		}
		row = &LatestLocation{
			DeviceToken:  deviceToken,
			Latitude:     in.Latitude,
			Longitude:    in.Longitude,
			Elevation:    in.Elevation,
			Accuracy:     in.Accuracy,
			Speed:        in.Speed,
			Heading:      in.Heading,
			OccurredTime: at,
		}
	}
	return row
}

// upsertLatestMeasurements writes rows — one tenant's, sorted by device token then name,
// each (device, name) at most once — under latestMeasurementGuard.
func upsertLatestMeasurements(tx *gorm.DB, rows []LatestMeasurement) error {
	if len(rows) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "device_token"}, {Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "classifier", "unit", "data_type", "occurred_time", "updated_at"}),
		Where:     latestMeasurementGuard,
	}).CreateInBatches(&rows, upsertChunk).Error
}

// upsertLatestLocations writes rows — one tenant's, sorted by device token, each device at
// most once — under latestLocationGuard.
func upsertLatestLocations(tx *gorm.DB, rows []LatestLocation) error {
	if len(rows) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "device_token"}},
		DoUpdates: clause.AssignmentColumns([]string{"latitude", "longitude", "elevation", "accuracy", "speed",
			"heading", "occurred_time", "updated_at"}),
		Where: latestLocationGuard,
	}).CreateInBatches(&rows, upsertChunk).Error
}

// MergeLatestMeasurements advances the current value of each named measurement for a device
// from one resolved measurement event: the per-event path, in a transaction of its own (see
// the file comment for the rule).
func (api *Api) MergeLatestMeasurements(ctx context.Context, deviceToken string, inputs []LatestMeasurementInput) error {
	rows := coalesceMeasurements(deviceToken, inputs)
	if len(rows) == 0 {
		return nil
	}
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		return upsertLatestMeasurements(tx, rows)
	})
}

// MergeLatestLocations advances a device's last-known position from the fixes carried by one
// resolved location event: one row per (tenant, device), so every fix in the event contends
// for the same row and the newest one wins. The per-event path, in a transaction of its own.
func (api *Api) MergeLatestLocations(ctx context.Context, deviceToken string, inputs []LatestLocationInput) error {
	row := coalesceLocations(deviceToken, inputs)
	if row == nil {
		return nil
	}
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		return upsertLatestLocations(tx, []LatestLocation{*row})
	})
}

// ProjectionUpdate is everything one resolved event contributes to the projection.
type ProjectionUpdate struct {
	Tenant       string
	DeviceToken  string
	OccurredAt   time.Time
	Presence     *PresenceTransition // nil for a plain data event
	Identity     DeviceIdentity
	Measurements []LatestMeasurementInput // measurement events only
	Locations    []LatestLocationInput    // location events only
}

// BatchRefusal is MergeProjectionBatch's error when a statement written for one tenant
// failed. The transaction is rolled back, and Tenant is the smallest unit the failure can be
// pinned on, because every statement in the batch is one tenant's.
type BatchRefusal struct {
	Tenant string
	Err    error
}

func (e *BatchRefusal) Error() string { return fmt.Sprintf("tenant %s: %v", e.Tenant, e.Err) }
func (e *BatchRefusal) Unwrap() error { return e.Err }

// MergeProjectionBatch merges updates in ONE transaction, each device's in arrival order:
// what it leaves is what merging them one at a time — MergeDeviceState, then
// MergeLatestMeasurements or MergeLatestLocations, per update — would leave, and either all
// of it commits or none does.
//
// ctx must carry NO tenant. Each tenant's statements bind core.WithTenant(ctx, tenant), so a
// statement that forgot to would fail closed with core.ErrNoTenant rather than write under a
// batch-mate's tenant. A statement that fails is returned as a *BatchRefusal naming its
// tenant; any other error (BEGIN, COMMIT, a lost connection) blames nothing.
//
// Per tenant, in sorted order, and per device within it, in sorted order:
//
//  1. One statement locks the existing row of every device in the batch (SELECT … FOR
//     UPDATE, in token order).
//  2. Each device's updates are folded into its row in arrival order: applyEvent for a row
//     that exists, newDeviceState for the first update of one that does not. A new row is
//     inserted with ON CONFLICT DO NOTHING; when another writer created it after step 1 read,
//     the insert does nothing, the committed row is locked and read, and every update is
//     folded into it instead — what merging them one at a time would have done after losing
//     the race. An existing row is kept for step 3.
//  3. The batch's existing device rows are written back in one statement (writeFoldedStates),
//     then its readings, then its positions, as one upsert each. The write-back takes no lock
//     step 1 or 2 did not already hold.
//
// Locks are taken in (tenant, table, key) order in every batch, so two batch writers cannot
// deadlock on each other in steps 1 and 3. Three shapes can still meet in a deadlock, and
// PostgreSQL then aborts one side: a row created by another writer between steps 1 and 2 is
// locked out of order; the inactivity sweep updates many rows in scan order; and during a
// rolling upgrade, an older per-event writer locks one device's readings in payload order.
// A batch that loses is a *BatchRefusal and its tenant's updates are merged again one at a
// time, which costs transactions. That one-event write is a transaction too and can lose a
// deadlock in its turn: the processor retries it in place a few times, and an event that still
// loses is left unacknowledged for the broker to redeliver, not applied. A sweep that loses is
// recorded as a failed pass and runs again at the next.
func (api *Api) MergeProjectionBatch(ctx context.Context, updates []ProjectionUpdate) error {
	byTenant := map[string]map[string][]int{}
	for i, u := range updates {
		devices := byTenant[u.Tenant]
		if devices == nil {
			devices = map[string][]int{}
			byTenant[u.Tenant] = devices
		}
		devices[u.DeviceToken] = append(devices[u.DeviceToken], i)
	}
	tenants := make([]string, 0, len(byTenant))
	for t := range byTenant {
		tenants = append(tenants, t)
	}
	sort.Strings(tenants)

	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		for _, t := range tenants {
			tdb := tx.WithContext(core.WithTenant(ctx, t))
			if err := mergeTenantBatch(tdb, byTenant[t], updates); err != nil {
				return &BatchRefusal{Tenant: t, Err: err}
			}
		}
		return nil
	})
}

// mergeTenantBatch is MergeProjectionBatch for one tenant, on a handle bound to it. devices
// maps each device token to the indexes of its updates, in arrival order.
func mergeTenantBatch(tdb *gorm.DB, devices map[string][]int, updates []ProjectionUpdate) error {
	tokens := make([]string, 0, len(devices))
	for tok := range devices {
		tokens = append(tokens, tok)
	}
	sort.Strings(tokens)

	var existing []DeviceState
	if err := tdb.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("device_token IN ?", tokens).Order("device_token").Find(&existing).Error; err != nil {
		return err
	}
	rows := make(map[string]*DeviceState, len(existing))
	for i := range existing {
		rows[existing[i].DeviceToken] = &existing[i]
	}

	var folded []DeviceState
	var measurements []LatestMeasurement
	var locations []LatestLocation
	for _, tok := range tokens {
		idx := devices[tok]
		row, found := rows[tok]
		if !found {
			created, err := createFolded(tdb, tok, idx, updates)
			if err != nil {
				return err
			}
			if !created {
				// Another writer created the row after the lock read: fold every update into
				// the committed row, as a redelivered first event would have been.
				row = &DeviceState{}
				if err := tdb.Clauses(clause.Locking{Strength: "UPDATE"}).
					Where("device_token = ?", tok).First(row).Error; err != nil {
					return err
				}
				found = true
			}
		}
		if found {
			for _, i := range idx {
				u := updates[i]
				applyEvent(row, storedTime(u.OccurredAt), storedTransition(u.Presence), u.Identity)
			}
			folded = append(folded, *row)
		}

		var readings []LatestMeasurementInput
		var fixes []LatestLocationInput
		for _, i := range idx {
			readings = append(readings, updates[i].Measurements...)
			fixes = append(fixes, updates[i].Locations...)
		}
		measurements = append(measurements, coalesceMeasurements(tok, readings)...)
		if fix := coalesceLocations(tok, fixes); fix != nil {
			locations = append(locations, *fix)
		}
	}
	if err := writeFoldedStates(tdb, folded); err != nil {
		return err
	}
	if err := upsertLatestMeasurements(tdb, measurements); err != nil {
		return err
	}
	return upsertLatestLocations(tdb, locations)
}

// createFolded inserts the row a device with no row ends up with — its first update creates
// it (newDeviceState), and the rest fold into it (applyEvent) — unless another writer has
// created one since the lock read, in which case it inserts nothing and reports false.
func createFolded(tdb *gorm.DB, tok string, idx []int, updates []ProjectionUpdate) (bool, error) {
	first := updates[idx[0]]
	row := newDeviceState(tok, storedTime(first.OccurredAt), storedTransition(first.Presence), first.Identity)
	for _, i := range idx[1:] {
		u := updates[i]
		applyEvent(row, storedTime(u.OccurredAt), storedTransition(u.Presence), u.Identity)
	}
	res := tdb.Clauses(clause.OnConflict{
		Columns:   deviceStateConflict(),
		DoNothing: true,
	}).Create(row)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}
