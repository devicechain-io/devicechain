// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"errors"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Literal snapshots owned by this migration, including inlined tenant fields.
// There is intentionally no FK to devices: previous-release deletes must work.
type deviceConfigurationReport struct {
	ID        uint      `gorm:"primarykey"`
	CreatedAt time.Time `gorm:"not null;index:idx_configuration_reports_tenant_created,priority:2"`
	TenantId  string    `gorm:"not null;size:128;index:idx_configuration_reports_tenant_created,priority:1"`
	DeviceId  uint      `gorm:"type:bigint;not null;uniqueIndex:uix_configuration_reports_device_revision_status,priority:1"`
	Revision  int64     `gorm:"not null;uniqueIndex:uix_configuration_reports_device_revision_status,priority:2"`
	Digest    string    `gorm:"not null;size:71"`
	Status    string    `gorm:"not null;size:16;uniqueIndex:uix_configuration_reports_device_revision_status,priority:3"`
	Errors    datatypes.JSON
	Source    string `gorm:"not null;size:16"`
}

// NewDeviceConfigurationReportsSchema expands storage without changing old-pod
// writes. No seed or backfill is performed by DDL. All new state columns are
// nullable with no default. AutoMigrate and the catalog-guarded CHECK tolerate
// replay from the top after partial DDL (UseTransaction:false).
func NewDeviceConfigurationReportsSchema() *gormigrate.Migration {
	// A local snapshot retains the existing table name without reusing any live
	// model or another migration's snapshot.
	type deviceConfigurationState struct {
		ID                   uint `gorm:"primarykey"`
		CreatedAt            time.Time
		UpdatedAt            time.Time
		TenantId             string `gorm:"index;not null;size:128"`
		DeviceId             uint   `gorm:"not null;uniqueIndex:uix_dcs_device"`
		ReportedRevision     sql.NullInt64
		ReportedDigest       sql.NullString `gorm:"size:71"`
		ReportedStatus       sql.NullString `gorm:"size:16"`
		ReportedErrors       datatypes.JSON
		ReportedAt           sql.NullTime
		LastSyncAt           sql.NullTime
		CapturedRevision     sql.NullInt64
		CapturedStatus       sql.NullString `gorm:"size:16"`
		CaptureGapThrough    sql.NullInt64
		CaptureCompleteSince sql.NullTime
		CapturedReportedAt   sql.NullTime
	}
	return &gormigrate.Migration{
		ID: "20261221120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&deviceConfigurationReport{}); err != nil {
				return err
			}
			// The state table exists: add only the new nullable columns, each re-runnable, and never
			// let AutoMigrate reconcile the existing columns against a snapshot.
			if tx.Dialector.Name() == "postgres" {
				if err := tx.Exec(`ALTER TABLE "device-management"."device_configuration_states"` +
					` ADD COLUMN IF NOT EXISTS captured_revision bigint,` +
					` ADD COLUMN IF NOT EXISTS captured_status varchar(16),` +
					` ADD COLUMN IF NOT EXISTS capture_gap_through bigint,` +
					` ADD COLUMN IF NOT EXISTS capture_complete_since timestamptz,` +
					` ADD COLUMN IF NOT EXISTS captured_reported_at timestamptz;`).Error; err != nil {
					return err
				}
			} else if err := tx.AutoMigrate(&deviceConfigurationState{}); err != nil {
				return err
			}
			return addConfigurationReportStatusCheck(tx)
		},
		Rollback: func(*gorm.DB) error {
			return errors.New("the configuration report history cannot be rolled back: dropping it would discard accepted device verdicts")
		},
	}
}

func addConfigurationReportStatusCheck(tx *gorm.DB) error {
	if tx.Dialector.Name() == "sqlite" {
		// SQLite rebuilds the stored CREATE statement to add a constraint.
		// This literal names the constraint; the migrator preserves all columns.
		type deviceConfigurationReport struct {
			Status string `gorm:"check:chk_configuration_reports_status,status IN ('APPLIED','REJECTED')"`
		}
		if tx.Migrator().HasConstraint(&deviceConfigurationReport{}, "chk_configuration_reports_status") {
			return nil
		}
		return tx.Migrator().CreateConstraint(&deviceConfigurationReport{}, "chk_configuration_reports_status")
	}
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	var count int64
	if err := tx.Raw(`SELECT count(*) FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = ? AND t.relname = ? AND c.conname = ?`,
		"device-management", "device_configuration_reports", "chk_configuration_reports_status").Scan(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	return tx.Exec(`ALTER TABLE "device-management".device_configuration_reports ADD CONSTRAINT chk_configuration_reports_status CHECK (status IN ('APPLIED','REJECTED'))`).Error
}
