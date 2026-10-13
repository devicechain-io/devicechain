// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// Like the migrations beside it, this file imports none of the service's own packages. A
// migration that can reach a live model is a migration whose behaviour changes when that
// model does.
import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// deviceGate is the table NewDeviceGatesSchema creates, as it was written: command-delivery's
// record of which devices are disabled (see the live DeviceGate for why it exists).
//
// 🔴 IT HAS NO TableName METHOD, for the reason baseline_snapshot.go gives: an explicit
// TableName bypasses the area TablePrefix and the table would resolve through search_path.
// Gorm derives "device_gates" from the type name, so renaming the type retargets the migration.
type deviceGate struct {
	ID uint `gorm:"primarykey"`

	TenantId    string `gorm:"not null;size:128"`
	DeviceToken string `gorm:"not null;size:128"`

	Disabled  bool `gorm:"not null"`
	UpdatedAt time.Time
}

// deviceGateSync is the table recording that a tenant's record was synced once, as written.
// No TableName method, for the reason deviceGate gives.
type deviceGateSync struct {
	ID uint `gorm:"primarykey"`

	TenantId string `gorm:"not null;size:128"`

	SyncedAt time.Time
}

// NewDeviceGatesSchema adds the table commands to a disabled device are held against.
//
// The indexes are literal DDL, named and written out, because an index name and its predicate
// are schema and a gorm tag cannot express the partial one:
//
//   - uix_device_gates_tenant_device is the upsert's conflict target and the per-device lookup
//     the dispatch gate makes; tenant_id leads, as it does on every unique index in this area.
//   - uix_device_gate_syncs_tenant keeps one "synced once" row per tenant.
//   - idx_device_gates_disabled is partial on disabled, so the sub-select every claim carries
//     (`(tenant_id, device_token) NOT IN (disabled rows)`) reads only the devices that are
//     disabled right now, however many have ever toggled.
//
// Every statement is individually re-runnable (IF NOT EXISTS), which it has to be: migrations
// run with UseTransaction:false, so a half-applied migration is never rolled back and replays
// from the top on the next boot.
func NewDeviceGatesSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261013120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&deviceGate{}, &deviceGateSync{}); err != nil {
				return err
			}
			for _, ddl := range []string{
				`CREATE UNIQUE INDEX IF NOT EXISTS uix_device_gates_tenant_device ` +
					`ON "command-delivery".device_gates (tenant_id, device_token);`,
				`CREATE UNIQUE INDEX IF NOT EXISTS uix_device_gate_syncs_tenant ` +
					`ON "command-delivery".device_gate_syncs (tenant_id);`,
				`CREATE INDEX IF NOT EXISTS idx_device_gates_disabled ` +
					`ON "command-delivery".device_gates (tenant_id, device_token) WHERE disabled;`,
			} {
				if err := tx.Exec(ddl).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropTable("device_gate_syncs"); err != nil {
				return err
			}
			return tx.Migrator().DropTable("device_gates")
		},
	}
}
