// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewMetricExpressionSchema adds metric_definitions.expression: a calculated metric's
// formula (ADR-082), or NULL for a measured metric.
//
// Nullable with no default, for the reason migration_device_disabled.go gives (a constant
// default is stored in the catalog rather than the rows and is lost by a logical dump and
// restore). NULL is "measured", the true state of every definition that predates the
// column, so there is no backfill. TEXT, unbounded in the column: the 1 KiB bound is
// enforced at write, where the refusal can say why. One statement, IF NOT EXISTS, hence
// re-runnable; it names the table literally rather than through the live model, so a
// later change to MetricDefinition cannot rewrite what this migration does.
func NewMetricExpressionSchema() *gormigrate.Migration {
	const table = `"device-management"."metric_definitions"`
	return &gormigrate.Migration{
		ID: "20261016200000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + table + ` ADD COLUMN IF NOT EXISTS expression text;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + table + ` DROP COLUMN IF EXISTS expression;`).Error
		},
	}
}
