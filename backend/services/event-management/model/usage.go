// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// MaxUsageRangeDays bounds one tenantUsage read. A daily record is a few dozen bytes, so the bound
// is about keeping one request's answer (and its zero-filled days) a sensible size, not the store.
const MaxUsageRangeDays = 366

// ErrUsageRange is returned for a usage range that is empty, inverted or longer than
// MaxUsageRangeDays.
var ErrUsageRange = errors.New("invalid usage range")

// TenantUsageDay is one tenant's usage for one UTC calendar day.
//
// 🔴 WHAT EACH NUMBER MEANS, because a billing figure that is "roughly" something is not one.
//   - EventsIngested counts the events the event store holds for the tenant whose event time falls
//     in the day (one per event, not per reading, and a redelivered duplicate is stored once so is
//     counted once). It is the event's own time, the same clock every query on the event store
//     reads, so a device backfilling old readings credits the days those readings are dated.
//   - DevicesActive is the number of distinct devices that produced at least one event that day.
//   - DevicesPeak is the largest number of distinct devices active in any single UTC hour.
//   - DevicesAvg is the mean of the 24 hourly distinct-device counts; an hour with no events counts
//     as zero, and so does an hour not yet computed (Complete says whether all of them were).
//
// Devices that are registered but silent are NOT counted by any of these: this record measures what
// a tenant used, from the event store, and the registry of devices lives in another area.
type TenantUsageDay struct {
	TenantId       string
	Day            time.Time // midnight UTC
	EventsIngested int64
	DevicesActive  int
	DevicesPeak    int
	DeviceHours    int64
	// Complete is true once every hour of the day has been computed after it closed, whether or
	// not the tenant had events. A day the rollup has not (or never) fully computed is not
	// complete, so a zero on it means "not measured yet", never "measured nothing".
	Complete bool
	// RolledUpAt is when the row was last computed; zero for a day that has no row.
	RolledUpAt time.Time
}

// DevicesAvg is the mean number of devices active per UTC hour over the 24 hours of the day.
func (d TenantUsageDay) DevicesAvg() float64 {
	return float64(d.DeviceHours) / 24.0
}

// usageDayStart truncates t to midnight UTC.
func usageDayStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
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

// usageHourSQL computes one UTC hour for EVERY tenant and upserts it into tenant_usage_hourly.
//
// 🔴 CROSS-TENANT BY CONSTRUCTION AND TENANT-SAFE BY GROUPING: the rollup runs under the system
// context because it must see every tenant's events, and every statement groups or keys by
// tenant_id so no count can mix two tenants. The tenant boundary is the GROUP BY, which is why the
// two-tenant integration test reuses a device token across tenants and asserts each is untouched.
//
// The range is half open on occurred_time, the hypertable's partition column, so the scan reads
// only the hour's chunk. The hour is passed in as an instant, never cut with date_trunc, which on a
// timestamptz uses the session time zone and would shift every bucket on a server not set to UTC.
const usageHourSQL = `
INSERT INTO "event-management".tenant_usage_hourly (tenant_id, hour, events, devices)
SELECT tenant_id, @start, count(*), count(DISTINCT device_token)
FROM "event-management".events
WHERE occurred_time >= @start AND occurred_time < @end
GROUP BY tenant_id
ON CONFLICT (tenant_id, hour) DO UPDATE SET
	events = EXCLUDED.events,
	devices = EXCLUDED.devices`

// usageDevicesSQL adds the hour's distinct devices to their tenant's device set for the day. A day's
// distinct-device count is the size of that set: it cannot be summed from the hours, since one
// device active in two hours is one device.
const usageDevicesSQL = `
INSERT INTO "event-management".tenant_usage_devices (tenant_id, day, device_token)
SELECT DISTINCT tenant_id, CAST(@day AS date), device_token
FROM "event-management".events
WHERE occurred_time >= @start AND occurred_time < @end
ON CONFLICT DO NOTHING`

// usageDaySQL derives the daily row of every tenant that has an hour that day from the working
// tables. Rows are only ever rewritten with the numbers the tables now hold, so running it twice
// leaves a day exactly as running it once. A tenant with no events that day has no hourly row and
// so no daily row; the read side reports it as zero.
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
ON CONFLICT (tenant_id, day) DO UPDATE SET
	events_ingested = EXCLUDED.events_ingested,
	devices_active = EXCLUDED.devices_active,
	devices_peak = EXCLUDED.devices_peak,
	device_hours = EXCLUDED.device_hours,
	rolled_up_at = EXCLUDED.rolled_up_at`

// usageLedgerSQL marks one hour of a day computed. The mask only ever gains bits.
const usageLedgerSQL = `
INSERT INTO "event-management".tenant_usage_ledger (day, hours, updated_at)
VALUES (CAST(@day AS date), @bit, @now)
ON CONFLICT (day) DO UPDATE SET
	hours = "event-management".tenant_usage_ledger.hours | EXCLUDED.hours,
	updated_at = EXCLUDED.updated_at`

// usageFullDay is the ledger mask of a day whose 24 hours have all been computed.
const usageFullDay = 1<<24 - 1

// usageHourStart truncates t to the start of its UTC hour.
func usageHourStart(t time.Time) time.Time {
	return t.UTC().Truncate(time.Hour)
}

// RollupUsageHours recomputes every UTC hour from the hour containing from to the hour containing
// to, both inclusive, for every tenant, re-deriving the daily row of the hour's day each time. now
// stamps the rows, so a test can pin it. It returns the number of (tenant, hour) rows written.
//
// Each hour is ONE transaction: its hourly rows, its device set, its day's derived rows and its
// ledger bit commit together, so the ledger never says an hour was computed when its figures were
// not, and a day's row is never older than the hours behind it. A failure leaves every earlier
// hour written; since every write is an idempotent recompute, the next pass redoes the rest.
//
// An hour is marked in the ledger only if it had closed by now: an hour computed while still open
// can still change, and a day is complete only when all 24 of its hours were computed closed.
func (a *Api) RollupUsageHours(ctx context.Context, from, to, now time.Time) (int64, error) {
	db := a.RDB.DB(core.WithSystemContext(ctx))
	now = now.UTC()
	var written int64
	for h := usageHourStart(from); !h.After(usageHourStart(to)); h = h.Add(time.Hour) {
		d := usageDayStart(h)
		hourArgs := map[string]any{"start": h, "end": h.Add(time.Hour), "day": d.Format("2006-01-02")}
		dayArgs := map[string]any{"start": d, "end": d.AddDate(0, 0, 1), "day": d.Format("2006-01-02"), "now": now}
		err := db.Transaction(func(tx *gorm.DB) error {
			res := tx.Exec(usageHourSQL, hourArgs)
			if res.Error != nil {
				return res.Error
			}
			if err := tx.Exec(usageDevicesSQL, hourArgs).Error; err != nil {
				return err
			}
			if err := tx.Exec(usageDaySQL, dayArgs).Error; err != nil {
				return err
			}
			if h.Add(time.Hour).After(now) {
				written += res.RowsAffected
				return nil
			}
			err := tx.Exec(usageLedgerSQL, map[string]any{"day": d.Format("2006-01-02"),
				"bit": 1 << h.Hour(), "now": now}).Error
			if err == nil {
				written += res.RowsAffected
			}
			return err
		})
		if err != nil {
			return written, fmt.Errorf("usage rollup for hour %s: %w", h.Format(time.RFC3339), err)
		}
	}
	return written, nil
}

// usageLedger reads the ledger masks of the days [f, e], keyed by YYYY-MM-DD.
func (a *Api) usageLedger(ctx context.Context, f, e time.Time) (map[string]int64, error) {
	var rows []struct {
		Day   time.Time
		Hours int64
	}
	err := a.RDB.DB(core.WithSystemContext(ctx)).Raw(`
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

// FirstUsageHourOwed returns the start of the earliest closed UTC hour in [from, before) that the
// ledger does not record as computed, or ok=false when every one of them is.
func (a *Api) FirstUsageHourOwed(ctx context.Context, from, before time.Time) (time.Time, bool, error) {
	f, b := usageHourStart(from), usageHourStart(before)
	if !f.Before(b) {
		return time.Time{}, false, nil
	}
	ledger, err := a.usageLedger(ctx, usageDayStart(f), usageDayStart(b.Add(-time.Hour)))
	if err != nil {
		return time.Time{}, false, err
	}
	h, ok := firstHourOwed(ledger, f, b)
	return h, ok, nil
}

// firstHourOwed scans [f, b) hour by hour for one whose ledger bit is clear.
func firstHourOwed(ledger map[string]int64, f, b time.Time) (time.Time, bool) {
	for h := f; h.Before(b); h = h.Add(time.Hour) {
		if ledger[h.Format("2006-01-02")]&(1<<h.Hour()) == 0 {
			return h, true
		}
	}
	return time.Time{}, false
}

// RollupUsageDay recomputes all 24 hours of the UTC day containing day and its daily row.
func (a *Api) RollupUsageDay(ctx context.Context, day, now time.Time) (int64, error) {
	start := usageDayStart(day)
	return a.RollupUsageHours(ctx, start, start.Add(23*time.Hour), now)
}

// PruneUsageWorkingTables deletes the hourly rows and device sets of every day that ends at or
// before cutoff. The daily rows are never pruned: they are the record. A day is pruned only once
// the rollup no longer rewrites it, so the derived daily row is final by then.
func (a *Api) PruneUsageWorkingTables(ctx context.Context, cutoff time.Time) error {
	day := usageDayStart(cutoff) // days strictly before this one end at or before cutoff's midnight
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

// tenantUsageRow is the scan target of a usage read.
//
// It is also the live shape of tenant_usage_daily, and with the two types below it names this
// area's usage tables as TENANT-SCOPED to the data-access guards (backend/tools/rdbguard), which
// derive that set from model types carrying a tenant field. The migration does not read them: it
// declares its own DDL.
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

// TenantUsage returns one tenant's usage for each UTC day from..to inclusive, oldest first, with a
// zero-valued entry for every day that has no record.
//
// 🔴 A DAY WITH NO RECORD READS ZERO, NOT ABSENT, and that is the contract a bill is built on: a
// tenant that sent nothing used nothing, and a consumer iterating days must not have to guess
// whether a gap means "zero" or "not measured". The read is for exactly one tenant, named by the
// caller under the system context, so the tenant predicate is explicit and is the only thing that
// scopes it.
func (a *Api) TenantUsage(ctx context.Context, tenantId string, from, to time.Time) ([]TenantUsageDay, error) {
	if tenantId == "" {
		return nil, fmt.Errorf("%w: tenant is required", ErrUsageRange)
	}
	if err := ValidateUsageRange(from, to); err != nil {
		return nil, err
	}
	f, e := usageDayStart(from), usageDayStart(to)
	var rows []tenantUsageRow
	err := a.RDB.DB(core.WithSystemContext(ctx)).Raw(`
		SELECT day, events_ingested, devices_active, devices_peak, device_hours, rolled_up_at
		FROM "event-management".tenant_usage_daily
		WHERE tenant_id = ? AND day >= ?::date AND day <= ?::date
		ORDER BY day`, tenantId, f.Format("2006-01-02"), e.Format("2006-01-02")).Scan(&rows).Error
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
// complete exactly the days whose 24 hours the ledger records as computed.
func fillUsageDays(tenantId string, f, e time.Time, rows []tenantUsageRow, ledger map[string]int64) []TenantUsageDay {
	byDay := make(map[string]tenantUsageRow, len(rows))
	for _, r := range rows {
		byDay[usageDayStart(r.Day).Format("2006-01-02")] = r
	}
	var out []TenantUsageDay
	for d := f; !d.After(e); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		u := TenantUsageDay{TenantId: tenantId, Day: d, Complete: ledger[key] == usageFullDay}
		if r, ok := byDay[key]; ok {
			u.EventsIngested, u.DevicesActive, u.DevicesPeak, u.DeviceHours = r.EventsIngested, r.DevicesActive, r.DevicesPeak, r.DeviceHours
			u.RolledUpAt = r.RolledUpAt
		}
		out = append(out, u)
	}
	return out
}
