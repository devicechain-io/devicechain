// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-user-management/iam"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewTenantAiGovernance adds the per-tenant AI-inference rate overrides to
// iam_tenants (ADR-056 §6 / ADR-023): a nullable ai_inference_requests_per_minute
// and ai_inference_burst, where NULL means "inherit the platform default" — never
// unlimited. A distinct dimension from the ai_external_enabled consent flag added
// alongside them: consent gates WHETHER a tenant's data may be routed externally,
// these gate HOW OFTEN, so an opted-in tenant still cannot loop the paid draft
// operation without bound. Additive — AutoMigrate only adds the new nullable
// columns and leaves existing rows (which inherit the default) alone. The Rollback
// drops just these columns.
func NewTenantAiGovernance() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260716120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&iam.Tenant{})
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropColumn(&iam.Tenant{}, "ai_inference_requests_per_minute"); err != nil {
				return err
			}
			return tx.Migrator().DropColumn(&iam.Tenant{}, "ai_inference_burst")
		},
	}
}
