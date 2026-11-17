// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewAlarmPositionDerivedSchema adds alarms.position_derived: true once a rule that tests
// geofence containment has contributed to the alarm. Such an alarm says when its device entered
// or left a fence, so a caller without location:read does not read it, exactly as it does not
// receive the rule's live detections.
//
//  1. The column is nullable with no default (see migration_device_disabled.go for why a
//     default is avoided). NULL reads as "not position-derived", and a replica of the previous
//     release, which writes alarms without the column during a rolling upgrade, keeps
//     succeeding.
//  2. The existing alarms are BACKFILLED, because without it every alarm a geofence rule raised
//     before the upgrade would stay readable to a caller without location:read. The judgement
//     the running service makes is event-processing's compiler (does the rule's condition call
//     inFence); that compiler is not available to a migration, so the backfill makes the
//     conservative textual version of it: an alarm is marked when any of its contributors (the
//     rule identities "{profileToken}/{ruleToken}" keyed in alarms.contributors) names a rule
//     whose definition contains the text inFence, in the profile's draft rules or in ANY
//     published version of the profile. Deleted rules and profiles count too. A rule that names
//     inFence only in a description or a name is marked as well: the error is towards
//     withholding, never towards showing. An alarm the integrator raised before contributors
//     existed has none and is not marked; no geofence rule predates the contributor set.
//
// Re-runnable: ADD COLUMN IF NOT EXISTS, and the backfill only ever sets NULL to true, so a
// replay after a partial first pass marks what is left and changes nothing it already marked.
// No snapshot struct: the statements name their tables and columns literally, for the reason
// migrations.go gives.
func NewAlarmPositionDerivedSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261203090000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`ALTER TABLE "device-management"."alarms" ` +
				`ADD COLUMN IF NOT EXISTS position_derived boolean;`).Error; err != nil {
				return err
			}
			return tx.Exec(alarmPositionDerivedBackfill).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return errors.New("the alarm position-derived migration cannot be rolled back: dropping the column " +
				"would show geofence alarms to callers that may not read device positions")
		},
	}
}

// alarmPositionDerivedBackfill marks the existing alarms a geofence rule contributed to. See
// NewAlarmPositionDerivedSchema.
const alarmPositionDerivedBackfill = `
UPDATE "device-management"."alarms" AS a
   SET position_derived = true
 WHERE a.position_derived IS NULL
   AND jsonb_typeof(a.contributors) = 'object'
   AND EXISTS (
       SELECT 1
         FROM jsonb_object_keys(a.contributors) AS k(key)
         JOIN "device-management"."device_profiles" AS p
           ON p.tenant_id = a.tenant_id
          AND p.token = split_part(k.key, '/', 1)
        WHERE EXISTS (
                SELECT 1
                  FROM "device-management"."detection_rules" AS r
                 WHERE r.device_profile_id = p.id
                   AND r.token = split_part(k.key, '/', 2)
                   AND r.definition::text LIKE '%inFence%')
           OR EXISTS (
                SELECT 1
                  FROM "device-management"."device_profile_versions" AS v
                 CROSS JOIN LATERAL jsonb_array_elements(
                       CASE WHEN jsonb_typeof(v.snapshot -> 'Rules') = 'array'
                            THEN v.snapshot -> 'Rules' ELSE '[]'::jsonb END) AS e(rule)
                 WHERE v.device_profile_id = p.id
                   AND e.rule ->> 'Token' = split_part(k.key, '/', 2)
                   AND (e.rule -> 'Definition')::text LIKE '%inFence%'));`
