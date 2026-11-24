// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// AttributeWriterProtocolFunctionSource is the body of dc_require_attr_protocol, byte for byte.
// It is exported for exactly one reason: the activation check compares a hash of what the
// database holds with a hash of what this release ships, and a test in the model package holds
// the two in step. Nothing may build SQL from it.
//
// 🔴 THIS BODY IS FROZEN THE MOMENT IT SHIPS. Changing a character changes the hash every later
// activation check expects, and an already-migrated database keeps the old text, so that
// database would then read as NOT INSTALLED. A change to the rule is a NEW function and a new
// appended migration, never an edit of this string.
//
// The rule: a write that touches a device's SHARED attribute row, in either image, must carry
// the transaction-local marker the writer protocol sets. The trigger takes no lock and reads no
// table; an unmarked writer cannot commit. The WHEN-less trigger filters here, because a WHEN
// clause could keep the function from ever running and the catalog cannot judge a condition.
const AttributeWriterProtocolFunctionSource = `
BEGIN
  IF TG_OP IN ('INSERT', 'UPDATE') THEN
    IF NEW.entity_type = 'device' AND NEW.scope = 'SHARED'
       AND current_setting('devicechain.attr_protocol', true) IS DISTINCT FROM '1' THEN
      RAISE EXCEPTION 'device attribute write without the writer protocol marker'
        USING ERRCODE = 'DCAP1';
    END IF;
  END IF;
  IF TG_OP IN ('UPDATE', 'DELETE') THEN
    IF OLD.entity_type = 'device' AND OLD.scope = 'SHARED'
       AND current_setting('devicechain.attr_protocol', true) IS DISTINCT FROM '1' THEN
      RAISE EXCEPTION 'device attribute write without the writer protocol marker'
        USING ERRCODE = 'DCAP1';
    END IF;
  END IF;
  IF TG_OP = 'DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END;
`

// NewAttributeWriterProtocolSchema installs the database guard behind the device attribute writer
// protocol: a BEFORE INSERT OR UPDATE OR DELETE row trigger on entity_attributes, enabled ALWAYS,
// that refuses (SQLSTATE DCAP1) any write to a device's SHARED row made outside the protocol.
//
// The names are the ones fleet-operation readiness has reserved since the release that shipped
// the protocol's marker (model.FleetOperationEnforcementTrigger and ...Function); the trigger has
// no WHEN condition and ENABLE ALWAYS, which is the only shape readiness reports as ENFORCED.
//
// # Why this is safe to ship now
//
// Every pod of the release before this one already marks its writes (the marker shipped a minor
// earlier, which is the whole reason this is a separate migration), so a supported roll refuses
// nothing. Across a SKIPPED minor, a not-yet-upgraded pod's device SHARED attribute writes fail
// with DCAP1 until the roll completes; that fails closed and corrupts no before-image. The
// release notes say so.
//
// The marker asserts a protocol and is not a security control: anything that can run SQL can set
// it. TRUNCATE and a superuser disabling the trigger are not covered; a disabled trigger reads
// as DISABLED in readiness and fails the activation check. A pg_dump restore loads rows before it
// creates triggers, so restore is unaffected.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure. The function
// is CREATE OR REPLACE, the trigger is created only when no trigger of that name exists ON THIS
// TABLE (the lookup is qualified by tgrelid, so the same name on another table never satisfies
// it), and ENABLE ALWAYS is idempotent and always re-run, so a replay also re-arms a trigger a
// failed earlier attempt left in another mode.
//
// PostgreSQL only: the SQLite unit fixtures have no triggers of this kind and a single writer.
func NewAttributeWriterProtocolSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261224120000",
		Migrate: func(tx *gorm.DB) error {
			if tx.Dialector.Name() != "postgres" {
				return nil
			}
			if err := tx.Exec(`CREATE OR REPLACE FUNCTION "device-management".dc_require_attr_protocol() RETURNS trigger
LANGUAGE plpgsql AS $dcap$` + AttributeWriterProtocolFunctionSource + `$dcap$`).Error; err != nil {
				return err
			}
			if err := tx.Exec(`DO $dcap$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_trigger
    WHERE tgname = 'dc_enforce_attr_protocol'
      AND tgrelid = '"device-management".entity_attributes'::regclass
      AND NOT tgisinternal
  ) THEN
    CREATE TRIGGER dc_enforce_attr_protocol
      BEFORE INSERT OR UPDATE OR DELETE ON "device-management".entity_attributes
      FOR EACH ROW EXECUTE FUNCTION "device-management".dc_require_attr_protocol();
  END IF;
END
$dcap$`).Error; err != nil {
				return err
			}
			if err := tx.Exec(`ALTER TABLE "device-management".entity_attributes ENABLE ALWAYS TRIGGER dc_enforce_attr_protocol`).Error; err != nil {
				return err
			}
			// A trigger of that name that was already there with another shape (an AFTER trigger, a WHEN
			// condition, a missing event, another function) was skipped by the guarded CREATE above. Say so
			// now, loudly, rather than leave readiness reading NOT_INSTALLED for ever with no replay able to fix it.
			var ok int64
			if err := tx.Raw(`SELECT count(*) FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE t.tgrelid = '"device-management".entity_attributes'::regclass AND t.tgname = 'dc_enforce_attr_protocol'
  AND NOT t.tgisinternal AND (t.tgtype::integer & 31) = 31 AND t.tgqual IS NULL AND t.tgenabled = 'A'
  AND n.nspname = 'device-management' AND p.proname = 'dc_require_attr_protocol' AND p.prosrc = ?`, AttributeWriterProtocolFunctionSource).Scan(&ok).Error; err != nil {
				return err
			}
			if ok != 1 {
				return errors.New("a trigger named dc_enforce_attr_protocol already exists on entity_attributes with another shape: " +
					"drop it and re-run, so the attribute writer guard is the one this release ships")
			}
			return nil
		},
		Rollback: func(*gorm.DB) error {
			return errors.New("the attribute writer protocol guard cannot be rolled back during rolling upgrades")
		},
	}
}
