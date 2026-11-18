// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The classification of alarms.position_derived (the upgrade backfill, the periodic pass and an
// edge with no verdict), on the real engine: it is jsonb and
// split_part SQL that SQLite cannot run, and migration-diff compares schema only, so it never
// sees which rows the backfill marks.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./model/... -run Backfill -v
package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// positionRig seeds tenant with profile "tracker" (draft rules "fence", a geofence rule, and
// "temp", a threshold rule, plus a published version that alone still holds "old-fence", a
// geofence rule, and "old-temp", a threshold rule, both deleted from the draft since) and other
// with a profile of the same token whose "fence" rule tests no fence. The published version is the
// REAL ProfileSnapshot marshalled, so the classifier is tested against the keys the service
// actually stores.
func positionRig(t *testing.T, db *gorm.DB, tenant, other string) {
	t.Helper()
	profile := func(tenant string) uint {
		var id uint
		require.NoError(t, db.Raw(`INSERT INTO "device-management".device_profiles (tenant_id, token) VALUES (?, 'tracker') RETURNING id`, tenant).Scan(&id).Error)
		return id
	}
	rule := func(tenant string, profile uint, token, def string) {
		require.NoError(t, db.Exec(`INSERT INTO "device-management".detection_rules (tenant_id, token, device_profile_id, definition) VALUES (?, ?, ?, ?::jsonb)`,
			tenant, token, profile, def).Error)
	}
	p := profile(tenant)
	rule(tenant, p, "fence", `{"type":"threshold","when":{"cel":"!geo.inFence(\"yard\")"}}`)
	rule(tenant, p, "temp", `{"type":"threshold","when":{"metric":"t","op":">","threshold":1}}`)
	snapshot, err := json.Marshal(&ProfileSnapshot{Rules: []*DetectionRule{
		{TokenReference: rdb.TokenReference{Token: "old-fence"}, Definition: datatypes.JSON(`{"type":"duration","when":{"cel":"geo.inFence(\"gate\")"}}`)},
		{TokenReference: rdb.TokenReference{Token: "temp"}, Definition: datatypes.JSON(`{"type":"threshold","when":{"metric":"t","op":">","threshold":1}}`)},
		{TokenReference: rdb.TokenReference{Token: "old-temp"}, Definition: datatypes.JSON(`{"type":"threshold","when":{"metric":"t","op":"<","threshold":0}}`)},
	}})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`INSERT INTO "device-management".device_profile_versions (tenant_id, device_profile_id, version, snapshot) VALUES (?, ?, 1, ?::jsonb)`,
		tenant, p, string(snapshot)).Error)
	rule(other, profile(other), "fence", `{"type":"threshold","when":{"metric":"t","op":">","threshold":1}}`)
}

// rawAlarm inserts an alarm as a replica older than the column writes one: position_derived NULL.
func rawAlarm(t *testing.T, db *gorm.DB, tenant, key, contributors string) {
	t.Helper()
	var c any
	if contributors != "" {
		c = contributors
	}
	require.NoError(t, db.Exec(`INSERT INTO "device-management".alarms (tenant_id, token, originator_type, originator_id, alarm_key, metric_key, state, severity, raised_time, contributors)
		VALUES (?, ?, 'device', 1, ?, '', 'ACTIVE', 'MAJOR', now(), ?::jsonb)`, tenant, tenant+"-"+key, key, c).Error)
}

func positionOf(t *testing.T, db *gorm.DB, tenant, key string) *bool {
	t.Helper()
	var v sql.NullBool
	require.NoError(t, db.Raw(`SELECT position_derived FROM "device-management".alarms WHERE tenant_id = ? AND alarm_key = ?`, tenant, key).Row().Scan(&v))
	if !v.Valid {
		return nil
	}
	return &v.Bool
}

// seedPositionCases inserts one unclassified alarm per classification case and returns each
// key's expected verdict.
func seedPositionCases(t *testing.T, db *gorm.DB, tenant, other string) map[[2]string]bool {
	t.Helper()
	cases := map[[2]string]struct {
		contributors string
		want         bool
	}{
		{tenant, "fence"}: {`{"tracker/fence":{"tier":"MAJOR","active":true}}`, true},
		{tenant, "temp"}:  {`{"tracker/temp":{"tier":"MAJOR","active":true}}`, false},
		// Only the published version still holds the rule: read through the real snapshot keys.
		{tenant, "old-fence"}: {`{"tracker/old-fence":{"active":false}}`, true},
		// A threshold rule only the published version holds: found there, so NOT position-derived.
		// This is the case that proves the snapshot is read at all; a geofence rule found nowhere
		// is marked anyway (unresolvable), which would hide a wrong key.
		{tenant, "old-temp"}: {`{"tracker/old-temp":{"active":true}}`, false},
		{tenant, "mixed"}:    {`{"tracker/temp":{"active":true},"tracker/fence":{"active":false}}`, true},
		// The profile was deleted, taking its rules and versions with it: unresolvable, so
		// withheld (fail closed).
		{tenant, "deleted-profile"}: {`{"retired/fence":{"active":true}}`, true},
		// The profile exists but no draft rule or published version holds the rule any more.
		{tenant, "deleted-rule"}: {`{"tracker/gone":{"active":true}}`, true},
		// Not in the "{profile}/{rule}" form at all.
		{tenant, "unparseable"}: {`{"no-separator":{"active":true}}`, true},
		// No contributor set: the retired measurement evaluator, which had no geofence predicate.
		{tenant, "legacy"}:    {"", false},
		{tenant, "null-json"}: {`null`, false},
		{tenant, "empty"}:     {`{}`, false},
		// Another tenant's profile of the same token: judged by ITS rules, not this tenant's.
		{other, "fence"}: {`{"tracker/fence":{"active":true}}`, false},
	}
	want := map[[2]string]bool{}
	for k, c := range cases {
		rawAlarm(t, db, k[0], k[1], c.contributors)
		want[k] = c.want
	}
	return want
}

// TestAlarmPositionBackfillClassifiesEveryAlarm: the upgrade classifies every existing alarm,
// true or false, including the ones whose rule only a published version holds and the ones whose
// profile or rule was deleted (fail closed).
func TestAlarmPositionBackfillClassifiesEveryAlarm(t *testing.T) {
	db := newPostgresRdbManager(t).Database
	tenant := fmt.Sprintf("bf-%d", time.Now().UnixNano())
	other := tenant + "-other"
	positionRig(t, db, tenant, other)
	want := seedPositionCases(t, db, tenant, other)

	// Twice: the migration is re-runnable, and a replay changes nothing it already classified.
	for i := 0; i < 2; i++ {
		require.NoError(t, schema.NewAlarmPositionDerivedSchema().Migrate(db))
	}
	for k, w := range want {
		got := positionOf(t, db, k[0], k[1])
		require.True(t, got != nil && *got == w, "%s/%s: position_derived = %v, want %v", k[0], k[1], got, w)
	}
}

// TestUnclassifiedAlarmsAreClassifiedByThePeriodicPass: what a replica older than the column
// writes during a rolling upgrade (NULL) is classified by the service's own pass, with no second
// migration, by the same judgement as the backfill.
func TestUnclassifiedAlarmsAreClassifiedByThePeriodicPass(t *testing.T) {
	mgr := newPostgresRdbManager(t)
	db := mgr.Database
	tenant := fmt.Sprintf("cp-%d", time.Now().UnixNano())
	other := tenant + "-other"
	positionRig(t, db, tenant, other)
	want := seedPositionCases(t, db, tenant, other)

	n, err := NewApi(mgr).ClassifyUnclassifiedAlarms(context.Background())
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, int64(len(want)))
	for k, w := range want {
		got := positionOf(t, db, k[0], k[1])
		require.True(t, got != nil && *got == w, "%s/%s: position_derived = %v, want %v", k[0], k[1], got, w)
	}
}

// TestAnEdgeWithNoVerdictIsClassifiedAtOnce: an edge from an event-processing replica older than
// the flag carries no verdict. The integrator does not take that for false: it classifies the
// alarm from the contributing rule's stored definition in the same call.
func TestAnEdgeWithNoVerdictIsClassifiedAtOnce(t *testing.T) {
	mgr := newPostgresRdbManager(t)
	db := mgr.Database
	tenant := fmt.Sprintf("nv-%d", time.Now().UnixNano())
	positionRig(t, db, tenant, tenant+"-other")
	api := NewApi(mgr)
	ctx := core.WithTenant(context.Background(), tenant)
	v := 1.0
	at := time.Now().UTC().Truncate(time.Microsecond)
	edge := func(key, rule string, sec int, requiresPosition *bool) {
		require.NoError(t, api.ApplyAlarmContributorEdge(ctx, 1, key, "", rule, AlarmEdgeRaised,
			string(AlarmSeverityMajor), &v, at.Add(time.Duration(sec)*time.Second), requiresPosition))
	}
	edge("fence", "tracker/fence", 0, nil)
	edge("temp", "tracker/temp", 0, nil)
	edge("retired", "retired/fence", 0, nil)
	for key, w := range map[string]bool{"fence": true, "temp": false, "retired": true} {
		got := positionOf(t, db, tenant, key)
		require.True(t, got != nil && *got == w, "%s: position_derived = %v, want %v", key, got, w)
	}

	// An alarm classified false that an older producer's geofence rule then contributes to.
	no := false
	edge("shared", "tracker/temp", 0, &no)
	edge("shared", "tracker/fence", 1, nil)
	got := positionOf(t, db, tenant, "shared")
	require.True(t, got != nil && *got, "a geofence contributor with no verdict must mark the alarm, got %v", got)
}

// TestPositionWithholdingOnPostgres runs the withholding predicates on the real engine: the
// alarms predicate under the counts' JOIN, and the labels' NOT EXISTS against the schema-qualified
// alarms table, which SQLite (no schema prefix) cannot show is named correctly.
func TestPositionWithholdingOnPostgres(t *testing.T) {
	r := newScopeAlarmRig(t)
	require.NoError(t, RegisterPositionWithholding(r.api.RDB.Database))
	ctx := core.WithTenant(context.Background(), r.tenant)
	v := 1.0
	fenced := true
	at := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, r.api.ApplyAlarmContributorEdge(ctx, r.devices["acme-pump"], "left-yard", "", "p/fence",
		AlarmEdgeRaised, string(AlarmSeverityMajor), &v, at, &fenced))
	require.NoError(t, r.api.ApplyAlarmContributorEdge(ctx, r.devices["acme-pump"], "over-temp", "temp", "p/temp",
		AlarmEdgeRaised, string(AlarmSeverityMajor), &v, at, new(bool)))
	all, err := r.api.Alarms(r.operator, AlarmSearchCriteria{})
	require.NoError(t, err)
	require.Len(t, all.Results, 2)
	for _, a := range all.Results {
		_, _, err := r.api.SetAlarmDisposition(r.operator, AlarmDispositionRequest{Token: a.Token, Disposition: "TRUE_POSITIVE"}, nil)
		require.NoError(t, err)
	}

	viewer := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: r.tenant,
		Authorities: []string{string(auth.AlarmRead), string(auth.DeviceRead)}})
	res, err := r.api.Alarms(viewer, AlarmSearchCriteria{})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.Equal(t, "over-temp", res.Results[0].AlarmKey)
	require.EqualValues(t, 1, res.Pagination.TotalRecords)

	byType, err := r.api.AlarmCountsByDeviceType(viewer, AlarmCountCriteria{})
	require.NoError(t, err)
	n := 0
	for _, c := range byType {
		n += c.Count
	}
	require.Equal(t, 1, n, "counts by device type must leave the geofence alarm out: %+v", byType)

	labels, err := r.api.AlarmDispositions(viewer, AlarmDispositionSearchCriteria{})
	require.NoError(t, err)
	require.Len(t, labels.Results, 1)

	located := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: r.tenant,
		Authorities: []string{string(auth.AlarmRead), string(auth.LocationRead)}})
	labels, err = r.api.AlarmDispositions(located, AlarmDispositionSearchCriteria{})
	require.NoError(t, err)
	require.Len(t, labels.Results, 2)

	// A member limited to acme reads within its scope, and location:read is judged for it too:
	// with it both of acme's alarms, without it only the threshold one.
	scoped := func(authorities ...auth.Authority) context.Context {
		names := []string{string(auth.AlarmRead), string(auth.DeviceRead)}
		for _, a := range authorities {
			names = append(names, string(a))
		}
		return auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: r.tenant,
			ScopeAnchors: []string{"customer:acme"}, Authorities: names})
	}
	res, err = r.api.Alarms(scoped(auth.LocationRead), AlarmSearchCriteria{})
	require.NoError(t, err)
	require.Len(t, res.Results, 2, "a scoped member holding location:read reads its scope's geofence alarm")
	res, err = r.api.Alarms(scoped(), AlarmSearchCriteria{})
	require.NoError(t, err)
	require.Len(t, res.Results, 1, "a scoped member without location:read does not")
}

// olderReplicaFold folds a contributor into an alarm as a device-management replica of the previous
// release does: the contributor set and contributor_version under the version CAS, and nothing
// about the position classification, which that release does not know.
func olderReplicaFold(t *testing.T, db *gorm.DB, tenant, key, contributor string) {
	t.Helper()
	res := db.Exec(`UPDATE "device-management".alarms
		   SET contributors = contributors || ?::jsonb, contributor_version = contributor_version + 1, updated_at = now()
		 WHERE tenant_id = ? AND alarm_key = ? AND state = 'ACTIVE'`,
		fmt.Sprintf(`{%q:{"tier":"MAJOR","active":true}}`, contributor), tenant, key)
	require.NoError(t, res.Error)
	require.EqualValues(t, 1, res.RowsAffected)
}

func stampOf(t *testing.T, db *gorm.DB, tenant, key string) (version int64, stamp sql.NullInt64) {
	t.Helper()
	require.NoError(t, db.Raw(`SELECT contributor_version, position_classified_version FROM "device-management".alarms WHERE tenant_id = ? AND alarm_key = ?`,
		tenant, key).Row().Scan(&version, &stamp))
	return version, stamp
}

// TestAnOlderReplicasFoldIntoAFalseAlarmIsHealed: during a rolling upgrade a replica of the
// previous release folds a geofence rule's edge into an alarm this release already classified
// false. The fold is withheld the moment it is written (the stale-false trigger), and one
// classification pass marks it. Controls: an older replica's fold of a rule that tests no fence is
// withheld only until that pass and is then false again; this release's own false fold is not
// withheld at all; and with the trigger dropped the same geofence fold stays false through the
// pass, which is the defect this guards.
func TestAnOlderReplicasFoldIntoAFalseAlarmIsHealed(t *testing.T) {
	mgr := newPostgresRdbManager(t)
	db := mgr.Database
	tenant := fmt.Sprintf("or-%d", time.Now().UnixNano())
	positionRig(t, db, tenant, tenant+"-other")
	api := NewApi(mgr)
	ctx := core.WithTenant(context.Background(), tenant)
	v := 1.0
	at := time.Now().UTC().Truncate(time.Microsecond)
	no := false
	for _, key := range []string{"shared", "control", "unguarded"} {
		require.NoError(t, api.ApplyAlarmContributorEdge(ctx, 1, key, "", "tracker/temp", AlarmEdgeRaised,
			string(AlarmSeverityMajor), &v, at, &no))
		got := positionOf(t, db, tenant, key)
		require.True(t, got != nil && !*got, "%s: precondition: classified false, got %v", key, got)
	}

	olderReplicaFold(t, db, tenant, "shared", "tracker/fence")
	require.Nil(t, positionOf(t, db, tenant, "shared"), "the older replica's fold must be withheld as it is written")
	olderReplicaFold(t, db, tenant, "control", "tracker/old-temp")
	require.Nil(t, positionOf(t, db, tenant, "control"), "any older replica's fold re-opens a false: it cannot be judged in the write")

	_, err := api.ClassifyUnclassifiedAlarms(context.Background())
	require.NoError(t, err)
	got := positionOf(t, db, tenant, "shared")
	require.True(t, got != nil && *got, "one pass must mark the alarm a geofence rule now contributes to, got %v", got)
	got = positionOf(t, db, tenant, "control")
	require.True(t, got != nil && !*got, "an alarm no geofence rule contributes to is false again after the pass, got %v", got)
	for _, key := range []string{"shared", "control"} {
		version, stamp := stampOf(t, db, tenant, key)
		require.True(t, stamp.Valid && stamp.Int64 == version, "%s: the pass stamps what it decides: stamp %v at version %d", key, stamp, version)
	}

	// This release's own fold with a false verdict carries a current stamp: never withheld.
	require.NoError(t, api.ApplyAlarmContributorEdge(ctx, 1, "control", "", "tracker/temp", AlarmEdgeRaised,
		string(AlarmSeverityMajor), &v, at.Add(time.Second), &no))
	got = positionOf(t, db, tenant, "control")
	require.True(t, got != nil && !*got, "a current false folded with a false verdict stays readable, got %v", got)

	// Negative control: without the trigger the defect is back. Inside a transaction that is
	// rolled back, so the trigger is restored for everything else.
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	require.NoError(t, tx.Exec(`DROP TRIGGER alarm_position_stale ON "device-management".alarms`).Error)
	olderReplicaFold(t, tx, tenant, "unguarded", "tracker/fence")
	require.NoError(t, tx.Exec(classifyAlarmsSQL).Error)
	got = positionOf(t, tx, tenant, "unguarded")
	require.True(t, got != nil && !*got, "control: with no trigger a geofence fold by an older replica stays false through the pass, got %v", got)
}

// TestPositionStampMigrationReopensUnstampedFalse: the stamp migration re-decides every false
// verdict made before the stamp existed, which covers a fold an older replica made between the
// column's migration and this one, and is re-runnable.
func TestPositionStampMigrationReopensUnstampedFalse(t *testing.T) {
	db := newPostgresRdbManager(t).Database
	tenant := fmt.Sprintf("sm-%d", time.Now().UnixNano())
	positionRig(t, db, tenant, tenant+"-other")
	// Classified false before the stamp existed, then folded by an older replica: a fence rule now
	// contributes. And a false that is still right.
	rawAlarm(t, db, tenant, "gap", `{"tracker/temp":{"active":true},"tracker/fence":{"active":true}}`)
	rawAlarm(t, db, tenant, "still-false", `{"tracker/temp":{"active":true}}`)
	require.NoError(t, db.Exec(`UPDATE "device-management".alarms SET position_derived = false, position_classified_version = NULL WHERE tenant_id = ?`, tenant).Error)

	for i := 0; i < 2; i++ {
		require.NoError(t, schema.NewAlarmPositionStampSchema().Migrate(db))
	}
	got := positionOf(t, db, tenant, "gap")
	require.True(t, got != nil && *got, "gap: got %v", got)
	got = positionOf(t, db, tenant, "still-false")
	require.True(t, got != nil && !*got, "still-false: got %v", got)
	for _, key := range []string{"gap", "still-false"} {
		version, stamp := stampOf(t, db, tenant, key)
		require.True(t, stamp.Valid && stamp.Int64 == version, "%s: stamp %v at version %d", key, stamp, version)
	}
}
