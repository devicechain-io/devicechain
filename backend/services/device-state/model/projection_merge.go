// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
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
//     the race. An existing row is saved.
//  3. The batch's readings, then its positions, are written as one upsert each.
//
// Locks are taken in (tenant, table, key) order in every batch, so two batch writers cannot
// deadlock on each other in steps 1 and 3. Three shapes can still meet in a deadlock, and
// PostgreSQL then aborts one side: a row created by another writer between steps 1 and 2 is
// locked out of order; the inactivity sweep updates many rows in scan order; and during a
// rolling upgrade, an older per-event writer locks one device's readings in payload order.
// A batch that loses is a *BatchRefusal and its tenant's updates are merged again one at a
// time, which is correct and costs transactions; a sweep that loses is recorded as a failed
// pass and runs again at the next.
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
			if err := tdb.Save(row).Error; err != nil {
				return err
			}
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
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "device_token"}},
		DoNothing: true,
	}).Create(row)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}
