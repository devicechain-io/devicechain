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
// INSERT … ON CONFLICT DO UPDATE per tenant (writeFoldedStates). A batch with no presence claim
// goes further and skips the lock read altogether: its device rows are ONE such upsert whose
// update arm is the data-event rule in SQL (mergeHeartbeats).
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

// deviceStateKey is device_states' conflict key: the columns every one of the batch's
// device-row statements arbitrates on (the heartbeat upsert, lockOrCreate, writeFoldedStates'
// write-back), and therefore the columns the write-back never rewrites. One definition for every use.
var deviceStateKey = []string{"tenant_id", "device_token"}

// deviceStateConflict is deviceStateKey as an ON CONFLICT target.
func deviceStateConflict() []clause.Column {
	cols := make([]clause.Column, len(deviceStateKey))
	for i, k := range deviceStateKey {
		cols[i] = clause.Column{Name: k}
	}
	return cols
}

// notFolded is what foldedStateColumns has and the fold never reads or writes: the row's
// soft-delete marker and its per-device inactivity override (neither is touched by
// applyEvent or newDeviceState), and updated_at, which a write stamps rather than folds.
var notFolded = map[string]bool{"deleted_at": true, "inactivity_timeout": true, "updated_at": true}

// foldedStateColumns is every column of device_states a batch may write over a row it has
// folded: all of them but the row's identity — its primary key, created_at, and
// deviceStateKey. It is derived from the model and not listed, so that a column added to
// DeviceState is in it without anyone remembering to say so. A column gorm would not insert
// (not Creatable) is left out, because its excluded.<col> would be the column's default rather
// than the folded value.
//
// It is the set tx.Save writes on the per-event path (MergeDeviceState, which updates every
// field), and a SUPERSET of foldColumns — see there for what a batch actually reads and writes.
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

// foldColumns is the columns applyEvent and newDeviceState read or write: foldedStateColumns
// less notFolded. It is the ONE list a batch uses for both halves of a locked merge — the
// lock read selects exactly these (plus the row's id and token), and the write-back overwrites
// exactly these (plus updated_at) — so a column applyEvent learns to touch is read AND written
// or neither, and a column the fold does not touch (inactivity_timeout, which an operator may
// set between the read and the write) is never overwritten with a value the read did not carry.
// notFolded is an opt-out list: a new column is folded until someone says otherwise.
func foldColumns(db *gorm.DB) ([]string, error) {
	all, err := foldedStateColumns(db)
	if err != nil {
		return nil, err
	}
	cols := make([]string, 0, len(all))
	for _, c := range all {
		if !notFolded[c] {
			cols = append(cols, c)
		}
	}
	return cols, nil
}

// foldSelect is the lock read's column list: foldColumns, the key the rows are matched on, and
// the primary key so the write-back names the row it holds (an absent id would burn a fresh
// sequence value per row written, and a row that conflicts is never inserted anyway).
func foldSelect(db *gorm.DB) ([]string, error) {
	cols, err := foldColumns(db)
	if err != nil {
		return nil, err
	}
	return append([]string{"id", "device_token"}, cols...), nil
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
// first write for the tenant (now the batch's first device-row statement), and DeviceState
// is exempt from the audit journal on both paths.
//
// 🔴 Not OnConflict{UpdateAll: true}: gorm binds its updated_at assignment after
// rdb.RowsPerInsert has counted, and core/rdb refuses that combination (ErrRowWidthUnknown).
func writeFoldedStates(tdb *gorm.DB, rows []DeviceState) error {
	if len(rows) == 0 {
		return nil
	}
	cols, err := foldColumns(tdb)
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
	return rdb.CreateChunked(foldedStatesUpsert(tdb, append(cols, "updated_at")), &rows).Error
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
// Per tenant, in sorted order, and per device within it, in sorted order, one of two paths.
//
// A tenant batch that carries NO presence claim (the common case: plain data events) takes
// the HEARTBEAT path, mergeHeartbeats — one INSERT … ON CONFLICT DO UPDATE writes every device
// row, with no lock read and no first-sight insert; see there. Only a device an inferred-dead
// row is waiting to resurrect needs more (a lock read and a write-back, in that function).
//
// A tenant batch that carries any claim takes the LOCKED path, mergeWithLocks:
//
//  1. ONE statement takes every device row of the batch, in token order: an INSERT … ON
//     CONFLICT DO UPDATE … WHERE 1 = 0 (lockOrCreate) that inserts the blank row
//     (blankDeviceState) for a device with none and, for a device with one, takes the conflict
//     arm, which LOCKS the row and changes nothing.
//  2. One statement reads those rows back (SELECT … FOR UPDATE, in token order), reading only
//     the columns the fold touches (foldSelect). Every row is already held, so it takes no lock.
//  3. Each device's updates are folded into its row in arrival order with applyEvent — a blank
//     row included, since applyEvent over it leaves what newDeviceState would.
//  4. The rows are written back in one statement (writeFoldedStates), then the readings, then
//     the positions, as one upsert each. The write-back takes no lock step 1 did not take.
//
// The two shapes are never mixed inside one tenant batch.
//
// 🔴 THE LOCK ORDER. A transaction that waits for a lock while holding another can deadlock
// only with one that acquires the same two in the opposite order. Every batch therefore takes
// device_states row locks in ONE statement and in ascending (tenant, token) order, whether
// the row exists or not: the heartbeat upsert and lockOrCreate both walk VALUES in token
// order, and an INSERT … ON CONFLICT takes a new row (its own insert, which a second
// inserter waits on until commit) and an existing one (the conflict arm's row lock) at the
// SAME point of that walk. Everything after it touches only rows already held, then
// latest_measurements in (token, name) order, then latest_locations in token order, each one
// statement (or ordered chunks of one) — so in (tenant, table, key) order throughout.
//
// The locked path used to lock the rows that EXISTED first, in one read, and insert the new
// ones after it. That order is token order only among existing rows: a new device whose token
// sorts below an existing one was locked after it, which the heartbeat upsert's single walk does
// the other way round — a claim batch held B and waited to insert A while a heartbeat batch had
// inserted A and waited for B, and PostgreSQL aborted one (40P01). Two such locked batches could
// meet the same way when a third writer created a row between one's read and its inserts.
// lockOrCreate is the cure for both: there is no "existing first" any more.
//
// Two shapes outside the batch can still meet a batch in a deadlock, and PostgreSQL then
// aborts one side: the inactivity sweep updates many rows in scan order, and during a rolling
// upgrade an older writer locks in its own order. A batch that loses is a *BatchRefusal and
// its tenant's updates are merged again one at a time, which costs transactions. That
// one-event write is a transaction too and can lose a deadlock in its turn: the processor
// retries it in place a few times, and an event that still loses is left unacknowledged for
// the broker to redeliver, not applied. A sweep that loses is recorded as a failed pass and
// runs again at the next.
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
	claims := false
	for tok, idx := range devices {
		tokens = append(tokens, tok)
		for _, i := range idx {
			if updates[i].Presence != nil {
				claims = true
			}
		}
	}
	sort.Strings(tokens)
	if claims {
		return mergeWithLocks(tdb, tokens, devices, updates)
	}
	return mergeHeartbeats(tdb, tokens, devices, updates)
}

// writeLatest writes one tenant's latest-value rows after its device rows (table order).
func writeLatest(tdb *gorm.DB, tokens []string, devices map[string][]int, updates []ProjectionUpdate) error {
	var measurements []LatestMeasurement
	var locations []LatestLocation
	for _, tok := range tokens {
		var readings []LatestMeasurementInput
		var fixes []LatestLocationInput
		for _, i := range devices[tok] {
			readings = append(readings, updates[i].Measurements...)
			fixes = append(fixes, updates[i].Locations...)
		}
		measurements = append(measurements, coalesceMeasurements(tok, readings)...)
		if fix := coalesceLocations(tok, fixes); fix != nil {
			locations = append(locations, *fix)
		}
	}
	if err := upsertLatestMeasurements(tdb, measurements); err != nil {
		return err
	}
	return upsertLatestLocations(tdb, locations)
}

// mergeWithLocks is mergeTenantBatch's locked path (steps 1-4 above), for a tenant batch that
// carries a presence claim. tokens is sorted.
func mergeWithLocks(tdb *gorm.DB, tokens []string, devices map[string][]int, updates []ProjectionUpdate) error {
	if err := lockOrCreate(tdb, tokens); err != nil {
		return err
	}
	sel, err := foldSelect(tdb)
	if err != nil {
		return err
	}
	var rows []DeviceState
	if err := tdb.Clauses(clause.Locking{Strength: "UPDATE"}).Select(sel).
		Where("device_token IN ?", tokens).Order("device_token").Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != len(tokens) {
		// lockOrCreate left a row for every token, so a short read is a row the read cannot
		// see (a soft-deleted one). Refuse it rather than drop those devices' events.
		return fmt.Errorf("the batch's lock read found %d device rows of the %d it had just locked or created",
			len(rows), len(tokens))
	}
	for i := range rows {
		foldInto(&rows[i], devices[rows[i].DeviceToken], updates)
	}
	if err := writeFoldedStates(tdb, rows); err != nil {
		return err
	}
	return writeLatest(tdb, tokens, devices, updates)
}

// lockOrCreateNever is lockOrCreate's DO UPDATE condition: never true, so the conflict arm
// writes nothing — but PostgreSQL locks a conflicting row whether or not the condition holds,
// which is the whole point.
var lockOrCreateNever = clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "1 = 0"}}}

// lockOrCreate takes every device row of a locked-path tenant batch in ONE statement, walking
// tokens (sorted) in order: a device with no row gets the blank row (blankDeviceState), and a
// device with one takes the conflict arm, which locks the row and, by lockOrCreateNever,
// changes nothing. It is the heartbeat upsert's walk with a different row and update arm, so
// the two paths take the same locks in the same order — see "THE LOCK ORDER" on
// MergeProjectionBatch. A row another writer is inserting is waited for and then locked, so
// first sight has no lost race to recover from.
//
// The blank row is never committed as it is: the caller folds the device's events into it and
// writes it back in the same transaction, and no other transaction can see it before then.
func lockOrCreate(tdb *gorm.DB, tokens []string) error {
	rows := make([]DeviceState, len(tokens))
	for i, tok := range tokens {
		rows[i] = *blankDeviceState(tok)
	}
	return rdb.CreateChunked(tdb.Clauses(clause.OnConflict{
		Columns: deviceStateConflict(),
		DoUpdates: clause.Set{{Column: clause.Column{Name: "updated_at"},
			Value: clause.Column{Table: "device_states", Name: "updated_at"}}},
		Where: lockOrCreateNever,
	}), &rows).Error
}

// foldInto folds a device's updates into row, in arrival order.
func foldInto(row *DeviceState, idx []int, updates []ProjectionUpdate) {
	for _, i := range idx {
		u := updates[i]
		applyEvent(row, storedTime(u.OccurredAt), storedTransition(u.Presence), u.Identity)
	}
}

// foldNew is the row a device with no row ends up with: its first update creates it
// (newDeviceState) and the rest fold into it (applyEvent).
func foldNew(tok string, idx []int, updates []ProjectionUpdate) *DeviceState {
	first := updates[idx[0]]
	row := newDeviceState(tok, storedTime(first.OccurredAt), storedTransition(first.Presence), first.Identity)
	foldInto(row, idx[1:], updates)
	return row
}

// heartbeatGuard is the DO UPDATE's WHERE: it EXCLUDES the one row a plain data event cannot
// be folded into in SQL — an INFERRED, inactive device the event is fresh for, which the
// event resurrects (Active, LastConnectTime and InactivityAlarmTime change, and which event
// supplies LastConnectTime depends on arrival order). The condition is applyEvent's test, in
// SQL: `fresh` is "no activity recorded, or the event is NOT OLDER" (>=, the frozen-clock
// rule), and the flip needs presence_source <> ASSERTED and not active. excluded holds the
// batch's folded row, whose last_activity_time is the latest time in the batch; a batch flips
// the row exactly when its latest event is fresh, because activity only ever rises through
// fresh events.
//
// A row this excludes is still LOCKED by the conflict arm, and is not counted in RowsAffected:
// that shortfall is how mergeHeartbeats finds the rows it must fold in Go.
var heartbeatGuard = clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "NOT (device_states.presence_source <> '" +
	PresenceSourceAsserted + "' AND NOT device_states.active AND (device_states.last_activity_time IS NULL " +
	"OR excluded.last_activity_time >= device_states.last_activity_time))"}}}

// heartbeatSet is the DO UPDATE's SET: applyEvent's data-event rule for a row heartbeatGuard
// let through, which cannot flip. There each event's only effects are the identity overwrite
// (the last non-empty value) and the monotone advance of last_activity_time when the event is
// not older — effects that commute, so one SET over the batch's folded row (excluded) equals
// folding its events in any order. updated_at is stamped as Save stamped it.
//
// applyEvent's data branch is the Go twin of this; model/projection_equivalence_test.go pins
// the two together.
var heartbeatSet = clause.Set{
	{Column: clause.Column{Name: "external_id"}, Value: clause.Expr{SQL: "CASE WHEN excluded.external_id <> '' " +
		"THEN excluded.external_id ELSE device_states.external_id END"}},
	{Column: clause.Column{Name: "source"}, Value: clause.Expr{SQL: "CASE WHEN excluded.source <> '' " +
		"THEN excluded.source ELSE device_states.source END"}},
	{Column: clause.Column{Name: "last_activity_time"}, Value: clause.Expr{SQL: "CASE WHEN " +
		"device_states.last_activity_time IS NULL OR excluded.last_activity_time >= device_states.last_activity_time " +
		"THEN excluded.last_activity_time ELSE device_states.last_activity_time END"}},
	{Column: clause.Column{Name: "updated_at"}, Value: clause.Column{Table: "excluded", Name: "updated_at"}},
}

// heartbeatUpsert is tdb with mergeHeartbeats' conflict clause.
func heartbeatUpsert(tdb *gorm.DB) *gorm.DB {
	return tdb.Clauses(clause.OnConflict{
		Columns:   deviceStateConflict(),
		DoUpdates: heartbeatSet,
		Where:     heartbeatGuard,
	})
}

// mergeHeartbeats is mergeTenantBatch's path for a tenant batch with NO presence claim: ONE
// INSERT … ON CONFLICT DO UPDATE writes every device's row.
//
//   - A device with no row is inserted as foldNew builds it, in Go: the insert arm IS
//     newDeviceState and applyEvent, so first sight needs no lock read and no per-device insert,
//     and two writers racing to create a row fold into each other instead of one losing.
//   - A device with a row takes heartbeatSet — unless heartbeatGuard excludes it, in which
//     case the statement locks the row and leaves it alone.
//
// The statement takes each row in token order (the VALUES order) — a new row by inserting it,
// an existing one by its conflict arm's lock, at the same point of the walk — exactly as the
// locked path's lockOrCreate does, so the two paths take device_states in one order (see "THE
// LOCK ORDER" on MergeProjectionBatch). A redelivered event changes nothing (the activity
// advance is monotone and the identity already equal).
//
// When the statement wrote fewer rows than it was given, some device was excluded: it is an
// inferred-dead device a data event brings back. Those rows are already locked by this
// transaction, so reading them takes no new lock; the read takes every inactive inferred row
// of the batch — a superset of the excluded ones, harmlessly, because folding a row the SQL
// already handled changes nothing (nothing in it is fresh) — and folds their events with
// applyEvent and writes them back. That costs two statements, once per device that was swept.
//
// A soft-deleted row would be updated and left deleted, as the latest-value upserts would;
// nothing soft-deletes these rows.
//
// Every VALUES row evaluates the id default, so a row that takes the conflict arm still consumes
// a value of device_states' own sequence (one per device per batch; the latest-value upserts
// already do the same). Nothing reads the ids as dense — they are row identities and a keyset
// cursor, both gap-tolerant — and a bigint sequence outlasts any event rate. Naming the id in
// VALUES would avoid it only for rows already known, which is the read this path exists to skip.
func mergeHeartbeats(tdb *gorm.DB, tokens []string, devices map[string][]int, updates []ProjectionUpdate) error {
	rows := make([]DeviceState, len(tokens))
	for i, tok := range tokens {
		rows[i] = *foldNew(tok, devices[tok], updates)
	}
	now := tdb.NowFunc()
	for i := range rows {
		rows[i].UpdatedAt = now
	}
	res := rdb.CreateChunked(heartbeatUpsert(tdb), &rows)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected < int64(len(rows)) {
		if err := foldSkipped(tdb, tokens, devices, updates); err != nil {
			return err
		}
	}
	return writeLatest(tdb, tokens, devices, updates)
}

// foldSkipped is mergeHeartbeats' fallback: lock-read the batch's inactive INFERRED rows (the
// only ones heartbeatGuard excludes), fold their events, and write them back.
func foldSkipped(tdb *gorm.DB, tokens []string, devices map[string][]int, updates []ProjectionUpdate) error {
	sel, err := foldSelect(tdb)
	if err != nil {
		return err
	}
	var skipped []DeviceState
	if err := tdb.Clauses(clause.Locking{Strength: "UPDATE"}).Select(sel).
		Where("device_token IN ? AND active = ? AND presence_source <> ?", tokens, false, PresenceSourceAsserted).
		Order("device_token").Find(&skipped).Error; err != nil {
		return err
	}
	for i := range skipped {
		foldInto(&skipped[i], devices[skipped[i].DeviceToken], updates)
	}
	return writeFoldedStates(tdb, skipped)
}
