// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type automationReviewPolicy struct {
	ID                  uint `gorm:"primarykey"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
	TenantId            string `gorm:"index;not null;size:128;uniqueIndex:uix_automation_review_policies_profile,priority:1"`
	DeviceProfileId     uint   `gorm:"not null;uniqueIndex:uix_automation_review_policies_profile,priority:2"`
	Required            bool   `gorm:"not null;default:false"`
	RequiredApprovals   int16  `gorm:"not null;default:1"`
	AllowSelfApproval   bool   `gorm:"not null;default:false"`
	ApprovalTtlSeconds  int32  `gorm:"not null;default:86400"`
	StepUpMaxAgeSeconds int32  `gorm:"not null;default:900"`
	Revision            int32  `gorm:"not null;default:1"`
	UpdatedBy           string `gorm:"size:320;not null"`
}

type automationReviewPolicyChange struct {
	ID              uint `gorm:"primarykey"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
	TenantId        string         `gorm:"index;not null;size:128;uniqueIndex:uix_automation_review_policy_changes_token,priority:1"`
	Token           string         `gorm:"size:64;not null;uniqueIndex:uix_automation_review_policy_changes_token,priority:2"`
	DeviceProfileId uint           `gorm:"not null;index"`
	BaseRevision    int32          `gorm:"not null"`
	Proposed        datatypes.JSON `gorm:"not null"`
	ProposedBy      string         `gorm:"size:320;not null"`
	State           string         `gorm:"size:16;not null;index"`
	EffectiveAt     sql.NullTime
	DecidedBy       sql.NullString `gorm:"size:320"`
	DecidedAt       sql.NullTime
	Revision        int `gorm:"not null;default:0"`
}

type automationReviewBreakGlass struct {
	ID              uint `gorm:"primarykey"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
	TenantId        string         `gorm:"index;not null;size:128"`
	DeviceProfileId uint           `gorm:"not null;index"`
	Action          string         `gorm:"size:32;not null"`
	Target          string         `gorm:"size:128;not null"`
	Reason          string         `gorm:"size:512;not null"`
	Actor           string         `gorm:"size:320;not null"`
	Amr             datatypes.JSON `gorm:"not null"`
	MfaTime         sql.NullTime
	Txid            int64 `gorm:"not null;index"`
}

type enforcementProfileVersion struct {
	AutomationReviewToken *string `gorm:"size:64"`
}

// NewAutomationReviewEnforcementSchema is replayable: AutoMigrate, catalog-checked
// constraints, replaced functions and dropped/recreated triggers tolerate interrupted runs.
// It seeds no rows. All four affected tables carry tenant_id; policies and policy
// changes reference device_profiles, so tenant purge still deletes children first
// in foreign-key order. ON DELETE CASCADE lets old pods delete unprotected profiles
// with policy history. The profile's BEFORE DELETE guard refuses protected deletes
// before the foreign-key cascade runs as an AFTER trigger.
// During rolling upgrades old pods never write these tables. Triggers refuse their
// writes on protected profiles and permit everything on unprotected profiles.
func NewAutomationReviewEnforcementSchema() *gormigrate.Migration {
	return &gormigrate.Migration{ID: "20261126120000", Migrate: func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(&automationReviewPolicy{}, &automationReviewPolicyChange{}); err != nil {
			return err
		}
		name := tx.NamingStrategy.TableName("automationReviewBreakGlass")
		if strings.HasSuffix(name, "glasses") {
			name = strings.TrimSuffix(name, "glasses") + "glass"
		}
		if err := tx.Table(name).AutoMigrate(&automationReviewBreakGlass{}); err != nil {
			return err
		}
		versions := tx.NamingStrategy.TableName("deviceProfileVersion")
		if !tx.Migrator().HasColumn(versions, "automation_review_token") {
			if err := tx.Table(versions).Migrator().AddColumn(&enforcementProfileVersion{}, "AutomationReviewToken"); err != nil {
				return err
			}
		}
		if tx.Dialector.Name() != "postgres" {
			return nil
		}
		for _, table := range []string{"automation_review_policies", "automation_review_policy_changes"} {
			constraint := "fk_" + table + "_profile"
			if err := addForeignKeyIfAbsent(tx, table, constraint, fmt.Sprintf(`ALTER TABLE "device-management".%s ADD CONSTRAINT %s FOREIGN KEY (device_profile_id) REFERENCES "device-management".device_profiles(id) ON DELETE CASCADE NOT VALID`, table, constraint)); err != nil {
				return err
			}
		}
		for _, check := range []struct{ name, expression string }{
			{"ck_automation_review_policies_approvals", "required_approvals IN (1,2)"},
			{"ck_automation_review_policies_ttl", "approval_ttl_seconds BETWEEN 900 AND 604800"},
			{"ck_automation_review_policies_step_up", "step_up_max_age_seconds BETWEEN 60 AND 3600"},
		} {
			if err := addReviewCheckIfAbsent(tx, check.name, check.expression); err != nil {
				return err
			}
		}
		for _, statement := range []string{automationReviewPolicyRequiredSQL, automationReviewGuardSQL,
			automationReviewVersionInsertTriggerSQL, automationReviewVersionUpdateTriggerSQL,
			automationReviewProfileUpdateTriggerSQL, automationReviewProfileDeleteTriggerSQL,
			automationReviewTypeUpdateTriggerSQL, automationReviewDeviceUpdateTriggerSQL} {
			if err := tx.Exec(statement).Error; err != nil {
				return err
			}
		}
		return nil
	}, Rollback: func(tx *gorm.DB) error {
		// Dropping policy or journal data would erase protection and its attribution.
		return errors.New("automation review enforcement cannot be rolled back: policy and journal history must be retained")
	}}
}

func addReviewCheckIfAbsent(tx *gorm.DB, name, expression string) error {
	var count int64
	if err := tx.Raw(`SELECT count(*) FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname=? AND t.relname=? AND c.conname=?`, "device-management", "automation_review_policies", name).Scan(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	return tx.Exec(fmt.Sprintf(`ALTER TABLE "device-management".automation_review_policies ADD CONSTRAINT %s CHECK (%s)`, name, expression)).Error
}

const automationReviewPolicyRequiredSQL = `CREATE OR REPLACE FUNCTION "device-management".automation_review_policy_required(p_profile bigint)
RETURNS boolean LANGUAGE sql VOLATILE AS $f$
 SELECT EXISTS (SELECT 1 FROM "device-management".automation_review_policies
 WHERE device_profile_id = p_profile AND required)
$f$;`

const automationReviewGuardSQL = `CREATE OR REPLACE FUNCTION "device-management".automation_review_guard()
RETURNS trigger LANGUAGE plpgsql VOLATILE AS $f$
DECLARE
 p bigint;
BEGIN
 -- Lock the profile before a separate policy read, which takes a fresh READ COMMITTED snapshot.
 IF TG_TABLE_NAME = 'device_profile_versions' THEN
  IF TG_OP = 'INSERT' THEN
   PERFORM 1 FROM "device-management".device_profiles WHERE id = NEW.device_profile_id FOR NO KEY UPDATE;
   IF "device-management".automation_review_policy_required(NEW.device_profile_id) AND NOT EXISTS (
    SELECT 1 FROM "device-management".automation_reviews r
    WHERE r.token = NEW.automation_review_token AND r.tenant_id = NEW.tenant_id
     AND r.device_profile_id = NEW.device_profile_id AND r.state = 'CONSUMED'
     AND r.consumed_version = NEW.version AND r.consumed_txid = txid_current()) THEN
    RAISE EXCEPTION USING ERRCODE = 'DCR01', MESSAGE = 'REVIEW_REQUIRED: this device profile requires an approved automation review to publish';
   END IF;
   RETURN NEW;
  END IF;
  IF OLD.snapshot IS DISTINCT FROM NEW.snapshot
   OR OLD.automation_review_token IS DISTINCT FROM NEW.automation_review_token
   OR OLD.version IS DISTINCT FROM NEW.version
   OR OLD.device_profile_id IS DISTINCT FROM NEW.device_profile_id
   OR OLD.deleted_at IS DISTINCT FROM NEW.deleted_at THEN
   PERFORM 1 FROM "device-management".device_profiles WHERE id IN (OLD.device_profile_id, NEW.device_profile_id) FOR SHARE;
   IF "device-management".automation_review_policy_required(OLD.device_profile_id)
    OR "device-management".automation_review_policy_required(NEW.device_profile_id) THEN
    RAISE EXCEPTION USING ERRCODE = 'DCR02', MESSAGE = 'PROFILE_PROTECTED: published versions of a protected device profile cannot be changed';
   END IF;
  END IF;
  RETURN NEW;
 END IF;
 IF TG_TABLE_NAME = 'device_profiles' THEN
  IF TG_OP = 'DELETE' THEN
   IF "device-management".automation_review_policy_required(OLD.id) THEN
    RAISE EXCEPTION USING ERRCODE = 'DCR02', MESSAGE = 'PROFILE_PROTECTED: a protected device profile cannot be deleted; remove its protection first';
   END IF;
   RETURN OLD;
  END IF;
  IF OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL
   AND "device-management".automation_review_policy_required(OLD.id) THEN
   RAISE EXCEPTION USING ERRCODE = 'DCR02', MESSAGE = 'PROFILE_PROTECTED: a protected device profile cannot be deleted; remove its protection first';
  END IF;
  IF (OLD.active_version IS DISTINCT FROM NEW.active_version OR OLD.active_since IS DISTINCT FROM NEW.active_since)
   AND "device-management".automation_review_policy_required(NEW.id)
   AND NOT EXISTS (SELECT 1 FROM "device-management".automation_reviews r
    WHERE r.device_profile_id = NEW.id AND r.tenant_id = NEW.tenant_id AND r.state = 'CONSUMED'
     AND r.consumed_version = NEW.active_version AND r.consumed_txid = txid_current())
   AND NOT EXISTS (SELECT 1 FROM "device-management".automation_review_break_glass b
    WHERE b.device_profile_id = NEW.id AND b.tenant_id = NEW.tenant_id AND b.action = 'ROLLBACK'
     AND b.target = NEW.active_version::text AND b.txid = txid_current()) THEN
   RAISE EXCEPTION USING ERRCODE = 'DCR01', MESSAGE = 'REVIEW_REQUIRED: the active version of a protected device profile changes only through a reviewed publish or a break-glass rollback';
  END IF;
  RETURN NEW;
 END IF;
 IF TG_TABLE_NAME = 'device_types' THEN
  IF OLD.profile_id IS DISTINCT FROM NEW.profile_id THEN
   FOREACH p IN ARRAY ARRAY[OLD.profile_id, NEW.profile_id] LOOP
    CONTINUE WHEN p IS NULL;
    PERFORM 1 FROM "device-management".device_profiles WHERE id = p FOR SHARE;
    IF "device-management".automation_review_policy_required(p) AND NOT EXISTS (
     SELECT 1 FROM "device-management".automation_review_break_glass b
     WHERE b.device_profile_id = p AND b.tenant_id = NEW.tenant_id AND b.action = 'REPOINT_TYPE'
      AND b.target = NEW.token AND b.txid = txid_current()) THEN
     RAISE EXCEPTION USING ERRCODE = 'DCR02', MESSAGE = 'PROFILE_PROTECTED: re-pointing a device type to or from a protected device profile needs a break-glass reason';
    END IF;
   END LOOP;
  END IF;
  RETURN NEW;
 END IF;
 IF TG_TABLE_NAME = 'devices' THEN
  IF OLD.device_type_id IS DISTINCT FROM NEW.device_type_id AND OLD.device_type_id IS NOT NULL THEN
   SELECT t.profile_id INTO p FROM "device-management".device_types t WHERE t.id = OLD.device_type_id;
   IF p IS NOT NULL THEN
    PERFORM 1 FROM "device-management".device_profiles WHERE id = p FOR SHARE;
    IF "device-management".automation_review_policy_required(p) AND NOT EXISTS (
     SELECT 1 FROM "device-management".automation_review_break_glass b
     WHERE b.device_profile_id = p AND b.tenant_id = NEW.tenant_id AND b.action = 'MOVE_DEVICE'
      AND b.target = NEW.token AND b.txid = txid_current()) THEN
     RAISE EXCEPTION USING ERRCODE = 'DCR02', MESSAGE = 'PROFILE_PROTECTED: moving a device out of a type bound to a protected device profile needs a break-glass reason';
    END IF;
   END IF;
  END IF;
  RETURN NEW;
 END IF;
 RETURN NEW;
END
$f$;`

const automationReviewVersionInsertTriggerSQL = `DROP TRIGGER IF EXISTS automation_review_guard_version_insert ON "device-management".device_profile_versions;
CREATE TRIGGER automation_review_guard_version_insert BEFORE INSERT ON "device-management".device_profile_versions FOR EACH ROW EXECUTE FUNCTION "device-management".automation_review_guard();`

const automationReviewVersionUpdateTriggerSQL = `DROP TRIGGER IF EXISTS automation_review_guard_version_update ON "device-management".device_profile_versions;
CREATE TRIGGER automation_review_guard_version_update BEFORE UPDATE ON "device-management".device_profile_versions FOR EACH ROW EXECUTE FUNCTION "device-management".automation_review_guard();`

const automationReviewProfileUpdateTriggerSQL = `DROP TRIGGER IF EXISTS automation_review_guard_profile_update ON "device-management".device_profiles;
CREATE TRIGGER automation_review_guard_profile_update BEFORE UPDATE ON "device-management".device_profiles FOR EACH ROW EXECUTE FUNCTION "device-management".automation_review_guard();`

const automationReviewProfileDeleteTriggerSQL = `DROP TRIGGER IF EXISTS automation_review_guard_profile_delete ON "device-management".device_profiles;
CREATE TRIGGER automation_review_guard_profile_delete BEFORE DELETE ON "device-management".device_profiles FOR EACH ROW EXECUTE FUNCTION "device-management".automation_review_guard();`

const automationReviewTypeUpdateTriggerSQL = `DROP TRIGGER IF EXISTS automation_review_guard_type_update ON "device-management".device_types;
CREATE TRIGGER automation_review_guard_type_update BEFORE UPDATE OF profile_id ON "device-management".device_types FOR EACH ROW EXECUTE FUNCTION "device-management".automation_review_guard();`

const automationReviewDeviceUpdateTriggerSQL = `DROP TRIGGER IF EXISTS automation_review_guard_device_update ON "device-management".devices;
CREATE TRIGGER automation_review_guard_device_update BEFORE UPDATE OF device_type_id ON "device-management".devices FOR EACH ROW EXECUTE FUNCTION "device-management".automation_review_guard();`
