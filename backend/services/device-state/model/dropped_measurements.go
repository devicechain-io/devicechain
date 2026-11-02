// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MaxDroppedMeasurementsPerDevice bounds the drop record: at most this many (name, reason) rows
// per device. A device that sends many distinct text keys cannot grow the table without limit;
// a 33rd distinct (name, reason) evicts the row with the oldest last_time. Losing a row to
// eviction loses a hint about which key is being discarded, never data.
const MaxDroppedMeasurementsPerDevice = 32

// evictScan is the most rows over the bound one device's eviction reads. It is a LIMIT only
// because SQLite refuses an OFFSET without one; a device is a handful of rows over the bound, at
// most one batch's worth, far below it.
const evictScan = 100000

// dropped-record column widths. name matches latest_measurements.name; the value is cut by the
// producer to resolvedevent.MaxDroppedValueBytes and again here, so a producer that did not
// cannot fail the write.
const (
	droppedNameMax  = 128
	droppedValueMax = resolvedevent.MaxDroppedValueBytes
)

// DroppedMeasurement says that a device sent a measurement key the platform discarded at
// resolution while it stored the rest of the event: which key, why, the last value it sent
// and how often. One row per (tenant, device, name, reason), bounded per device
// (MaxDroppedMeasurementsPerDevice), so it never grows with history.
//
// It is a DIAGNOSTIC the device's owner can read ("this device is sending a key we discard,
// last at T, N times") — the fleet-wide rate is resolve_measurement_dropped_total in
// device-management, which has no tenant, device or key label. The remedy is the same for every
// reason: declare the key as a numeric metric on the device's profile to store it.
//
// LastValue is the device's own content, shown to exactly the readers who may see the device's
// latest readings (ScopedBy), customer scopes included.
type DroppedMeasurement struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	// TenantId is spelled out rather than embedded from rdb.TenantScoped (the tenant-scope
	// callback finds it by name either way) so it can LEAD the unique index the projection's
	// upserts arbitrate on: a device token is unique only per tenant. Production never
	// AutoMigrates this struct — the migration creates that index from its own snapshot —
	// so the tag is for the databases tests build from the live model, where without it
	// `ON CONFLICT (tenant_id, device_token, name, reason)` has no arbiter and is refused.
	TenantId    string `gorm:"uniqueIndex:idx_dropped_measurement_tenant_device_name_reason,priority:1;not null;size:128"`
	DeviceToken string `gorm:"uniqueIndex:idx_dropped_measurement_tenant_device_name_reason,priority:2;not null;type:varchar(128)"`
	Name        string `gorm:"uniqueIndex:idx_dropped_measurement_tenant_device_name_reason,priority:3;not null;size:128"`
	// Reason is one of resolvedevent.DroppedReasons.
	Reason string `gorm:"uniqueIndex:idx_dropped_measurement_tenant_device_name_reason,priority:4;not null;size:32"`
	// LastValue is the value last sent, cut to resolvedevent.MaxDroppedValueBytes. It follows
	// LastTime, not arrival.
	LastValue string `gorm:"not null;type:varchar(64)"`
	// Count is how many times the key was dropped. It can run ahead of the truth by the events
	// the stream redelivered: a diagnostic's "how often", not an audit count.
	Count     int64     `gorm:"not null;default:0"`
	FirstTime time.Time `gorm:"not null"`
	LastTime  time.Time `gorm:"not null"`
}

// AuditExempt: a high-volume derived diagnostic projection, not a control-plane mutation
// (ADR-019) — same rationale as DeviceState.
func (DroppedMeasurement) AuditExempt() bool { return true }

// ScopedBy implements rdb.ScopedModel: a drop record is visible when its device's live state is
// (deviceOwners) — the same visibility as the device's latest readings.
func (DroppedMeasurement) ScopedBy() rdb.ScopeRule { return deviceOwners }

// DroppedMeasurementInput is one discarded entry to fold into the drop record.
type DroppedMeasurementInput struct {
	Name         string
	Reason       string
	Value        string
	OccurredTime time.Time
}

// droppedKey is the unique key of a drop row within one device.
type droppedKey struct{ name, reason string }

// cutTo cuts s to at most n bytes on a UTF-8 boundary, after removing NUL: PostgreSQL refuses
// 0x00 in a varchar (SQLSTATE 22021), which would fail the write of the whole batch every time.
// The producer strips it too; this is for a producer that did not.
func cutTo(s string, n int) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// coalesceDropped reduces one device's drop inputs, in arrival order, to one row per
// (name, reason): count is the number of inputs, first_time the earliest, last_time the latest
// and last_value the value of the latest (of equal times, the one that ARRIVED FIRST — the rule
// the latest-value projection applies). It is required rather than an optimisation: PostgreSQL
// refuses an INSERT … ON CONFLICT DO UPDATE that touches one row twice. An entry whose reason is
// not in the shared vocabulary is skipped (a producer newer than this reader; the GraphQL enum
// could not name it). Rows are sorted by (name, reason), so every writer locks them in one
// order. TenantId is left empty for the tenant-scope callback to stamp.
func coalesceDropped(deviceToken string, inputs []DroppedMeasurementInput) []DroppedMeasurement {
	byKey := make(map[droppedKey]int, len(inputs))
	rows := make([]DroppedMeasurement, 0, len(inputs))
	for _, in := range inputs {
		name := cutTo(in.Name, droppedNameMax)
		if !knownDroppedReason(in.Reason) || name == "" {
			continue
		}
		value := cutTo(in.Value, droppedValueMax)
		at := storedTime(in.OccurredTime)
		k := droppedKey{name, in.Reason}
		if i, ok := byKey[k]; ok {
			r := &rows[i]
			r.Count++
			if at.Before(r.FirstTime) {
				r.FirstTime = at
			}
			if at.After(r.LastTime) {
				r.LastTime, r.LastValue = at, value
			}
			continue
		}
		byKey[k] = len(rows)
		rows = append(rows, DroppedMeasurement{DeviceToken: deviceToken, Name: name, Reason: in.Reason,
			LastValue: value, Count: 1, FirstTime: at, LastTime: at})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].Reason < rows[j].Reason
	})
	return rows
}

func knownDroppedReason(r string) bool {
	for _, k := range resolvedevent.DroppedReasons {
		if k == r {
			return true
		}
	}
	return false
}

// droppedSet is the DO UPDATE of the drop upsert: the stored row is the table's name and the
// incoming one is excluded. count adds; first_time keeps the earliest; last_time keeps the
// latest, so an event delivered late does not move it back (the rule lastActivityTime
// follows); last_value follows last_time. A SET reads the row as it was before the statement on
// both databases, so the arms agree on whether the incoming row is the newer. Written with CASE
// rather than LEAST/GREATEST so the same statement runs on SQLite, where the unit tests run.
var droppedSet = clause.Set{
	{Column: clause.Column{Name: "count"}, Value: clause.Expr{SQL: "dropped_measurements.count + excluded.count"}},
	{Column: clause.Column{Name: "first_time"}, Value: clause.Expr{SQL: "CASE WHEN excluded.first_time < " +
		"dropped_measurements.first_time THEN excluded.first_time ELSE dropped_measurements.first_time END"}},
	{Column: clause.Column{Name: "last_value"}, Value: clause.Expr{SQL: "CASE WHEN excluded.last_time > " +
		"dropped_measurements.last_time THEN excluded.last_value ELSE dropped_measurements.last_value END"}},
	{Column: clause.Column{Name: "last_time"}, Value: clause.Expr{SQL: "CASE WHEN excluded.last_time > " +
		"dropped_measurements.last_time THEN excluded.last_time ELSE dropped_measurements.last_time END"}},
	{Column: clause.Column{Name: "updated_at"}, Value: clause.Column{Table: "excluded", Name: "updated_at"}},
}

// evictions carries how many rows a transaction's bound enforcement deleted, from the statement
// that deleted them to the caller that reports them once the transaction has committed (a rolled
// back batch is merged again, and would otherwise be counted twice). It rides on the context
// because the upsert runs several calls below the transaction.
type evictions struct{ n int64 }

type evictionsKey struct{}

func withEvictions(ctx context.Context) (context.Context, *evictions) {
	e := &evictions{}
	return context.WithValue(ctx, evictionsKey{}, e), e
}

func noteEvicted(ctx context.Context, n int64) {
	if e, ok := ctx.Value(evictionsKey{}).(*evictions); ok && n > 0 {
		e.n += n
	}
}

// reportEvictions hands a committed transaction's evictions to the service's counter.
func (api *Api) reportEvictions(e *evictions) {
	if e != nil && e.n > 0 && api.OnDroppedEvicted != nil {
		api.OnDroppedEvicted(e.n)
	}
}

// upsertDropped writes rows — one tenant's, sorted by device token then name and reason, each
// (device, name, reason) at most once — and then enforces the per-device bound for tokens.
func upsertDropped(tdb *gorm.DB, tokens []string, rows []DroppedMeasurement) error {
	if len(rows) == 0 {
		return nil
	}
	if err := tdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "device_token"}, {Name: "name"}, {Name: "reason"}},
		DoUpdates: droppedSet,
	}).CreateInBatches(&rows, upsertChunk).Error; err != nil {
		return err
	}
	return enforceDroppedBound(tdb, tokens)
}

// enforceDroppedBound deletes, for each of tokens holding more than
// MaxDroppedMeasurementsPerDevice rows, the rows beyond the newest MaxDroppedMeasurementsPerDevice
// by last_time (the row id breaks a tie, newest first). A batch that touched no device over the
// bound costs one grouped read.
//
// It runs inside the batch's transaction after the upsert, so the row that tipped a device over
// is counted — and may be the one evicted, when its last_time is the oldest (an old event
// delivered late for a 33rd key). That is the rule as stated: the oldest last_time goes.
func enforceDroppedBound(tdb *gorm.DB, tokens []string) error {
	ctx := tdb.Statement.Context
	var over []struct {
		DeviceToken string
		N           int64
	}
	if err := tdb.Model(&DroppedMeasurement{}).Select("device_token, count(*) AS n").
		Where("device_token IN ?", tokens).Group("device_token").
		Having("count(*) > ?", MaxDroppedMeasurementsPerDevice).Scan(&over).Error; err != nil {
		return err
	}
	tenant, _ := core.TenantFromContext(ctx)
	for _, o := range over {
		// ONE statement picks and deletes: the ids are chosen by the ordering at the moment of
		// the delete, so a row refreshed since a separate read cannot be deleted on its old
		// rank. The subquery is built outside the tenant-scope callback, so it names the tenant
		// itself (a device token is unique only per tenant).
		beyond := tdb.Session(&gorm.Session{NewDB: true}).Model(&DroppedMeasurement{}).Select("id").
			Where("tenant_id = ? AND device_token = ?", tenant, o.DeviceToken).
			Order("last_time DESC, id DESC").Offset(MaxDroppedMeasurementsPerDevice).Limit(evictScan)
		res := tdb.Where("id IN (?)", beyond).Delete(&DroppedMeasurement{})
		if res.Error != nil {
			return res.Error
		}
		noteEvicted(ctx, res.RowsAffected)
	}
	return nil
}

// MergeDroppedMeasurements folds the entries one resolved measurement event had discarded into
// the device's drop record: the per-event path, in a transaction of its own.
func (api *Api) MergeDroppedMeasurements(ctx context.Context, deviceToken string, inputs []DroppedMeasurementInput) error {
	rows := coalesceDropped(deviceToken, inputs)
	if len(rows) == 0 {
		return nil
	}
	ctx, ev := withEvictions(ctx)
	if err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// 🔴 Serialize with every other writer of this device the way the batch does: take the
		// device's device_states row lock FIRST. Without it this transaction upserts and evicts
		// drop rows while a batch holds the device row and does the same, and the two can meet
		// in opposite row orders (40P01). With it a device's drop writes happen one at a time,
		// whichever path they arrive by.
		if err := lockOrCreate(tx, []string{deviceToken}); err != nil {
			return err
		}
		return upsertDropped(tx, []string{deviceToken}, rows)
	}); err != nil {
		return err
	}
	api.reportEvictions(ev)
	return nil
}

// DroppedMeasurementsByDeviceToken returns a device's drop record, name then reason ordered. A
// scoped member sees it only for a device their customers own (ScopedBy).
func (api *Api) DroppedMeasurementsByDeviceToken(ctx context.Context, deviceToken string) ([]*DroppedMeasurement, error) {
	found := make([]*DroppedMeasurement, 0)
	if err := api.RDB.DB(ctx).Where("device_token = ?", deviceToken).Order("name asc, reason asc").Find(&found).Error; err != nil {
		return nil, err
	}
	return found, nil
}

// DroppedMeasurementCount is the number of rows in a device's drop record (distinct
// (name, reason) pairs, not events): the cheap badge.
func (api *Api) DroppedMeasurementCount(ctx context.Context, deviceToken string) (int64, error) {
	var n int64
	if err := api.RDB.DB(ctx).Model(&DroppedMeasurement{}).Where("device_token = ?", deviceToken).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}
