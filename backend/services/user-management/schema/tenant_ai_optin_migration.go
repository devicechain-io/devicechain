// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-user-management/iam"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewTenantAiOptIn adds the per-tenant external-AI consent column to iam_tenants
// (ADR-056 §6): a nullable ai_external_enabled, where NULL (or false) means "not
// opted in" — fail-closed, never "allowed". A distinct governance dimension from
// the ingest/outbound rate overrides: it is a boolean consent gate, not a limit.
// Additive — AutoMigrate only adds the new nullable column and leaves existing
// rows (which are not opted in) alone. The Rollback drops just this column.
func NewTenantAiOptIn() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260715130000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&iam.Tenant{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropColumn(&iam.Tenant{}, "ai_external_enabled")
		},
	}
}
