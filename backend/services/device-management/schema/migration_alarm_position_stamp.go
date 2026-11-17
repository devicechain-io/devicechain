// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewAlarmPositionStampSchema makes an alarm's position classification say WHICH contributor set it
// was decided for, so a fold by a replica that does not know the classification exists cannot
// leave a false verdict standing over a set that now holds a geofence rule.
//
// NewAlarmPositionDerivedSchema classified every alarm, and the service classifies what a replica
// older than that column writes (NULL). But an older replica can also fold an edge into an alarm
// that is ALREADY classified false: it bumps contributor_version and adds the rule to the set, and
// leaves position_derived false, because it does not know the column. If that rule tests a fence,
// a caller without location:read goes on reading a position-derived alarm, and nothing ever
// re-decided a false.
//
//  1. alarms.position_classified_version: the contributor_version a verdict was decided at.
//     Nullable, no default: a replica of the previous release never writes it, and its writes
//     keep succeeding. NULL is "never stamped".
//  2. Trigger alarm_position_stale, BEFORE UPDATE OF contributor_version: a write that leaves
//     position_derived false and the stamp different from the version it writes sets
//     position_derived to NULL in that same row write. Every fold bumps contributor_version, so an
//     older replica's fold over a false row is caught whatever it writes; the current integrator
//     carries the stamp forward when its edge's verdict keeps the false current, so its own folds
//     are not. NULL is withheld from a caller without location:read and classified by the next
//     pass, so the error is towards withholding, never towards showing, and it lasts only until
//     that pass. A true verdict is sticky and is never touched.
//  3. Every false verdict decided before the stamp existed is re-opened (set to NULL) and
//  4. classified again by AlarmPositionClassifyStampedSQL, which stamps what it decides. That
//     covers a fold an older replica made between the previous migration and this one.
//
// Re-runnable: ADD COLUMN IF NOT EXISTS, CREATE OR REPLACE FUNCTION, DROP TRIGGER IF EXISTS before
// CREATE TRIGGER, and steps 3 and 4 touch only unstamped false rows and NULL rows, so a replay
// after a partial run re-opens and classifies what is left and changes nothing already stamped.
// No snapshot struct: the statements name their table and columns literally, for the reason
// migrations.go gives.
func NewAlarmPositionStampSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261217120000",
		Migrate: func(tx *gorm.DB) error {
			for _, statement := range []string{
				`ALTER TABLE "device-management"."alarms" ADD COLUMN IF NOT EXISTS position_classified_version bigint;`,
				alarmPositionStaleFunctionSQL,
				alarmPositionStaleTriggerSQL,
				`UPDATE "device-management"."alarms" SET position_derived = NULL ` +
					`WHERE position_derived IS FALSE AND position_classified_version IS NULL;`,
				AlarmPositionClassifyStampedSQL,
			} {
				if err := tx.Exec(statement).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return errors.New("the alarm position stamp migration cannot be rolled back: without it a fold by an " +
				"older replica could leave a geofence alarm readable by callers that may not read device positions")
		},
	}
}

// alarmPositionStaleFunctionSQL is the trigger function of step 2 in NewAlarmPositionStampSchema.
const alarmPositionStaleFunctionSQL = `CREATE OR REPLACE FUNCTION "device-management".alarm_position_stale() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.position_derived IS FALSE
     AND NEW.position_classified_version IS DISTINCT FROM NEW.contributor_version THEN
    NEW.position_derived := NULL;
  END IF;
  RETURN NEW;
END
$$;`

// alarmPositionStaleTriggerSQL binds the function to every write that names contributor_version:
// every contributor fold, from any release.
const alarmPositionStaleTriggerSQL = `DROP TRIGGER IF EXISTS alarm_position_stale ON "device-management"."alarms";
CREATE TRIGGER alarm_position_stale BEFORE UPDATE OF contributor_version ON "device-management"."alarms"
FOR EACH ROW EXECUTE FUNCTION "device-management".alarm_position_stale();`

// AlarmPositionClassifyStampedSQL classifies every unclassified alarm and stamps each verdict with
// the contributor version it judged. It is AlarmPositionClassifySQL with the stamp added; see
// NewAlarmPositionDerivedSchema for the judgement itself.
//
// The service runs the same statement (model/position_classify.go), and a test keeps the two
// identical; this literal is the migration's own copy, as the rule in migrations.go requires.
const AlarmPositionClassifyStampedSQL = `
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
            WHERE strpos(k.key, '/') = 0 OR s.found = 0 OR s.fence)),
       position_classified_version = a.contributor_version
 WHERE a.position_derived IS NULL;`
