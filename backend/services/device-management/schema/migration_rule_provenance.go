// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// These are this migration's own column snapshots, never live model types.
// Literal table names follow the area's search_path, as enforcementProfileVersion does.
type ruleProvenanceDraft struct {
	AuthoredVia datatypes.JSON
}

func (ruleProvenanceDraft) TableName() string { return "detection_rules" }

type ruleProvenanceVersion struct {
	PublishedById *string `gorm:"type:text"`
	AuthoredVia   datatypes.JSON
}

func (ruleProvenanceVersion) TableName() string { return "device_profile_versions" }

// NewRuleProvenanceSchema only expands existing tables. All columns are nullable,
// without defaults or backfills: old rows and old-pod writes remain truthful UNKNOWNs.
// Migrations run without a transaction, so each column is checked independently on replay.
func NewRuleProvenanceSchema() *gormigrate.Migration {
	return &gormigrate.Migration{ID: "20261226120000", Migrate: func(tx *gorm.DB) error {
		for _, column := range []struct {
			model any
			name  string
		}{
			{&ruleProvenanceDraft{}, "AuthoredVia"},
			{&ruleProvenanceVersion{}, "PublishedById"},
			{&ruleProvenanceVersion{}, "AuthoredVia"},
		} {
			if !tx.Migrator().HasColumn(column.model, column.name) {
				if err := tx.Migrator().AddColumn(column.model, column.name); err != nil {
					return err
				}
			}
		}
		return nil
	}, Rollback: func(*gorm.DB) error {
		return errors.New("rule provenance cannot be rolled back during rolling upgrades")
	}}
}
