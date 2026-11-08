// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// MaxUsageRangeDays bounds one tenantEventUsage read. A daily record is a few dozen bytes, so the
// bound is about keeping one request's answer (and its zero-filled days) a sensible size.
const MaxUsageRangeDays = 366

// ErrUsageRange is returned for a usage range that is empty, inverted or longer than
// MaxUsageRangeDays.
var ErrUsageRange = errors.New("invalid usage range")

// UsageSettle is how long after an hour closes the rollup waits before computing it as FINAL.
//
// An event is bucketed by when the platform RECEIVED it (events.processed_time), and it is stored
// a moment after that, once the persistence consumer has written it. The settle window is the room
// given to that gap. 🔴 THE PRICE, stated plainly: an event received in an hour but stored more
// than this long after the hour closed (a persistence backlog longer than the window) is stored
// and served like any other, but is not counted in the usage record, because its hour was already
// final when it arrived and a final hour is never rewritten.
const UsageSettle = 10 * time.Minute

// usageHourTimeout bounds every statement of one hour's transaction, so a pass that meets a slow
// store fails that hour (and retries it on the next pass) rather than holding the lock forever.
const usageHourTimeout = "120s"

// usageStateName names the rollup's row in tenant_usage_state.
const usageStateName = "hourly"

// TenantEventUsageDay is one tenant's event usage for one UTC calendar day.
//
// 🔴 WHAT EACH NUMBER MEANS, because a billing figure that is "roughly" something is not one.
// Every figure is bucketed by INGEST time: the time the platform received the event
// (events.processed_time), not the time the event is dated.
//   - EventsIngested counts each stored event once, in the UTC day it was received. A backfilled
//     or late reading is counted on the day it arrived, never on the day it is dated, so a day
//     that is complete is never credited afterwards. A redelivered duplicate is stored once and so
//     is counted once.
//   - DevicesActive is the number of distinct devices with at least one event received that day.
//   - DevicesPeak is the largest number of distinct devices with an event received in any single
//     UTC hour.
//   - DevicesAvg is the mean of the 24 hourly distinct-device counts; an hour with no events counts
//     as zero, and so does an hour not yet final (Complete says whether all of them are).
//
// Devices that are registered but silent are NOT counted: this record measures what a tenant used,
// from the event store, and the registry of devices lives in another area. A tenant the instance
// has never heard of reads as zeros, exactly like one that sent nothing: the record does not
// consult the tenant catalogue.
type TenantEventUsageDay struct {
	TenantId       string
	Day            time.Time // midnight UTC
	EventsIngested int64
	DevicesActive  int
	DevicesPeak    int
	DeviceHours    int64
	// Complete is true once every hour of the day is FINAL: computed after it closed and its
	// settle window passed, never to be rewritten. A day not (or never) fully final is not
	// complete, so a zero on it means "not measured yet", never "measured nothing".
	Complete bool
	// RolledUpAt is when the row was last computed; zero for a day that has no row.
	RolledUpAt time.Time
}

// DevicesAvg is the mean number of devices active per UTC hour over the 24 hours of the day.
func (d TenantEventUsageDay) DevicesAvg() float64 {
	return float64(d.DeviceHours) / 24.0
}

// usageDayStart truncates t to midnight UTC.
func usageDayStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// usageHourStart truncates t to the start of its UTC hour.
func usageHourStart(t time.Time) time.Time {
	return t.UTC().Truncate(time.Hour)
}

// usageHourSettled reports whether the hour starting at h is past its settle window at now.
func usageHourSettled(h, now time.Time) bool {
	return !h.Add(time.Hour + UsageSettle).After(now)
}

// ValidateUsageRange checks a [from, to] range of UTC days, inclusive on both ends.
func ValidateUsageRange(from, to time.Time) error {
	f, e := usageDayStart(from), usageDayStart(to)
	if e.Before(f) {
		return fmt.Errorf("%w: to is before from", ErrUsageRange)
	}
	if days := int(e.Sub(f).Hours()/24) + 1; days > MaxUsageRangeDays {
		return fmt.Errorf("%w: %d days requested, at most %d", ErrUsageRange, days, MaxUsageRangeDays)
	}
	return nil
}

// usageHourSQL computes one UTC hour of INGEST time for every tenant: the hourly rows and the
// day's device set, from ONE read of the hour's events.
//
// 🔴 CROSS-TENANT BY CONSTRUCTION AND TENANT-SAFE BY GROUPING: the rollup runs under the system
// context because it must see every tenant's events, and every write groups or keys by tenant_id
// so no count can mix two tenants. The two-tenant integration test reuses a device token across
// tenants to hold that.
//
// HOW IT READS. The hour is a half-open range on processed_time, which is NOT the hypertable's
// partition column, so the read is carried by idx_events_processed_time_brin: a bitmap scan of a
// few BRIN pages per uncompressed chunk, and on a compressed chunk a filter on the per-batch
// min/max TimescaleDB keeps for an indexed column, so batches outside the hour are never
// decompressed. MATERIALIZED is load-bearing: inlined, the planner feeds the GROUP BY from the
// (tenant_id, device_token, occurred_time) btree and reads every row of every chunk (measured:
// 564 ms against 24 ms on a 4M-row store; see the PR). The hour is passed in as an instant, never
// cut with date_trunc, which on a timestamptz uses the session time zone.
//
// A tenant whose erasure is in progress (its fence in purged_tenants stands) is skipped: its
// events are being deleted, and a usage row written now would be tenant data the purge must chase.
// A fence that has COMPLETED does not skip: the token may since belong to a successor tenant.
//
// %s is the optional scan floor (see usageFloorClause).
const usageHourSQL = `
WITH hour_rows AS MATERIALIZED (
	SELECT e.tenant_id, e.device_token
	FROM "event-management".events e
	WHERE e.processed_time >= @start AND e.processed_time < @end%s
	  AND NOT EXISTS (SELECT 1 FROM "event-management".purged_tenants p
	                  WHERE p.token = e.tenant_id AND p.completed_at IS NULL)
), devices AS (
	INSERT INTO "event-management".tenant_usage_devices (tenant_id, day, device_token)
	SELECT DISTINCT tenant_id, CAST(@day AS date), device_token FROM hour_rows
	ORDER BY 1, 3
	ON CONFLICT DO NOTHING
)
INSERT INTO "event-management".tenant_usage_hourly (tenant_id, hour, events, devices)
SELECT tenant_id, @start, count(*), count(DISTINCT device_token)
FROM hour_rows
GROUP BY tenant_id
ORDER BY tenant_id`

// usageFloorClause bounds the read below by event time, when the store holds chunks compressed
// before idx_events_processed_time_brin existed. Those chunks carry no processed_time metadata, so
// an hour's read would have to decompress every one of them on every pass. The floor (recorded
// once, on the rollup's first pass: the newest such chunk's end) leaves them out. The cost is
// narrow and stated: an event received after the upgrade but DATED inside those older chunks is
// not counted. A fresh install has no such chunks and no floor.
const usageFloorClause = `
	  AND e.occurred_time >= @floor`

// usageClearHourSQL removes an hour's rows before it is recomputed, so a tenant with no events in
// the hour any more keeps no stale row.
const usageClearHourSQL = `DELETE FROM "event-management".tenant_usage_hourly WHERE hour = @start`

// usageClearDaySQL removes the daily row of a tenant that no longer has any hour that day.
const usageClearDaySQL = `
DELETE FROM "event-management".tenant_usage_daily d
WHERE d.day = CAST(@day AS date)
  AND NOT EXISTS (SELECT 1 FROM "event-management".tenant_usage_hourly h
                  WHERE h.tenant_id = d.tenant_id AND h.hour >= @start AND h.hour < @end)`

// usageDaySQL derives the daily row of every tenant that has an hour that day from the working
// tables. A tenant with no events that day has no hourly row and so no daily row; the read side
// reports it as zero.
const usageDaySQL = `
INSERT INTO "event-management".tenant_usage_daily
	(tenant_id, day, events_ingested, devices_active, devices_peak, device_hours, rolled_up_at)
SELECT h.tenant_id, CAST(@day AS date), sum(h.events),
       (SELECT count(*) FROM "event-management".tenant_usage_devices d
         WHERE d.tenant_id = h.tenant_id AND d.day = CAST(@day AS date)),
       max(h.devices), sum(h.devices), @now
FROM "event-management".tenant_usage_hourly h
WHERE h.hour >= @start AND h.hour < @end
GROUP BY h.tenant_id
ORDER BY h.tenant_id
ON CONFLICT (tenant_id, day) DO UPDATE SET
	events_ingested = EXCLUDED.events_ingested,
	devices_active = EXCLUDED.devices_active,
	devices_peak = EXCLUDED.devices_peak,
	device_hours = EXCLUDED.device_hours,
	rolled_up_at = EXCLUDED.rolled_up_at`

// usageLedgerSQL marks one hour of a day final. The mask only ever gains bits.
const usageLedgerSQL = `
INSERT INTO "event-management".tenant_usage_ledger (day, hours, updated_at)
VALUES (CAST(@day AS date), @bit, @now)
ON CONFLICT (day) DO UPDATE SET
	hours = "event-management".tenant_usage_ledger.hours | EXCLUDED.hours,
	updated_at = EXCLUDED.updated_at`

// usageFullDay is the ledger mask of a day whose 24 hours are all final.
const usageFullDay = 1<<24 - 1

// usageState is the rollup's persisted watermark.
type usageState struct {
	NextHour  time.Time
	ScanFloor *time.Time
}

// readUsageState reads the watermark, or ok=false before the rollup's first pass.
func readUsageState(db *gorm.DB) (usageState, bool, error) {
	var rows []usageState
	err := db.Raw(`SELECT next_hour, scan_floor FROM "event-management".tenant_usage_state WHERE name = ?`,
		usageStateName).Scan(&rows).Error
	if err != nil {
		return usageState{}, false, fmt.Errorf("reading the usage rollup state: %w", err)
	}
	if len(rows) == 0 {
		return usageState{}, false, nil
	}
	s := rows[0]
	s.NextHour = s.NextHour.UTC()
	return s, true, nil
}

// initUsageState writes the watermark on the rollup's first pass: it starts at the beginning of
// the current UTC day, so today is measured in full and nothing older is scanned, and it records
// the scan floor (see usageFloorClause). A concurrent first pass loses the insert and reads the
// winner's row.
func initUsageState(db *gorm.DB, now time.Time) (usageState, error) {
	var floor []struct{ Floor *time.Time }
	if err := db.Raw(`SELECT max(range_end) AS floor FROM timescaledb_information.chunks
		WHERE hypertable_schema = 'event-management' AND hypertable_name = 'events' AND is_compressed`).
		Scan(&floor).Error; err != nil {
		return usageState{}, fmt.Errorf("reading compressed event chunks: %w", err)
	}
	var f *time.Time
	if len(floor) > 0 {
		f = floor[0].Floor
	}
	if err := db.Exec(`INSERT INTO "event-management".tenant_usage_state (name, next_hour, scan_floor, updated_at)
		VALUES (?, ?, ?, ?) ON CONFLICT (name) DO NOTHING`, usageStateName, usageDayStart(now), f, now).Error; err != nil {
		return usageState{}, fmt.Errorf("writing the usage rollup state: %w", err)
	}
	s, ok, err := readUsageState(db)
	if err == nil && !ok {
		err = errors.New("the usage rollup state was not written")
	}
	return s, err
}

// RollupUsagePass computes, in order, the hours the rollup owes that are past their settle window,
// at most maxHours of them, and returns how many it finalized.
//
// db is the handle the caller's lock holds (rdb.TryAdvisoryLockSession), so the watermark is read
// and advanced on the lock's own connection: one pass covers every replica, and an hour is
// computed once. Each hour advances the watermark in its own transaction, so a failure leaves
// every earlier hour final and the next pass resumes at the one that failed. The cap keeps one
// pass short after an outage; the rollup catches up a few hours per pass.
func (a *Api) RollupUsagePass(ctx context.Context, db *gorm.DB, now time.Time, maxHours int) (int, error) {
	// System context: the rollup reads every tenant's events by design and keys every write by
	// tenant_id itself (see usageHourSQL).
	sdb := db.WithContext(core.WithSystemContext(ctx))
	now = now.UTC()
	state, ok, err := readUsageState(sdb)
	if err != nil {
		return 0, err
	}
	if !ok {
		if state, err = initUsageState(sdb, now); err != nil {
			return 0, err
		}
	}
	done := 0
	for h := state.NextHour; done < maxHours && usageHourSettled(h, now); h = h.Add(time.Hour) {
		if _, err := rollupHour(sdb, h, state.ScanFloor, now, true); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// RollupUsageHours computes the UTC hours from the hour containing from to the hour containing to,
// both inclusive, without touching the watermark. It is the rollup's unit of work exposed for
// tests and repair; the sweep uses RollupUsagePass. An hour already final is left as it is.
func (a *Api) RollupUsageHours(ctx context.Context, from, to, now time.Time) (int64, error) {
	// System context: as RollupUsagePass.
	db := a.RDB.DB(core.WithSystemContext(ctx))
	state, _, err := readUsageState(db)
	if err != nil {
		return 0, err
	}
	var written int64
	for h := usageHourStart(from); !h.After(usageHourStart(to)); h = h.Add(time.Hour) {
		n, err := rollupHour(db, h, state.ScanFloor, now.UTC(), false)
		if err != nil {
			return written, err
		}
		written += n
	}
	return written, nil
}

// rollupHour computes one hour in one transaction: the hour's rows, its day's derived rows, its
// ledger bit once it is settled, and, when advance is set, the watermark. It returns the number of
// (tenant, hour) rows written.
//
// 🔴 A FINAL HOUR IS NEVER REWRITTEN. Its ledger bit is checked first, inside the transaction, and
// a set bit ends the work: the figure may already have been billed, and the raw events behind it
// are later removed by retention, so recomputing it could only move a number someone relied on.
func rollupHour(db *gorm.DB, h time.Time, floor *time.Time, now time.Time, advance bool) (int64, error) {
	d := usageDayStart(h)
	day := d.Format("2006-01-02")
	var written int64
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL statement_timeout = '` + usageHourTimeout + `'`).Error; err != nil {
			return err
		}
		var mask []struct{ Hours int64 }
		if err := tx.Raw(`SELECT hours FROM "event-management".tenant_usage_ledger WHERE day = ?::date FOR UPDATE`,
			day).Scan(&mask).Error; err != nil {
			return err
		}
		final := len(mask) > 0 && mask[0].Hours&(1<<h.Hour()) != 0
		if !final {
			hourArgs := map[string]any{"start": h, "end": h.Add(time.Hour), "day": day}
			stmt := fmt.Sprintf(usageHourSQL, "")
			if floor != nil {
				hourArgs["floor"] = *floor
				stmt = fmt.Sprintf(usageHourSQL, strings.TrimRight(usageFloorClause, " "))
			}
			if err := tx.Exec(usageClearHourSQL, hourArgs).Error; err != nil {
				return err
			}
			res := tx.Exec(stmt, hourArgs)
			if res.Error != nil {
				return res.Error
			}
			written = res.RowsAffected
			dayArgs := map[string]any{"start": d, "end": d.AddDate(0, 0, 1), "day": day, "now": now}
			if err := tx.Exec(usageClearDaySQL, dayArgs).Error; err != nil {
				return err
			}
			if err := tx.Exec(usageDaySQL, dayArgs).Error; err != nil {
				return err
			}
			if usageHourSettled(h, now) {
				if err := tx.Exec(usageLedgerSQL, map[string]any{"day": day, "bit": 1 << h.Hour(), "now": now}).Error; err != nil {
					return err
				}
			}
		}
		if advance {
			return tx.Exec(`UPDATE "event-management".tenant_usage_state SET next_hour = ?, updated_at = ? WHERE name = ?`,
				h.Add(time.Hour), now, usageStateName).Error
		}
		return nil
	})
	if err != nil {
		return written, fmt.Errorf("usage rollup for hour %s: %w", h.Format(time.RFC3339), err)
	}
	return written, nil
}

// usageLedger reads the ledger masks of the days [f, e], keyed by YYYY-MM-DD. The ledger is
// instance-wide bookkeeping that names no tenant, so it needs no tenant in context.
func (a *Api) usageLedger(ctx context.Context, f, e time.Time) (map[string]int64, error) {
	var rows []struct {
		Day   time.Time
		Hours int64
	}
	err := a.RDB.DB(ctx).Raw(`
		SELECT day, hours FROM "event-management".tenant_usage_ledger
		WHERE day >= ?::date AND day <= ?::date`, f.Format("2006-01-02"), e.Format("2006-01-02")).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("reading the usage ledger: %w", err)
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[usageDayStart(r.Day).Format("2006-01-02")] = r.Hours
	}
	return out, nil
}

// PruneUsageWorkingTables deletes the hourly rows and device sets of every day that ends at or
// before cutoff. The daily rows and the ledger are never pruned: they are the record. A day is
// pruned only long after its hours are final, so its derived daily row no longer changes.
func (a *Api) PruneUsageWorkingTables(ctx context.Context, cutoff time.Time) error {
	day := usageDayStart(cutoff) // days strictly before this one end at or before cutoff's midnight
	// System context: retention by time across every tenant's working rows; it reads nothing.
	db := a.RDB.DB(core.WithSystemContext(ctx))
	if err := db.Exec(`DELETE FROM "event-management".tenant_usage_hourly WHERE hour < ?`, day).Error; err != nil {
		return fmt.Errorf("pruning hourly usage: %w", err)
	}
	if err := db.Exec(`DELETE FROM "event-management".tenant_usage_devices WHERE day < ?::date`,
		day.Format("2006-01-02")).Error; err != nil {
		return fmt.Errorf("pruning usage device sets: %w", err)
	}
	return nil
}

// tenantUsageRow is the live shape of tenant_usage_daily, and the scan target of a usage read.
//
// With the two types below it also names this area's usage tables as TENANT-SCOPED to the
// data-access guards (backend/tools/rdbguard), which derive that set from model types carrying a
// tenant field. The migrations do not read them: they declare their own DDL.
type tenantUsageRow struct {
	TenantId       string    `gorm:"primaryKey;size:128"`
	Day            time.Time `gorm:"primaryKey;type:date"`
	EventsIngested int64
	DevicesActive  int
	DevicesPeak    int
	DeviceHours    int64
	RolledUpAt     time.Time
}

// TableName names the table tenantUsageRow is the shape of.
func (tenantUsageRow) TableName() string { return "tenant_usage_daily" }

// tenantUsageHourRow is the live shape of tenant_usage_hourly.
type tenantUsageHourRow struct {
	TenantId string    `gorm:"primaryKey;size:128"`
	Hour     time.Time `gorm:"primaryKey"`
	Events   int64
	Devices  int
}

// TableName names the table tenantUsageHourRow is the shape of.
func (tenantUsageHourRow) TableName() string { return "tenant_usage_hourly" }

// tenantUsageDeviceRow is the live shape of tenant_usage_devices.
type tenantUsageDeviceRow struct {
	TenantId    string    `gorm:"primaryKey;size:128"`
	Day         time.Time `gorm:"primaryKey;type:date"`
	DeviceToken string    `gorm:"primaryKey;size:128"`
}

// TableName names the table tenantUsageDeviceRow is the shape of.
func (tenantUsageDeviceRow) TableName() string { return "tenant_usage_devices" }

// TenantEventUsage returns one tenant's event usage for each UTC day from..to inclusive, oldest
// first, with a zero-valued entry for every day that has no record.
//
// 🔴 A DAY WITH NO RECORD READS ZERO, NOT ABSENT, and that is the contract a bill is built on: a
// tenant that sent nothing used nothing, and a consumer iterating days must not have to guess
// whether a gap means "zero" or "not measured" (Complete answers that). The read runs with the
// tenant in the context, so the tenant-scope callback injects the predicate: no raw statement and
// no system context.
func (a *Api) TenantEventUsage(ctx context.Context, tenantId string, from, to time.Time) ([]TenantEventUsageDay, error) {
	if tenantId == "" {
		return nil, fmt.Errorf("%w: tenant is required", ErrUsageRange)
	}
	if err := ValidateUsageRange(from, to); err != nil {
		return nil, err
	}
	f, e := usageDayStart(from), usageDayStart(to)
	var rows []tenantUsageRow
	err := a.RDB.DB(core.WithTenant(ctx, tenantId)).Model(&tenantUsageRow{}).
		Where("day >= ? AND day <= ?", f.Format("2006-01-02"), e.Format("2006-01-02")).
		Order("day").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("reading usage of tenant %q: %w", tenantId, err)
	}
	ledger, err := a.usageLedger(ctx, f, e)
	if err != nil {
		return nil, err
	}
	return fillUsageDays(tenantId, f, e, rows, ledger), nil
}

// fillUsageDays lays the stored rows over every day of [f, e], zero where a day has none, and marks
// complete exactly the days whose 24 hours the ledger records as final.
func fillUsageDays(tenantId string, f, e time.Time, rows []tenantUsageRow, ledger map[string]int64) []TenantEventUsageDay {
	byDay := make(map[string]tenantUsageRow, len(rows))
	for _, r := range rows {
		byDay[usageDayStart(r.Day).Format("2006-01-02")] = r
	}
	var out []TenantEventUsageDay
	for d := f; !d.After(e); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		u := TenantEventUsageDay{TenantId: tenantId, Day: d, Complete: ledger[key] == usageFullDay}
		if r, ok := byDay[key]; ok {
			u.EventsIngested, u.DevicesActive, u.DevicesPeak, u.DeviceHours = r.EventsIngested, r.DevicesActive, r.DevicesPeak, r.DeviceHours
			u.RolledUpAt = r.RolledUpAt
		}
		out = append(out, u)
	}
	return out
}
