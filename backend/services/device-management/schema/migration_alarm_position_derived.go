// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewAlarmPositionDerivedSchema adds alarms.position_derived: whether a rule that tests geofence
// containment has contributed to the alarm. Such an alarm says when its device entered or left a
// fence, so a caller without location:read does not read it, exactly as it does not receive the
// rule's live detections.
//
//  1. The column is nullable with no default (see migration_device_disabled.go for why a
//     default is avoided). It is three-valued: true and false are verdicts, and NULL is "not
//     classified yet", which a caller without location:read is denied like true. A replica of
//     the previous release, which writes alarms without the column during a rolling upgrade,
//     keeps succeeding and leaves NULL, which the service classifies on its next pass (see
//     model/position_classify.go).
//  2. A partial index on the unclassified alarms, so that periodic pass is a cheap probe when
//     there is nothing to classify, which is almost always.
//  3. Every existing alarm is CLASSIFIED by AlarmPositionClassifySQL. The judgement the running
//     service receives on each edge is event-processing's compiler (does the rule's condition
//     call inFence); a migration cannot run it, so this makes the conservative textual version
//     of it. An alarm is marked when any of its contributors (the rule identities
//     "{profileToken}/{ruleToken}" keyed in alarms.contributors) names a rule whose definition
//     contains the text inFence, in the profile's draft rules or in any published version of the
//     profile, OR names a rule that can no longer be found: deleting a profile removes its rules
//     and published versions with it, so a contributor of a deleted profile is unresolvable and
//     is marked (fail closed). A rule that mentions inFence only in a name or description is
//     marked as well: every error is towards withholding, never towards showing. Every other
//     alarm, including one with no contributor set (raised by the retired measurement evaluator,
//     which had no geofence predicate), is classified false.
//
// Re-runnable: ADD COLUMN and CREATE INDEX IF NOT EXISTS, and the classification only touches
// NULL rows, so a replay after a partial first pass classifies what is left and changes nothing
// it already classified. No snapshot struct: the statements name their tables and columns
// literally, for the reason migrations.go gives.
func NewAlarmPositionDerivedSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261203090000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`ALTER TABLE "device-management"."alarms" ` +
				`ADD COLUMN IF NOT EXISTS position_derived boolean;`).Error; err != nil {
				return err
			}
			if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_alarms_position_unclassified ` +
				`ON "device-management"."alarms" (id) WHERE position_derived IS NULL;`).Error; err != nil {
				return err
			}
			return tx.Exec(AlarmPositionClassifySQL).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return errors.New("the alarm position-derived migration cannot be rolled back: dropping the column " +
				"would show geofence alarms to callers that may not read device positions")
		},
	}
}

// AlarmPositionClassifySQL classifies every unclassified alarm. See NewAlarmPositionDerivedSchema.
//
// The published snapshot's keys are what model.ProfileSnapshot marshals to: the rule list under
// "rules" (its json tag) and each rule's "Token" and "Definition" (model.DetectionRule carries no
// json tags, so the stored key is the Go field name).
//
// The service runs the same statement (model/position_classify.go), and a test keeps the two
// identical; this literal is the migration's own copy, as the rule in migrations.go requires.
const AlarmPositionClassifySQL = `
UPDATE "device-management"."alarms" AS a
   SET position_derived = (
       jsonb_typeof(a.contributors) = 'object'
       AND EXISTS (
           SELECT 1
             FROM jsonb_object_keys(a.contributors) AS k(key)
             CROSS JOIN LATERAL (
                 SELECT count(*) AS found, coalesce(bool_or(d.def LIKE '%inFence%'), false) AS fence
                   FROM (
                       SELECT r.definition::text AS def
                         FROM "device-management"."device_profiles" AS p
                         JOIN "device-management"."detection_rules" AS r ON r.device_profile_id = p.id
                        WHERE p.tenant_id = a.tenant_id
                          AND p.token = split_part(k.key, '/', 1)
                          AND r.token = split_part(k.key, '/', 2)
                       UNION ALL
                       SELECT (e.rule -> 'Definition')::text AS def
                         FROM "device-management"."device_profiles" AS p
                         JOIN "device-management"."device_profile_versions" AS v ON v.device_profile_id = p.id
                        CROSS JOIN LATERAL jsonb_array_elements(
                              CASE WHEN jsonb_typeof(v.snapshot -> 'rules') = 'array'
                                   THEN v.snapshot -> 'rules' ELSE '[]'::jsonb END) AS e(rule)
                        WHERE p.tenant_id = a.tenant_id
                          AND p.token = split_part(k.key, '/', 1)
                          AND e.rule ->> 'Token' = split_part(k.key, '/', 2)
                   ) AS d
             ) AS s
            WHERE strpos(k.key, '/') = 0 OR s.found = 0 OR s.fence))
 WHERE a.position_derived IS NULL;`
