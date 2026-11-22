// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDecoderHealthWindowIndexesSchema adds the two indexes the decoder health reads need.
//
// The table's only multi-column index is the upsert's conflict target,
// uix_profile_decoder_healths_window (tenant_id, device_profile_id, profile_version, window_start,
// replica). window_start is its FOURTH column, so neither read can use it for the range:
//
//   - payloadDecoderHealth sums one profile's rows over a span (tenant_id, device_profile_id,
//     window_start >= since): ix_profile_decoder_healths_profile_window leads with the two
//     equality columns and ranges on the third.
//   - the trim deletes every tenant's rows older than a cutoff (window_start < before, first across
//     tenants to find who has any, then per tenant): ix_profile_decoder_healths_window_start serves
//     the cross-tenant range, and the profile index above is no help to it because the trim names
//     no profile.
//
// 🔴 NO SNAPSHOT STRUCT, for the reason NewListOrderIndexesSchema gives: two indexes over named
// columns, every name written literally in the DDL below, which IS the snapshot. A struct would
// need a TableName() that bypasses this area's table prefix, and AutoMigrate on it would re-assert
// columns this change has no business touching.
//
// Re-runnable (IF NOT EXISTS; migrations run with UseTransaction:false and replay from the top),
// and not CONCURRENTLY, for the reason NewListOrderIndexesSchema gives: a failed concurrent build
// leaves an INVALID index the replay's IF NOT EXISTS then skips forever. The plain build holds the
// table's write lock for its length; the table is one row per replica per profile version per
// minute, trimmed to a week, so the build is short, and the writers it holds back are the health
// flusher's upserts, which retry on the next flush.
func NewDecoderHealthWindowIndexesSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261222120000",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range []string{
				`CREATE INDEX IF NOT EXISTS ix_profile_decoder_healths_profile_window ` +
					`ON "device-management".profile_decoder_healths (tenant_id, device_profile_id, window_start);`,
				`CREATE INDEX IF NOT EXISTS ix_profile_decoder_healths_window_start ` +
					`ON "device-management".profile_decoder_healths (window_start);`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		// Dropping them leaves every row in place and every read answering the same; the reads just
		// scan again. Re-runnable in this direction too.
		Rollback: func(tx *gorm.DB) error {
			for _, name := range []string{
				"ix_profile_decoder_healths_window_start",
				"ix_profile_decoder_healths_profile_window",
			} {
				if err := tx.Exec(`DROP INDEX IF EXISTS "device-management".` + name + `;`).Error; err != nil {
					return err
				}
			}
			return nil
		},
	}
}
