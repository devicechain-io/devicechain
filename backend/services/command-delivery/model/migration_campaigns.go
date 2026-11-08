// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// Like the migrations beside it, this file imports none of the service's own packages. A
// migration that can reach a live model is a migration whose behaviour changes when that
// model does.
import (
	"database/sql"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// campaign, campaignCycle and campaignElement are the SNAPSHOTS of the three tables
// NewCampaignsSchema creates, as they were written. They are not the live models.
//
// 🔴 NONE HAS A TableName METHOD, for the reason migration_command_batches.go gives: an
// explicit TableName bypasses the area TablePrefix. Gorm derives "campaigns",
// "campaign_cycles" and "campaign_elements" from the type names, so renaming a type
// retargets the migration.
type campaign struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`
	Token    string `gorm:"index;not null;size:128"`

	Name    string `gorm:"size:128"`
	Enabled bool   `gorm:"not null"`

	Trigger datatypes.JSON
	Steps   datatypes.JSON

	TargetKind string `gorm:"not null;size:16"`
	GroupToken string `gorm:"not null;size:128"`

	MaxLatenessSeconds int `gorm:"not null"`
	ExpiresInSeconds   int `gorm:"not null"`

	PlannedThrough sql.NullTime
	LastError      sql.NullString `gorm:"size:512"`
	EnabledAt      sql.NullTime
}

type campaignCycle struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time

	TenantId string `gorm:"index;not null;size:128"`

	CampaignId uint   `gorm:"not null"`
	CycleKey   string `gorm:"not null;size:64"`
	Day        sql.NullString
	At         sql.NullTime

	GroupVersion        sql.NullInt32
	Resolved            int `gorm:"not null"`
	MembershipCheckedAt sql.NullTime
}

type campaignElement struct {
	ID        uint `gorm:"primarykey"`
	UpdatedAt time.Time

	TenantId string `gorm:"index;not null;size:128"`

	CampaignId  uint      `gorm:"not null"`
	CycleId     uint      `gorm:"not null"`
	Step        int       `gorm:"not null"`
	DeviceToken string    `gorm:"not null;size:128"`
	DueAt       time.Time `gorm:"not null"`

	Status string `gorm:"not null;size:16"`
	Reason sql.NullString

	BatchToken      sql.NullString `gorm:"size:128"`
	RetryBatchToken sql.NullString `gorm:"size:128"`

	ClaimedBy sql.NullString `gorm:"size:64"`
	ClaimedAt sql.NullTime
}

// NewCampaignsSchema adds the scheduler's three tables: the campaign definition, its cycles,
// and one element per device per step per cycle.
//
// The indexes are literal DDL, named and written out, because an index name and its predicate
// are schema:
//
//   - uix_campaigns_tenant_token: a campaign's token is unique among live rows in a tenant.
//   - uix_campaign_cycles_key is what makes planning idempotent across replicas: two replicas
//     that both plan a cycle collide here and exactly one wins.
//   - uix_campaign_elements_device is the same guard one level down.
//   - idx_campaign_elements_due serves a tenant's pass finding which campaigns have work due: a
//     range over due_at of the tenant's PENDING rows only. Partial on status, so it stays as
//     small as what is still to do.
//   - idx_campaign_elements_due_campaign serves one campaign's claim, coalescing and membership
//     re-check, which read that campaign's PENDING rows in due order. Partial for the same reason.
//   - idx_campaign_elements_claimed serves the recovery of a dead replica's claims.
//   - idx_campaign_elements_batch serves tracking from a batch token.
//   - idx_campaign_elements_cycle serves paging a cycle's elements, and the pruner deleting a
//     retired cycle's finished elements.
//   - idx_campaign_cycles_created serves the pruner finding the cycles past retention.
//
// Every statement is individually re-runnable (IF NOT EXISTS): migrations run with
// UseTransaction:false, so a half-applied migration replays from the top on the next boot.
func NewCampaignsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261116120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&campaign{}, &campaignCycle{}, &campaignElement{}); err != nil {
				return err
			}
			for _, ddl := range []string{
				`CREATE UNIQUE INDEX IF NOT EXISTS uix_campaigns_tenant_token ` +
					`ON "command-delivery".campaigns (tenant_id, token) WHERE deleted_at IS NULL;`,
				`CREATE UNIQUE INDEX IF NOT EXISTS uix_campaign_cycles_key ` +
					`ON "command-delivery".campaign_cycles (tenant_id, campaign_id, cycle_key);`,
				`CREATE UNIQUE INDEX IF NOT EXISTS uix_campaign_elements_device ` +
					`ON "command-delivery".campaign_elements (tenant_id, cycle_id, step, device_token);`,
				`CREATE INDEX IF NOT EXISTS idx_campaign_elements_due ` +
					`ON "command-delivery".campaign_elements (tenant_id, due_at) WHERE status = 'PENDING';`,
				`CREATE INDEX IF NOT EXISTS idx_campaign_elements_due_campaign ` +
					`ON "command-delivery".campaign_elements (tenant_id, campaign_id, due_at) WHERE status = 'PENDING';`,
				`CREATE INDEX IF NOT EXISTS idx_campaign_elements_claimed ` +
					`ON "command-delivery".campaign_elements (tenant_id, claimed_at) WHERE status = 'CLAIMED';`,
				`CREATE INDEX IF NOT EXISTS idx_campaign_elements_batch ` +
					`ON "command-delivery".campaign_elements (tenant_id, batch_token) WHERE batch_token IS NOT NULL;`,
				`CREATE INDEX IF NOT EXISTS idx_campaign_elements_cycle ` +
					`ON "command-delivery".campaign_elements (tenant_id, cycle_id, id);`,
				`CREATE INDEX IF NOT EXISTS idx_campaign_cycles_created ` +
					`ON "command-delivery".campaign_cycles (tenant_id, created_at);`,
			} {
				if err := tx.Exec(ddl).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			for _, table := range []string{"campaign_elements", "campaign_cycles", "campaigns"} {
				if err := tx.Migrator().DropTable(table); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
