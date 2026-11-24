// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// analyticsGenerationSnapshot is this migration's literal singleton/seed shape.
// It deliberately does not reference a live model or the build's generation.
type analyticsGenerationSnapshot struct {
	ID         int32
	Generation int32
}

// NewAnalyticsGenerationSchema appends metadata only; old pods ignore the table.
// MigrationOptions uses UseTransaction:false, so both statements must replay
// independently. The seed never overwrites an already reconciled generation.
func NewAnalyticsGenerationSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261124000000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`CREATE TABLE IF NOT EXISTS "event-management".analytics_surface_generation (
				id integer NOT NULL PRIMARY KEY CHECK (id = 1),
				generation integer NOT NULL CHECK (generation >= 0)
			)`).Error; err != nil {
				return fmt.Errorf("creating the analytics surface generation table: %w", err)
			}
			seed := analyticsGenerationSnapshot{ID: 1, Generation: 0}
			if err := tx.Exec(`INSERT INTO "event-management".analytics_surface_generation (id, generation)
				VALUES (?, ?) ON CONFLICT (id) DO NOTHING`, seed.ID, seed.Generation).Error; err != nil {
				return fmt.Errorf("seeding the unversioned analytics surface generation: %w", err)
			}
			return nil
		},
	}
}
