// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// provisioningProfileKeySnapshot is the SNAPSHOT of the provisioning_profiles columns the
// per-tenant key index is declared over, as of this migration. It is not
// model.ProvisioningProfile and must never be replaced by it: the index's table and its
// columns are schema, and a migration that resolved them through a live model would be
// rewritten whenever that model changed. It exists only so the index helper can resolve the
// table name; it is never passed to AutoMigrate.
type provisioningProfileKeySnapshot struct {
	ID           uint `gorm:"primarykey"`
	CreatedAt    time.Time
	TenantId     string `gorm:"index;not null;size:128"`
	ProvisionKey string `gorm:"not null;size:256"`
	DeletedAt    gorm.DeletedAt
}

func (provisioningProfileKeySnapshot) TableName() string { return "provisioning_profiles" }

// NewSelfRegistrationSchema is the schema half of device self-registration: what the
// provisioning endpoint decides on and what it records.
//
// # What it changes
//
//  1. provisioning_profiles.provision_key becomes unique PER TENANT among live rows. The
//     baseline made it a GLOBAL unique constraint that also counted soft-deleted rows, so one
//     tenant picking a key learned, from the refusal, that another tenant held it. The
//     endpoint resolves a profile by (tenant, key) from the URL, so the key only has to be
//     unique inside its tenant.
//  2. provisioning_profiles.max_devices: the profile's own cap on devices it registers. NULL is
//     "uncapped".
//  3. provisioning_profiles.provision_secret_issued: set TRUE only by a create or a rotate,
//     the two writes that mint the secret on the server. A profile whose secret was chosen by
//     an operator (every row that existed before this migration) stays FALSE, and the
//     endpoint refuses it until the secret is rotated.
//  4. devices.expected_provisioning_profile_id: the earmark. A profile only ever adopts a
//     device that was earmarked for it. ON DELETE SET NULL, so deleting a profile returns its
//     earmarked, unregistered devices to "no earmark" rather than blocking on them.
//  5. device_credentials.provisioning_profile_id: the provenance of a credential the endpoint
//     issued. ON DELETE RESTRICT, so a profile that issued credentials cannot be deleted out
//     from under them (the API check gives the readable refusal; the FK is the backstop that
//     turns a missed check into a refused delete rather than a dangling id).
//
// # Nullable, no default, no backfill
//
// The new columns on devices and device_credentials are NULL for every existing row, which is
// the truth: no device was ever earmarked and no credential was ever issued by a profile. A
// default would put a value in the catalog's attmissingval (see
// migration_profile_location_declaration.go). provision_secret_issued is the one NOT NULL
// column, and its DEFAULT false is the point rather than a convenience: false is the safe
// answer for a row that existed before the marker did.
//
// # The order, and why the global constraint goes LAST of the key changes
//
// The per-tenant index is created first and the global constraint dropped second, so a
// failure between them leaves the table with both rather than with neither, and the replay
// finishes the job. Dropping first would leave a window with no uniqueness at all.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure. Every
// statement is IF NOT EXISTS / IF EXISTS, and each foreign key is added only when the catalog
// does not already hold it (Postgres has no ADD CONSTRAINT IF NOT EXISTS).
//
// # The foreign keys are added NOT VALID and then validated
//
// ADD CONSTRAINT ... FOREIGN KEY scans the whole table while holding a lock that blocks writes.
// Added NOT VALID it checks only new writes and takes the lock briefly; VALIDATE CONSTRAINT then
// scans without blocking them. Both columns are NULL on every existing row, so validation cannot
// fail, but devices and device_credentials are the busiest tables here and the scan is the cost.
// A replay after a failure between the two statements finds the constraint present and not
// validated, and validates it (VALIDATE of an already-valid constraint is a no-op). The drop names a
// constraint with a hyphen, so it is quoted.
//
// # DDL only
//
// No row is seeded or written, so the DDL-only bar the baselines are held to holds here too.
func NewSelfRegistrationSchema() *gormigrate.Migration {
	const (
		profiles    = `"device-management"."provisioning_profiles"`
		devices     = `"device-management"."devices"`
		credentials = `"device-management"."device_credentials"`
	)

	return &gormigrate.Migration{
		ID: "20261025120000",
		Migrate: func(tx *gorm.DB) error {
			// 1. The per-tenant key index, then the global constraint it replaces. The index
			// helper is this package's own copy (baseline.go says why it is not rdb's).
			if err := createPartialUniqueIndexWhere(tx, &provisioningProfileKeySnapshot{},
				"uix_provisioning_profiles_tenant_provision_key",
				"deleted_at IS NULL", "tenant_id", "provision_key"); err != nil {
				return err
			}
			for _, stmt := range []string{
				`ALTER TABLE ` + profiles +
					` DROP CONSTRAINT IF EXISTS "uni_device-management_provisioning_profiles_provision_key";`,

				// 2, 3. The profile's cap and its issued marker.
				`ALTER TABLE ` + profiles + ` ADD COLUMN IF NOT EXISTS max_devices integer;`,
				`ALTER TABLE ` + profiles +
					` ADD COLUMN IF NOT EXISTS provision_secret_issued boolean NOT NULL DEFAULT false;`,

				// 4. The earmark.
				`ALTER TABLE ` + devices + ` ADD COLUMN IF NOT EXISTS expected_provisioning_profile_id bigint;`,
				`CREATE INDEX IF NOT EXISTS idx_devices_expected_provisioning_profile ON ` + devices +
					` (expected_provisioning_profile_id) WHERE expected_provisioning_profile_id IS NOT NULL;`,

				// 5. The provenance.
				`ALTER TABLE ` + credentials + ` ADD COLUMN IF NOT EXISTS provisioning_profile_id bigint;`,
				`CREATE INDEX IF NOT EXISTS idx_device_credentials_provisioning_profile ON ` + credentials +
					` (provisioning_profile_id) WHERE provisioning_profile_id IS NOT NULL;`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			// The foreign keys, each only when the catalog does not hold it yet.
			if err := addForeignKeyIfAbsent(tx, "devices", "fk_devices_expected_provisioning_profile",
				`ALTER TABLE `+devices+` ADD CONSTRAINT fk_devices_expected_provisioning_profile `+
					`FOREIGN KEY (expected_provisioning_profile_id) REFERENCES `+profiles+`(id) ON DELETE SET NULL NOT VALID;`); err != nil {
				return err
			}
			return addForeignKeyIfAbsent(tx, "device_credentials", "fk_device_credentials_provisioning_profile",
				`ALTER TABLE `+credentials+` ADD CONSTRAINT fk_device_credentials_provisioning_profile `+
					`FOREIGN KEY (provisioning_profile_id) REFERENCES `+profiles+`(id) ON DELETE RESTRICT NOT VALID;`)
		},
		// 🔴 THERE IS NO ROLLBACK, and that is deliberate. Dropping provisioning_profile_id
		// would erase which profile issued which credential, and re-adding the global key
		// constraint would fail the day two tenants hold the same key. Neither is a state to
		// walk back to by script.
		Rollback: func(tx *gorm.DB) error {
			return errors.New("the self-registration migration cannot be rolled back: dropping " +
				"provisioning_profile_id would erase which profile issued which credential, and " +
				"re-adding the global provision key constraint fails once two tenants hold one key")
		},
	}
}

// addForeignKeyIfAbsent runs the ADD CONSTRAINT statement unless the schema already holds a
// constraint of that name on that table of the area's schema, then validates it. Postgres has no ADD CONSTRAINT
// IF NOT EXISTS, so the catalog is asked, naming the table rather than relying on the
// constraint name being unique across schemas.
func addForeignKeyIfAbsent(tx *gorm.DB, table, name, stmt string) error {
	var present int64
	if err := tx.Raw(`SELECT count(*) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid `+
		`JOIN pg_namespace n ON n.oid = t.relnamespace `+
		`WHERE n.nspname = ? AND t.relname = ? AND c.conname = ?`,
		"device-management", table, name).Scan(&present).Error; err != nil {
		return err
	}
	if present == 0 {
		if err := tx.Exec(stmt).Error; err != nil {
			return err
		}
	}
	// Always validated: a constraint left NOT VALID by an interrupted run is finished by the
	// replay, and validating one that is already valid does nothing.
	return tx.Exec(`ALTER TABLE "device-management".` + `"` + table + `" VALIDATE CONSTRAINT "` + name + `";`).Error
}
