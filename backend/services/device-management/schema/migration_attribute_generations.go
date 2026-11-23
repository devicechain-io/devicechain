// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// Literal migration snapshot: never substitute the live model or core mixins.
// AttrKey's 256 bound is the frozen baseline entityAttribute bound.
type attributeGenerationSnapshot struct {
	ID         uint   `gorm:"primaryKey"`
	TenantId   string `gorm:"not null;size:128;uniqueIndex:uix_attribute_generation_key,priority:1"`
	DeviceId   uint   `gorm:"type:bigint;not null;uniqueIndex:uix_attribute_generation_key,priority:2"`
	Scope      string `gorm:"not null;size:32;uniqueIndex:uix_attribute_generation_key,priority:3"`
	AttrKey    string `gorm:"not null;size:256;uniqueIndex:uix_attribute_generation_key,priority:4"`
	Generation int64  `gorm:"not null"`
	UpdatedAt  time.Time
}

func (attributeGenerationSnapshot) TableName() string { return "entity_attribute_generations" }

// NewAttributeGenerationsSchema expands the schema without a device foreign key.
// Each statement is replayable independently after a non-transactional failure.
func NewAttributeGenerationsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{ID: "20261220130000", Migrate: func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec(`CREATE SEQUENCE IF NOT EXISTS "device-management".entity_attribute_generation_seq`).Error; err != nil {
				return err
			}
		}
		return tx.AutoMigrate(&attributeGenerationSnapshot{})
	}, Rollback: func(*gorm.DB) error {
		return errors.New("attribute generations cannot be rolled back during rolling upgrades")
	}}
}
