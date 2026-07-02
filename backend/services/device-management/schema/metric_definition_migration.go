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

// NewMetricDefinitionSchema adds the metric_definitions table (ADR-016): typed,
// unit-bearing metrics declared on a device profile. It is a post-initial
// migration (the model was developed on a branch that never merged and is being
// brought onto main), so it is added here rather than folded into the frozen
// initial schema. The snapshot is defined inline so the migration is frozen
// against future model changes; it must produce the same columns as
// model.MetricDefinition.
func NewMetricDefinitionSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260625130000",
		Migrate: func(tx *gorm.DB) error {
			// The (device_type_id, metric_key) lookup index is unique so a profile
			// cannot declare the same key twice; tenant isolation is enforced by the
			// app-layer tenant scope on the embedded TenantScoped.
			type MetricDefinition struct {
				gorm.Model
				rdb.TenantScoped
				rdb.TokenReference
				rdb.NamedEntity
				rdb.MetadataEntity

				DeviceTypeId uint   `gorm:"not null;index:idx_metric_definition_key,unique,priority:1"`
				MetricKey    string `gorm:"not null;size:128;index:idx_metric_definition_key,unique,priority:2"`
				DataType     string `gorm:"not null;size:16"`
				Unit         sql.NullString
				MinValue     sql.NullFloat64
				MaxValue     sql.NullFloat64
				Enum         *datatypes.JSON
				Descriptor   sql.NullString
			}
			if err := tx.AutoMigrate(&MetricDefinition{}); err != nil {
				return err
			}
			// ADR-042 P1: per-tenant partial unique index on token.
			return rdb.CreateTenantTokenIndex(tx, &MetricDefinition{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("metric_definitions")
		},
	}
}
