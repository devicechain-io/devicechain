// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// definitionTables are the three definition tables re-parented from the device
// type onto the device profile (ADR-045 slice b).
var definitionTables = []struct {
	table    string
	oldIndex string
	newIndex string
	// keyCols are the per-profile uniqueness columns, appended to device_profile_id.
	keyCols string
}{
	{"metric_definitions", "idx_metric_definition_key", "idx_metric_definition_profile_key", "metric_key"},
	{"command_definitions", "idx_command_definition_key", "idx_command_definition_profile_key", "command_key"},
	{"alarm_definitions", "idx_alarm_definition_key", "idx_alarm_definition_profile_key", "alarm_key, severity"},
}

// NewDefinitionsToProfileSchema re-homes the metric, command, and alarm definitions
// from the device type onto the device profile (ADR-045 slice b). It is a data
// cutover: it adds device_profile_id, seeds a 1:1 profile for every device type that
// carries definitions but has no profile yet, points those types at their new
// profile, repoints the definitions, then drops device_type_id and re-creates the
// per-profile uniqueness indexes. Pre-GA decisive cutover (no installed base); on a
// fresh bring-up there are no definitions and the data steps are no-ops.
func NewDefinitionsToProfileSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260705160000",
		// The whole cutover runs in one transaction. The migration runner does not
		// wrap migrations (rdb UseTransaction is false), and this one drops a column
		// mid-flight — without atomicity a failure at any step (e.g. an edge-case
		// abort below) would leave the schema half-migrated and every subsequent boot
		// failing on the now-missing device_type_id. Postgres DDL is transactional, so
		// wrapping makes the cutover all-or-nothing.
		//
		// Pre-GA assumption (no installed base; fresh bring-up): a few inputs abort
		// this migration *cleanly* (rolled back, service won't start until the data is
		// fixed) rather than corrupting — two definition-bearing types sharing one
		// profile and declaring the same key (a duplicate under the new per-profile
		// unique index), a pre-existing profile already named "<type>-profile", or a
		// device-type token long enough that "<token>-profile" overflows varchar(128).
		// None arise on a fresh bring-up; the GA migration-squash bakes the final shape.
		Migrate: func(tx *gorm.DB) error {
			return tx.Transaction(func(tx *gorm.DB) error {
				// 1. Add the nullable device_profile_id column to each definition table.
				for _, d := range definitionTables {
					if err := tx.Exec("ALTER TABLE " + d.table +
						" ADD COLUMN IF NOT EXISTS device_profile_id bigint").Error; err != nil {
						return err
					}
				}

				// 2. Seed a 1:1 profile for each live device type that has any definition
				//    but no profile. The token is the type's token + "-profile" (grammar-
				//    safe: appending to an already-valid token); per-tenant unique.
				if err := tx.Exec(`
					INSERT INTO device_profiles (created_at, updated_at, tenant_id, token, name)
					SELECT now(), now(), dt.tenant_id, dt.token || '-profile', dt.name
					FROM device_types dt
					WHERE dt.profile_id IS NULL AND dt.deleted_at IS NULL
					  AND (EXISTS (SELECT 1 FROM metric_definitions m  WHERE m.device_type_id = dt.id)
					    OR EXISTS (SELECT 1 FROM command_definitions c WHERE c.device_type_id = dt.id)
					    OR EXISTS (SELECT 1 FROM alarm_definitions a   WHERE a.device_type_id = dt.id))`).Error; err != nil {
					return err
				}

				// 3. Point those types at their freshly-seeded profiles. The same
				//    has-definitions guard as step 2 keeps a coincidentally-named
				//    pre-existing "<type>-profile" from capturing an unrelated type.
				if err := tx.Exec(`
					UPDATE device_types dt SET profile_id = dp.id
					FROM device_profiles dp
					WHERE dt.profile_id IS NULL AND dt.deleted_at IS NULL
					  AND dp.tenant_id = dt.tenant_id
					  AND dp.token = dt.token || '-profile'
					  AND (EXISTS (SELECT 1 FROM metric_definitions m  WHERE m.device_type_id = dt.id)
					    OR EXISTS (SELECT 1 FROM command_definitions c WHERE c.device_type_id = dt.id)
					    OR EXISTS (SELECT 1 FROM alarm_definitions a   WHERE a.device_type_id = dt.id))`).Error; err != nil {
					return err
				}

				// 4. Repoint every definition to its device type's profile.
				for _, d := range definitionTables {
					if err := tx.Exec("UPDATE " + d.table + " def SET device_profile_id = dt.profile_id " +
						"FROM device_types dt WHERE def.device_type_id = dt.id").Error; err != nil {
						return err
					}
				}

				// 5. Drop any orphan definitions (device_type_id matched no type, so they
				//    got no profile) and enforce NOT NULL for parity with the old
				//    device_type_id column and the non-null GraphQL contract.
				for _, d := range definitionTables {
					if err := tx.Exec("DELETE FROM " + d.table + " WHERE device_profile_id IS NULL").Error; err != nil {
						return err
					}
					if err := tx.Exec("ALTER TABLE " + d.table + " ALTER COLUMN device_profile_id SET NOT NULL").Error; err != nil {
						return err
					}
				}

				// 6. Drop the old per-type uniqueness index and the device_type_id column,
				//    then create the new per-profile uniqueness index.
				for _, d := range definitionTables {
					if err := tx.Exec("DROP INDEX IF EXISTS " + d.oldIndex).Error; err != nil {
						return err
					}
					if err := tx.Exec("ALTER TABLE " + d.table + " DROP COLUMN IF EXISTS device_type_id").Error; err != nil {
						return err
					}
					if err := tx.Exec("CREATE UNIQUE INDEX IF NOT EXISTS " + d.newIndex +
						" ON " + d.table + " (device_profile_id, " + d.keyCols + ")").Error; err != nil {
						return err
					}
				}
				return nil
			})
		},
		Rollback: func(tx *gorm.DB) error {
			// Best-effort schema reversal (pre-GA; the type→profile mapping a shared
			// profile collapsed is not restored). Re-add device_type_id (nullable) and
			// restore the per-type index shape; drop the per-profile index + column.
			for _, d := range definitionTables {
				if err := tx.Exec("DROP INDEX IF EXISTS " + d.newIndex).Error; err != nil {
					return err
				}
				if err := tx.Exec("ALTER TABLE " + d.table +
					" ADD COLUMN IF NOT EXISTS device_type_id bigint").Error; err != nil {
					return err
				}
				if err := tx.Exec("CREATE UNIQUE INDEX IF NOT EXISTS " + d.oldIndex +
					" ON " + d.table + " (device_type_id, " + d.keyCols + ")").Error; err != nil {
					return err
				}
				if err := tx.Exec("ALTER TABLE " + d.table + " DROP COLUMN IF EXISTS device_profile_id").Error; err != nil {
					return err
				}
			}
			return nil
		},
	}
}
