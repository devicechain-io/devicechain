// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// profileDecoderHealth is the SNAPSHOT of one replica's decode tally for one profile version and
// one minute, as of this migration. It is not model.ProfileDecoderHealth and must never be
// replaced by it: a migration's shapes are a point in time, and pointing one at a live model
// silently rewrites an already-applied migration whenever that model changes, which breaks FRESH
// installs while every existing database applies cleanly and looks healthy.
//
// The mixin is INLINED, as in every other shape in this package. The values are ABSOLUTE for
// (replica, window): the flusher writes the replica's cumulative tally with SET, never an
// increment, so a flush whose commit succeeded but whose response was lost re-applies to the
// same value.
//
// 🔴 NO TableName(), for the reason deviceTrustAnchor gives.
type profileDecoderHealth struct {
	ID       uint   `gorm:"primaryKey"`
	TenantId string `gorm:"index;not null;size:128;uniqueIndex:uix_profile_decoder_healths_window,priority:1"`

	DeviceProfileId uint      `gorm:"not null;uniqueIndex:uix_profile_decoder_healths_window,priority:2"`
	ProfileVersion  int32     `gorm:"not null;uniqueIndex:uix_profile_decoder_healths_window,priority:3"`
	WindowStart     time.Time `gorm:"not null;uniqueIndex:uix_profile_decoder_healths_window,priority:4"`
	Replica         string    `gorm:"not null;size:128;uniqueIndex:uix_profile_decoder_healths_window,priority:5"`

	Decoded    int64 `gorm:"not null;default:0"`
	Ignored    int64 `gorm:"not null;default:0"`
	Failed     int64 `gorm:"not null;default:0"`
	Suppressed int64 `gorm:"not null;default:0"`
	ChargeSkew int64 `gorm:"not null;default:0"`

	LastFailureAt *time.Time
	LastError     string `gorm:"size:512"`
}

// profileDecoderSample is the SNAPSHOT of one stored failing uplink, kept so the test panel can
// replay it. Input is the canonical JSON of an uplink in the shape a decoder
// vector uses; with a 64 KiB json, a 64 KiB decoded object and a hex frame it can reach about 140
// KiB or more. The platform serves it only to DeviceWrite callers of the owning tenant.
type profileDecoderSample struct {
	ID              uint      `gorm:"primaryKey"`
	TenantId        string    `gorm:"index;not null;size:128"`
	DeviceProfileId uint      `gorm:"not null"`
	ProfileVersion  int32     `gorm:"not null"`
	CreatedAt       time.Time `gorm:"not null"`
	DeviceToken     string    `gorm:"not null;size:128"`
	Reason          string    `gorm:"not null;size:32"`
	Error           string    `gorm:"size:512"`
	Input           []byte    `gorm:"type:bytea;not null"`
}

// NewProfileDecodersSchema is the schema half of tenant-authored payload decoders.
//
// # What it changes
//
//  1. device_profiles.decoder_declaration: the profile's DRAFT decoder document, one nullable
//     JSONB value (NULL = no decoder). Publish freezes it into the version snapshot, which is JSON
//     inside an existing column, so published content needs no schema change.
//  2. profile_decoder_healths: per-replica, per-minute decode tallies of a profile version.
//  3. profile_decoder_samples: recent failing uplinks of a profile, for replay in the test panel.
//
// # Nullable, no default, no backfill
//
// The column carries no DEFAULT: `ADD COLUMN ... DEFAULT <const>` lives in PostgreSQL's
// attmissingval catalog state and is silently lost by a logical dump/restore (see
// migration_profile_location_declaration.go). NULL is the real answer for a profile that
// declares no decoder, so there is no backfill either.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure. The column is
// ADD COLUMN IF NOT EXISTS; AutoMigrate creates a table only when it is absent and adds only
// missing columns and indexes; the sample index is IF NOT EXISTS; each foreign key is added only
// when the catalog does not hold it yet, then validated.
//
// # DDL only
//
// No row is seeded or written, so the DDL-only bar the baselines are held to holds here too.
//
// # Tenant purge
//
// Both tables carry tenant_id, so the catalog-driven tenant purge classifies them directly with no
// exemption to register, and their foreign keys to device_profiles cascade.
func NewProfileDecodersSchema() *gormigrate.Migration {
	const (
		profiles = `"device-management"."device_profiles"`
		samples  = `"device-management"."profile_decoder_samples"`
	)
	return &gormigrate.Migration{
		ID: "20261110120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`ALTER TABLE ` + profiles +
				` ADD COLUMN IF NOT EXISTS decoder_declaration jsonb;`).Error; err != nil {
				return err
			}
			if err := tx.AutoMigrate(&profileDecoderHealth{}, &profileDecoderSample{}); err != nil {
				return err
			}
			if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_profile_decoder_samples_profile_created ON ` + samples +
				` (tenant_id, device_profile_id, created_at DESC);`).Error; err != nil {
				return err
			}
			if err := addForeignKeyIfAbsent(tx, "profile_decoder_healths", "fk_profile_decoder_healths_profile",
				`ALTER TABLE "device-management"."profile_decoder_healths" `+
					`ADD CONSTRAINT fk_profile_decoder_healths_profile FOREIGN KEY (device_profile_id) `+
					`REFERENCES `+profiles+`(id) ON DELETE CASCADE NOT VALID;`); err != nil {
				return err
			}
			return addForeignKeyIfAbsent(tx, "profile_decoder_samples", "fk_profile_decoder_samples_profile",
				`ALTER TABLE `+samples+` `+
					`ADD CONSTRAINT fk_profile_decoder_samples_profile FOREIGN KEY (device_profile_id) `+
					`REFERENCES `+profiles+`(id) ON DELETE CASCADE NOT VALID;`)
		},
		// 🔴 THERE IS NO ROLLBACK, and that is deliberate: dropping the column would erase every
		// tenant's draft decoders, which are authored work and not reconstructable. That is not a
		// state to walk back to by script.
		Rollback: func(tx *gorm.DB) error {
			return errors.New("the profile decoders migration cannot be rolled back: dropping " +
				"decoder_declaration would erase every tenant's draft decoder")
		},
	}
}
