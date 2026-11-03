// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/presence"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The column-array writes against the gorm writes they replaced.
//
// The legacy* functions below are the statements as they were before the projection moved to
// rdb.NewColumnUpsert, copied verbatim and kept ONLY here: a gorm Create with the same conflict
// target, update arm and guard. columnarWritesEqualTheGormWrites runs the same batch through
// each on two identical databases and requires the same rows written and the same rows left —
// including the shapes a column-array statement is most likely to get wrong: duplicate device
// keys inside one batch (folded into one row BEFORE the statement, since ON CONFLICT cannot
// touch a row twice), a guard that excludes a row, an older / equal / newer reading, NULL
// times, identity, anchors and a session id.

func legacyConflict() []clause.Column {
	return []clause.Column{{Name: "tenant_id"}, {Name: "device_token"}}
}

var legacyHeartbeatGuard = clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "NOT (device_states.presence_source <> '" +
	PresenceSourceAsserted + "' AND NOT device_states.active AND (device_states.last_activity_time IS NULL " +
	"OR excluded.last_activity_time >= device_states.last_activity_time))"}}}

const legacyOwnersNewer = "excluded.scope_anchors_at IS NOT NULL AND (device_states.scope_anchors_at IS NULL " +
	"OR excluded.scope_anchors_at > device_states.scope_anchors_at)"

var legacyHeartbeatSet = clause.Set{
	{Column: clause.Column{Name: "external_id"}, Value: clause.Expr{SQL: "CASE WHEN excluded.external_id <> '' " +
		"THEN excluded.external_id ELSE device_states.external_id END"}},
	{Column: clause.Column{Name: "source"}, Value: clause.Expr{SQL: "CASE WHEN excluded.source <> '' " +
		"THEN excluded.source ELSE device_states.source END"}},
	{Column: clause.Column{Name: "last_activity_time"}, Value: clause.Expr{SQL: "CASE WHEN " +
		"device_states.last_activity_time IS NULL OR excluded.last_activity_time >= device_states.last_activity_time " +
		"THEN excluded.last_activity_time ELSE device_states.last_activity_time END"}},
	{Column: clause.Column{Name: "scope_anchors"}, Value: clause.Expr{SQL: "CASE WHEN " + legacyOwnersNewer +
		" THEN excluded.scope_anchors ELSE device_states.scope_anchors END"}},
	{Column: clause.Column{Name: "scope_anchors_at"}, Value: clause.Expr{SQL: "CASE WHEN " + legacyOwnersNewer +
		" THEN excluded.scope_anchors_at ELSE device_states.scope_anchors_at END"}},
	{Column: clause.Column{Name: "updated_at"}, Value: clause.Column{Table: "excluded", Name: "updated_at"}},
}

func legacyHeartbeat(tdb *gorm.DB, rows []DeviceState) (int64, error) {
	now := tdb.NowFunc()
	for i := range rows {
		rows[i].UpdatedAt = now
	}
	res := rdb.CreateChunked(tdb.Clauses(clause.OnConflict{
		Columns: legacyConflict(), DoUpdates: legacyHeartbeatSet, Where: legacyHeartbeatGuard,
	}), &rows)
	return res.RowsAffected, res.Error
}

func legacyLock(tdb *gorm.DB, rows []DeviceState) error {
	return rdb.CreateChunked(tdb.Clauses(clause.OnConflict{
		Columns: legacyConflict(),
		DoUpdates: clause.Set{{Column: clause.Column{Name: "updated_at"},
			Value: clause.Column{Table: "device_states", Name: "updated_at"}}},
		Where: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "1 = 0"}}},
	}), &rows).Error
}

func legacyFolded(tdb *gorm.DB, rows []DeviceState) error {
	cols, err := foldColumns(tdb)
	if err != nil {
		return err
	}
	now := tdb.NowFunc()
	for i := range rows {
		rows[i].UpdatedAt = now
	}
	return rdb.CreateChunked(tdb.Clauses(clause.OnConflict{
		Columns: legacyConflict(), DoUpdates: clause.AssignmentColumns(append(cols, "updated_at")),
	}), &rows).Error
}

func legacyMeasurements(tx *gorm.DB, rows []LatestMeasurement) error {
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "device_token"}, {Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "classifier", "unit", "data_type", "occurred_time", "updated_at"}),
		Where: clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "latest_measurements.occurred_time < excluded.occurred_time"}}},
	}).CreateInBatches(&rows, upsertChunk).Error
}

func legacyLocations(tx *gorm.DB, rows []LatestLocation) error {
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "device_token"}},
		DoUpdates: clause.AssignmentColumns([]string{"latitude", "longitude", "elevation", "accuracy", "speed",
			"heading", "occurred_time", "updated_at"}),
		Where: clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "latest_locations.occurred_time < excluded.occurred_time"}}},
	}).CreateInBatches(&rows, upsertChunk).Error
}

// columnarWritesEqualTheGormWrites seeds two empty databases identically, runs the same
// batches through the gorm writes on legacy and through the column-array writes on current, and
// requires the same counts and the same rows after each step.
func columnarWritesEqualTheGormWrites(t *testing.T, legacy, current *Api) {
	t.Helper()
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	ctx := core.WithTenant(context.Background(), "acme")

	// Seed both through the per-event (Save) path, which neither side of this comparison touches:
	// an active device, one the sweep then declares dead, an asserted one, one with owners, and
	// readings and a fix to be guarded.
	for _, api := range []*Api{legacy, current} {
		seed := []ProjectionUpdate{
			data("acme", "a-active", t0),
			data("acme", "b-dead", t0),
			claim("acme", "d-asserted", t0, presence.ClaimConnected, 3),
			reading("acme", "a-active", t0, "temp", 1),
			reading("acme", "a-active", t0, "hum", 50),
			fix("acme", "a-active", t0, 10),
		}
		for _, u := range seed {
			if err := mergeOneAtATime(api, u); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
		if _, err := api.SweepInactive(core.WithSystemContext(context.Background()), t0.Add(2*time.Hour)); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		for _, u := range []ProjectionUpdate{
			data("acme", "c-fresh", t0.Add(2*time.Hour)),
			{Tenant: "acme", DeviceToken: "g-identified", OccurredAt: t0.Add(2 * time.Hour),
				Identity: DeviceIdentity{ExternalId: "ext-g", Source: "src-g"}},
		} {
			if err := mergeOneAtATime(api, u); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
		if _, err := api.ApplyDeviceOwnership(ctx, "a-active", []string{"customer:acme", `customer:"q,{x}"`}, t0); err != nil {
			t.Fatalf("seed ownership: %v", err)
		}
		if _, err := api.ApplyDeviceOwnership(ctx, "g-identified", []string{"customer:g"}, t0); err != nil {
			t.Fatalf("seed ownership: %v", err)
		}
	}
	requireSameRows(t, "after the seed", legacy, current)

	t1 := t0.Add(2*time.Hour + time.Minute)
	// Several events per device, out of order: the batch's duplicate keys, which fold into ONE
	// row each before any statement is sent.
	updates := []ProjectionUpdate{
		{Tenant: "acme", DeviceToken: "a-active", OccurredAt: t1.Add(time.Minute), Identity: DeviceIdentity{ExternalId: "ext-a"}},
		{Tenant: "acme", DeviceToken: "a-active", OccurredAt: t1.Add(3 * time.Minute), Identity: DeviceIdentity{Source: "src-a"}},
		{Tenant: "acme", DeviceToken: "a-active", OccurredAt: t1.Add(2 * time.Minute)},
		data("acme", "b-dead", t1), // fresh for a dead inferred device: the guard excludes it
		data("acme", "b-dead", t1.Add(time.Second)),
		data("acme", "c-fresh", t0), // older than its stored activity
		data("acme", "d-asserted", t1),
		data("acme", "e-new", t1.Add(2*time.Second)),
		data("acme", "e-new", t1),
		{Tenant: "acme", DeviceToken: "f-new", OccurredAt: t1, Identity: DeviceIdentity{ExternalId: "ext-f", Source: "src-f"}},
		data("acme", "g-identified", t1), // no identity in the event: the stored one stays
	}
	devices := map[string][]int{}
	for i, u := range updates {
		devices[u.DeviceToken] = append(devices[u.DeviceToken], i)
	}
	tokens := make([]string, 0, len(devices))
	for tok := range devices {
		tokens = append(tokens, tok)
	}
	sort.Strings(tokens)
	tieStamp := loadRow(t, legacy, "acme", "g-identified").ScopeAnchorsAt
	if !tieStamp.Valid {
		t.Fatal("the seeded ownership left no stamp to tie with")
	}
	rowsFor := func() []DeviceState {
		rows := make([]DeviceState, len(tokens))
		for i, tok := range tokens {
			rows[i] = *foldNew(tok, devices[tok], updates)
			// An owners stamp older than a stored one, newer than none, and absent.
			switch tok {
			case "a-active":
				rows[i].ScopeAnchors = rdb.Anchors{"customer:older"}
				rows[i].ScopeAnchorsAt = sql.NullTime{Time: t0.Add(-time.Hour), Valid: true}
			case "g-identified":
				// The SAME stamp as the stored one with different owners: not strictly later, so it
				// must not replace them.
				rows[i].ScopeAnchors = rdb.Anchors{"customer:tie"}
				rows[i].ScopeAnchorsAt = tieStamp
			case "e-new", "d-asserted":
				rows[i].ScopeAnchors = rdb.Anchors{"customer:acme", "site:1"}
				rows[i].ScopeAnchorsAt = sql.NullTime{Time: t1, Valid: true}
			}
		}
		return rows
	}

	// 1. The heartbeat upsert.
	var wrote [2]int64
	for i, api := range []*Api{legacy, current} {
		rows := rowsFor()
		if err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) (err error) {
			if i == 0 {
				wrote[i], err = legacyHeartbeat(tx, rows)
			} else {
				wrote[i], err = upsertDeviceStates(tx, deviceStateHeartbeat, rows)
			}
			return err
		}); err != nil {
			t.Fatalf("heartbeat upsert (%d): %v", i, err)
		}
	}
	// a-active and b-dead were swept inactive and the batch is fresh for them: the guard excludes
	// both (they are locked and left alone), so 5 of the 7 are written.
	if wrote[0] != wrote[1] || wrote[0] != int64(len(tokens)-2) {
		t.Errorf("heartbeat upsert wrote legacy=%d columnar=%d rows; want both %d (the guard excludes the two swept devices)",
			wrote[0], wrote[1], len(tokens)-2)
	}
	requireSameRows(t, "after the heartbeat upsert", legacy, current)

	// 2. The locked path: lock-or-create, fold the claims, write back.
	claims := []ProjectionUpdate{
		claim("acme", "a-active", t1.Add(time.Hour), presence.ClaimConnected, 9),
		claim("acme", "b-dead", t1.Add(time.Hour), presence.ClaimDisconnected, 2),
		claim("acme", "d-asserted", t1.Add(time.Hour), presence.ClaimDemoted, 3),
		claim("acme", "h-claimed", t1.Add(time.Hour), presence.ClaimConnected, 1),
		data("acme", "h-claimed", t1.Add(time.Hour+time.Second)),
	}
	cdev := map[string][]int{}
	var ctoks []string
	for i, u := range claims {
		if _, ok := cdev[u.DeviceToken]; !ok {
			ctoks = append(ctoks, u.DeviceToken)
		}
		cdev[u.DeviceToken] = append(cdev[u.DeviceToken], i)
	}
	sort.Strings(ctoks)
	for i, api := range []*Api{legacy, current} {
		if err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			blanks := make([]DeviceState, len(ctoks))
			for j, tok := range ctoks {
				blanks[j] = *blankDeviceState(tok)
			}
			if i == 0 {
				if err := legacyLock(tx, blanks); err != nil {
					return err
				}
			} else if err := lockOrCreate(tx, ctoks); err != nil {
				return err
			}
			sel, err := foldSelect(tx)
			if err != nil {
				return err
			}
			var rows []DeviceState
			if err := tx.Select(sel).Where("device_token IN ?", ctoks).Order("device_token").Find(&rows).Error; err != nil {
				return err
			}
			for j := range rows {
				foldInto(&rows[j], cdev[rows[j].DeviceToken], claims)
			}
			if i == 0 {
				return legacyFolded(tx, rows)
			}
			return writeFoldedStates(tx, rows)
		}); err != nil {
			t.Fatalf("locked path (%d): %v", i, err)
		}
	}
	requireSameRows(t, "after the locked path", legacy, current)

	// 3. Readings: an older, an equal and a newer one against what is stored, with the same
	// name twice in the input (coalesced to one row, the first of equal times).
	at := t1.Add(10 * time.Minute)
	var inputs [][]LatestMeasurementInput
	for _, in := range [][]LatestMeasurementInput{
		{{Name: "temp", Value: num(0), OccurredTime: t0.Add(-time.Second)}},
		{{Name: "temp", Value: num(2), OccurredTime: at}, {Name: "temp", Value: num(3), OccurredTime: at}},
		{{Name: "hum", Value: num(50), OccurredTime: t0}},
		{{Name: "hum", Value: sql.NullFloat64{}, OccurredTime: at}},
		{{Name: "pressure", Value: num(1013.25), OccurredTime: at, Unit: ptr("hPa"), DataType: ptr("DOUBLE"), Classifier: ptr(uint(7))}},
	} {
		inputs = append(inputs, in)
	}
	var meas []LatestMeasurement
	for _, tok := range []string{"a-active", "e-new"} {
		for _, in := range inputs {
			meas = append(meas, coalesceMeasurements(tok, in)...)
		}
	}
	// One row per (device, name), as writeLatest hands them over: the coalesced rows of one
	// device repeat names across the inputs above, so fold them per device first.
	meas = oneRowPerKey(meas)
	loc := []LatestLocation{
		*coalesceLocations("a-active", []LatestLocationInput{{Latitude: num(11), Longitude: num(-1.5), OccurredTime: at},
			{Latitude: num(12), Longitude: num(-1.25), Elevation: num(8.5), Speed: num(3), OccurredTime: at.Add(time.Second)}}),
		*coalesceLocations("e-new", []LatestLocationInput{{Latitude: num(1), Longitude: num(2), OccurredTime: at}}),
		*coalesceLocations("c-fresh", []LatestLocationInput{{Latitude: num(5), Longitude: num(6), OccurredTime: t0.Add(-time.Hour)}}),
	}
	for i, api := range []*Api{legacy, current} {
		if err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			if i == 0 {
				if err := legacyMeasurements(tx, slices.Clone(meas)); err != nil {
					return err
				}
				return legacyLocations(tx, slices.Clone(loc))
			}
			if err := upsertLatestMeasurements(tx, slices.Clone(meas)); err != nil {
				return err
			}
			return upsertLatestLocations(tx, slices.Clone(loc))
		}); err != nil {
			t.Fatalf("latest values (%d): %v", i, err)
		}
	}
	requireSameRows(t, "after the latest-value upserts", legacy, current)

	// The same fix again, and the same readings: redelivery changes nothing on either side.
	for i, api := range []*Api{legacy, current} {
		if err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			if i == 0 {
				return legacyMeasurements(tx, slices.Clone(meas))
			}
			return upsertLatestMeasurements(tx, slices.Clone(meas))
		}); err != nil {
			t.Fatalf("redelivery (%d): %v", i, err)
		}
	}
	requireSameRows(t, "after a redelivery", legacy, current)

	// A value at the stored one's EXACT time but different: the guard is strict, so neither side
	// replaces it. temp2 is written at t0 as 1 and then as 77; a-active's fix, stored at
	// at+1s as latitude 12, is offered again at that instant as 99.
	for i, api := range []*Api{legacy, current} {
		if err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			for _, v := range []float64{1, 77} {
				row := coalesceMeasurements("a-active", []LatestMeasurementInput{{Name: "temp2", Value: num(v), OccurredTime: t0}})
				if i == 0 {
					if err := legacyMeasurements(tx, row); err != nil {
						return err
					}
				} else if err := upsertLatestMeasurements(tx, row); err != nil {
					return err
				}
			}
			tie := []LatestLocation{*coalesceLocations("a-active",
				[]LatestLocationInput{{Latitude: num(99), OccurredTime: at.Add(time.Second)}})}
			if i == 0 {
				return legacyLocations(tx, tie)
			}
			return upsertLatestLocations(tx, tie)
		}); err != nil {
			t.Fatalf("equal-time writes (%d): %v", i, err)
		}
	}
	requireSameRows(t, "after equal-time writes", legacy, current)
}

func ptr[T any](v T) *T { return &v }

// oneRowPerKey keeps, for every (device, name), the row with the latest time (the first of
// equals), sorted by device then name: the shape upsertLatestMeasurements is documented to take.
func oneRowPerKey(rows []LatestMeasurement) []LatestMeasurement {
	type key struct{ dev, name string }
	best := map[key]LatestMeasurement{}
	for _, r := range rows {
		k := key{r.DeviceToken, r.Name}
		if cur, ok := best[k]; !ok || r.OccurredTime.After(cur.OccurredTime) {
			best[k] = r
		}
	}
	out := make([]LatestMeasurement, 0, len(best))
	for _, r := range best {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DeviceToken != out[j].DeviceToken {
			return out[i].DeviceToken < out[j].DeviceToken
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func requireSameRows(t *testing.T, when string, legacy, current *Api) {
	t.Helper()
	want, got := projectionRows(t, legacy), projectionRows(t, current)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("%s the column-array writes left different rows than the gorm writes.\n  gorm:\n    %v\n  columnar:\n    %v",
			when, joinLines(want), joinLines(got))
	}
}

func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n    "
		}
		out += l
	}
	return out
}

func TestColumnarWritesEqualTheGormWrites(t *testing.T) {
	columnarWritesEqualTheGormWrites(t, newSQLiteProjectionApi(t, "legacy"), newSQLiteProjectionApi(t, "current"))
}

// The update arm of the folded write-back is the fold's columns, exactly: a column the fold
// learns to touch (foldColumns is derived from the model) and the SET does not list would be
// read and folded and then silently not written back.
func TestDeviceStateUpdateArmCoversTheFold(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	cols, err := foldColumns(api.RDB.DB(core.WithSystemContext(context.Background())))
	if err != nil {
		t.Fatalf("fold columns: %v", err)
	}
	cols = append(cols, "updated_at")
	sort.Strings(cols)
	// The SQLite rendering names the assigned columns in the statement text.
	var sqlText string
	ctx := core.WithTenant(context.Background(), "acme")
	if err := api.RDB.Database.Callback().Raw().After("gorm:raw").Register("test:folded_sql", func(tx *gorm.DB) {
		if writesDeviceStates(tx) {
			sqlText = tx.Statement.SQL.String()
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	if err := writeFoldedStates(api.RDB.DB(ctx), []DeviceState{*blankDeviceState("d1")}); err != nil {
		t.Fatalf("write back: %v", err)
	}
	_, arm, ok := strings.Cut(sqlText, " DO UPDATE SET ")
	if !ok {
		t.Fatalf("the write-back is not an upsert: %s", sqlText)
	}
	arm, _, _ = strings.Cut(arm, " WHERE ")
	var assigned []string
	for _, part := range strings.Split(arm, ", ") {
		col, _, _ := strings.Cut(part, " = ")
		assigned = append(assigned, strings.Trim(col, "`\""))
	}
	sort.Strings(assigned)
	if !reflect.DeepEqual(assigned, cols) {
		t.Errorf("the folded write-back assigns %v; the fold reads and writes %v", assigned, cols)
	}
}
