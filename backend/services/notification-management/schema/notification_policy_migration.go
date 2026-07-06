// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// NewNotificationPolicySchema adds the notification_policies and
// notification_rules tables (ADR-017): a policy is the routing header, its rules
// are the per-severity (severity → channel + recipients) mappings it owns. The
// two are created together because a rule has no meaning apart from its policy.
// Snapshots are inline (frozen) and must match model.NotificationPolicy /
// model.NotificationRule.
func NewNotificationPolicySchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260705130000",
		Migrate: func(tx *gorm.DB) error {
			type NotificationPolicy struct {
				gorm.Model
				rdb.TenantScoped
				rdb.TokenReference
				rdb.NamedEntity
				rdb.MetadataEntity

				DeviceTypeToken sql.NullString `gorm:"size:128;index"`
				ThrottleSeconds sql.NullInt64
				Enabled         bool `gorm:"not null;default:true"`

				EscalateAfterSeconds sql.NullInt64
				MaxEscalations       sql.NullInt64
			}
			type NotificationRule struct {
				gorm.Model
				rdb.TenantScoped

				PolicyId   uint   `gorm:"not null;index"`
				Severity   string `gorm:"not null;size:16"`
				ChannelId  uint   `gorm:"not null;index"`
				Recipients *datatypes.JSON
			}
			if err := tx.AutoMigrate(&NotificationPolicy{}, &NotificationRule{}); err != nil {
				return err
			}
			// ADR-042 P1: per-tenant partial unique index on the policy token.
			return rdb.CreateTenantTokenIndex(tx, &NotificationPolicy{})
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropTable("notification_rules"); err != nil {
				return err
			}
			return tx.Migrator().DropTable("notification_policies")
		},
	}
}
