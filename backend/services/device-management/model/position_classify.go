// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
)

// Alarm position CLASSIFICATION: deciding, in device-management, whether an alarm is
// position-derived when the raise-alarm edge did not say.
//
// alarms.position_derived is three-valued. true and false are verdicts; NULL is "not classified
// yet", and a caller without location:read is denied a NULL alarm as it is denied a true one
// (position_withholding.go). The verdict normally arrives on the edge (RaiseAlarmRequest.
// RequiresPosition, judged by event-processing's compiler). Three writers cannot supply it:
//
//   - the upgrade's backfill, for every alarm that existed before the column;
//   - an edge from an event-processing replica older than the field, during a rolling upgrade.
//     The flag is ABSENT, which is not false: the integrator stores NULL and classifies the
//     alarm here at once;
//   - a device-management replica older than the column, which during the same rollout folds
//     edges and creates alarms without writing it at all. Those rows are NULL too, and the
//     classification task (ClassifyUnclassifiedAlarms, every replica, every minute) classifies
//     them.
//
// So a NULL never outlives the next pass, and a rollout needs no second migration: whatever an
// old replica writes, the next pass after it classifies.
//
// The classifier cannot run the compiler, so it makes the conservative textual version of its
// judgement. An alarm is position-derived when ANY of its contributors (the rule identities
// "{profileToken}/{ruleToken}" keyed in alarms.contributors) either names a rule whose definition
// contains the text inFence, in the profile's draft rules or in any published version of the
// profile, or names a rule that can no longer be found at all: its profile deleted (a profile
// delete removes its rules and versions with it), the rule deleted from the draft and never
// published, or a contributor id that is not in the "{profile}/{rule}" form. An unresolvable rule
// is treated as position-derived for the same reason the REACT dispatcher treats a rule it cannot
// compile so: an error here is towards withholding, never towards showing. An alarm with no
// contributor set (raised before the set existed, by the retired measurement evaluator, which had
// no geofence predicate) is not position-derived.
//
// 🔴 schema/migration_alarm_position_derived.go carries the same statement as a literal, as a
// migration must. They are kept identical by TestClassifierMatchesTheMigration.

// classifyAlarmsSQL classifies every unclassified alarm; a caller narrows it to one alarm by
// appending " AND a.tenant_id = ? AND a.id = ?". It is a CONSTANT with unqualified table names on purpose: the
// connection's search_path is pinned to this area's schema (core/rdb), and a constant is what the
// raw-SQL guard (backend/tools/rdbguard) can read, so the tables this cross-tenant statement
// touches are visible to it and to its allow-list entry. Every join binds p.tenant_id to the
// alarm's own tenant_id, so a rule is only ever resolved within the alarm's tenant.
//
// The published snapshot's keys are what ProfileSnapshot marshals to: the rule list under
// "rules" (its json tag) and each rule's "Token" and "Definition" (DetectionRule carries no json
// tags, so the stored key is the Go field name). TestProfileSnapshotStoredKeysArePinned pins them.
const classifyAlarmsSQL = `
UPDATE alarms AS a
   SET position_derived = (
       jsonb_typeof(a.contributors) = 'object'
       AND EXISTS (
           SELECT 1
             FROM jsonb_object_keys(a.contributors) AS k(key)
             CROSS JOIN LATERAL (
                 SELECT count(*) AS found, coalesce(bool_or(d.def LIKE '%inFence%'), false) AS fence
                   FROM (
                       SELECT r.definition::text AS def
                         FROM device_profiles AS p
                         JOIN detection_rules AS r ON r.device_profile_id = p.id
                        WHERE p.tenant_id = a.tenant_id
                          AND p.token = split_part(k.key, '/', 1)
                          AND r.token = split_part(k.key, '/', 2)
                       UNION ALL
                       SELECT (e.rule -> 'Definition')::text AS def
                         FROM device_profiles AS p
                         JOIN device_profile_versions AS v ON v.device_profile_id = p.id
                        CROSS JOIN LATERAL jsonb_array_elements(
                              CASE WHEN jsonb_typeof(v.snapshot -> 'rules') = 'array'
                                   THEN v.snapshot -> 'rules' ELSE '[]'::jsonb END) AS e(rule)
                        WHERE p.tenant_id = a.tenant_id
                          AND p.token = split_part(k.key, '/', 1)
                          AND e.rule ->> 'Token' = split_part(k.key, '/', 2)
                   ) AS d
             ) AS s
            WHERE strpos(k.key, '/') = 0 OR s.found = 0 OR s.fence))
 WHERE a.position_derived IS NULL`

// ClassifyUnclassifiedAlarms classifies every alarm whose position_derived is NULL, across all
// tenants, and returns how many it classified. It is the periodic pass described above: one
// conditional UPDATE, so two replicas running it at once each classify what the other has not.
// The partial index on unclassified alarms keeps a pass with nothing to do a cheap probe.
func (api *Api) ClassifyUnclassifiedAlarms(ctx context.Context) (int64, error) {
	res := api.RDB.Database.WithContext(ctx).Exec(classifyAlarmsSQL)
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected > 0 {
		log.Info().Int64("alarms", res.RowsAffected).Msg("Classified alarms whose position dependency was not known.")
	}
	return res.RowsAffected, nil
}

// classifyAlarm classifies one alarm, if it is still unclassified. A failure is logged and left
// to the periodic pass: the alarm stays NULL, which already withholds it from a caller without
// location:read, so nothing is shown that should not be while it waits.
//
// It binds the alarm's tenant from the context as well as its id: the alarm is the one the
// integrator just folded for that tenant, and the statement may not reach any other.
func (api *Api) classifyAlarm(ctx context.Context, id uint) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		log.Warn().Uint("alarm", id).Msg("Cannot classify an alarm without a tenant; it stays withheld until the next classification pass.")
		return
	}
	if err := api.RDB.Database.WithContext(ctx).Exec(classifyAlarmsSQL+" AND a.tenant_id = ? AND a.id = ?", tenant, id).Error; err != nil {
		log.Warn().Err(err).Uint("alarm", id).
			Msg("Could not classify an alarm's position dependency; it stays withheld until the next classification pass.")
	}
}

// normalizeClassifierSQL reduces a rendering of the classifier to a comparable form: the
// area's schema qualifier and the trailing semicolon dropped, whitespace collapsed. The migration
// names its tables schema-qualified, as migrations here do; the service relies on search_path.
func normalizeClassifierSQL(s string) string {
	s = strings.ReplaceAll(s, `"device-management".`, "")
	s = strings.TrimSuffix(strings.TrimSpace(s), ";")
	s = strings.ReplaceAll(s, `"alarms"`, "alarms")
	for _, t := range []string{"device_profiles", "detection_rules", "device_profile_versions"} {
		s = strings.ReplaceAll(s, `"`+t+`"`, t)
	}
	return strings.Join(strings.Fields(s), " ")
}
