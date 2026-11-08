// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// tenantUsageDailyDDL is this migration's own snapshot of the per-tenant daily usage record: the
// statements that create the table, written as literals. Nothing here reads the baseline, a live
// model or another migration, so the live TenantEventUsageDay struct can change without rewriting what a
// fresh install builds; a later change to the table is a new migration.
//
// What a daily row is. One tenant and one UTC calendar day: its event count (events_ingested),
// distinct devices (devices_active), the busiest hour's distinct devices (devices_peak) and the
// sum of every hour's distinct devices (device_hours, which divided by 24 is the day's average
// devices per hour). rolled_up_at is when the row was last computed. What each figure counts, and
// by which clock, is the live model's to say (model.TenantEventUsageDay), not this snapshot's.
//
// The daily row is DERIVED from two working tables, so that a pass only ever scans the hours that
// can still change instead of the whole day: tenant_usage_hourly holds one row per tenant per UTC
// hour (its events and distinct devices), and tenant_usage_devices holds each distinct device a
// tenant had on a day, which is what a day's distinct-device count cannot be summed from hours.
// Both are pruned by the rollup once their day is final; the daily row is kept.
//
// tenant_usage_ledger records which hours of each day are final, as a 24-bit mask, for all tenants
// at once. It is what makes a day "complete": a day is final only when every one of its hours is,
// so a day the rollup never measured reads as not complete rather than as a confident zero. It
// names no tenant, so it is registered with the tenant purge as exempt.
//
// Plain tables, not hypertables: they hold a row per tenant per day (or hour, or active device),
// are written by upserts on their primary keys, and the daily one must OUTLIVE the event retention
// window, since the whole point of keeping it is to say what a tenant used after the raw events are
// gone.
//
// Every statement is re-runnable (IF NOT EXISTS): this area's migrations run with
// UseTransaction:false, so a half-applied run replays from the top.
var tenantUsageDailyDDL = []string{
	`CREATE TABLE IF NOT EXISTS "event-management".tenant_usage_daily (
		tenant_id varchar(128) NOT NULL,
		day date NOT NULL,
		events_ingested bigint NOT NULL DEFAULT 0,
		devices_active integer NOT NULL DEFAULT 0,
		devices_peak integer NOT NULL DEFAULT 0,
		device_hours bigint NOT NULL DEFAULT 0,
		rolled_up_at timestamptz NOT NULL,
		PRIMARY KEY (tenant_id, day)
	)`,
	`CREATE TABLE IF NOT EXISTS "event-management".tenant_usage_hourly (
		tenant_id varchar(128) NOT NULL,
		hour timestamptz NOT NULL,
		events bigint NOT NULL DEFAULT 0,
		devices integer NOT NULL DEFAULT 0,
		PRIMARY KEY (tenant_id, hour)
	)`,
	`CREATE TABLE IF NOT EXISTS "event-management".tenant_usage_devices (
		tenant_id varchar(128) NOT NULL,
		day date NOT NULL,
		device_token varchar(128) NOT NULL,
		PRIMARY KEY (tenant_id, day, device_token)
	)`,
	`CREATE TABLE IF NOT EXISTS "event-management".tenant_usage_ledger (
		day date NOT NULL,
		hours integer NOT NULL DEFAULT 0,
		updated_at timestamptz NOT NULL,
		PRIMARY KEY (day)
	)`,
}

// NewTenantUsageDailySchema adds the per-tenant daily usage record (and its two working tables) the usage rollup writes and the
// admin plane's tenantEventUsage query reads.
//
// It is inert until the rollup runs: no seed rows, nothing written.
func NewTenantUsageDailySchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261107000000",
		Migrate: func(tx *gorm.DB) error {
			for i, stmt := range tenantUsageDailyDDL {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("tenant_usage_daily DDL statement %d: %w", i+1, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			for _, t := range []string{"tenant_usage_ledger", "tenant_usage_devices", "tenant_usage_hourly", "tenant_usage_daily"} {
				if err := tx.Exec(`DROP TABLE IF EXISTS "event-management".` + t).Error; err != nil {
					return err
				}
			}
			return nil
		},
	}
}
