// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The upgrade backfill of alarms.position_derived, on the real engine: it is jsonb and
// split_part SQL that SQLite cannot run, and migration-diff compares schema only, so it never
// sees which rows the backfill marks.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./model/... -run Backfill -v
package model

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

func TestAlarmPositionBackfillMarksGeofenceAlarms(t *testing.T) {
	mgr := newPostgresRdbManager(t)
	db := mgr.Database
	tenant := fmt.Sprintf("bf-%d", time.Now().UnixNano())
	other := tenant + "-other"

	profile := func(tenant string) uint {
		var id uint
		require.NoError(t, db.Raw(`INSERT INTO "device-management".device_profiles (tenant_id, token) VALUES (?, 'tracker') RETURNING id`, tenant).Scan(&id).Error)
		return id
	}
	p := profile(tenant)
	profile(other)
	for token, def := range map[string]string{
		"fence": `{"type":"threshold","when":{"cel":"!geo.inFence(\"yard\")"}}`,
		"temp":  `{"type":"threshold","when":{"metric":"t","op":">","threshold":1}}`,
	} {
		require.NoError(t, db.Exec(`INSERT INTO "device-management".detection_rules (tenant_id, token, device_profile_id, definition) VALUES (?, ?, ?, ?::jsonb)`,
			tenant, token, p, def).Error)
	}
	// A rule only a published version still holds (deleted from the draft since).
	require.NoError(t, db.Exec(`INSERT INTO "device-management".device_profile_versions (tenant_id, device_profile_id, version, snapshot) VALUES (?, ?, 1, ?::jsonb)`,
		tenant, p, `{"Rules":[{"Token":"old-fence","Definition":{"type":"duration","when":{"cel":"geo.inFence(\"gate\")"}}}]}`).Error)

	alarm := func(tenant, key, contributors string) {
		var c any
		if contributors != "" {
			c = contributors
		}
		require.NoError(t, db.Exec(`INSERT INTO "device-management".alarms (tenant_id, token, originator_type, originator_id, alarm_key, metric_key, state, severity, raised_time, contributors)
			VALUES (?, ?, 'device', 1, ?, '', 'ACTIVE', 'MAJOR', now(), ?::jsonb)`, tenant, tenant+"-"+key, key, c).Error)
	}
	alarm(tenant, "fence", `{"tracker/fence":{"tier":"MAJOR","active":true}}`)
	alarm(tenant, "temp", `{"tracker/temp":{"tier":"MAJOR","active":true}}`)
	alarm(tenant, "old-fence", `{"tracker/old-fence":{"active":false}}`)
	alarm(tenant, "mixed", `{"tracker/temp":{"active":true},"tracker/fence":{"active":false}}`)
	alarm(tenant, "legacy", "")
	alarm(tenant, "null-json", `null`)
	// Another tenant's profile of the same token has no geofence rule: its alarm is not marked
	// by this tenant's rules.
	alarm(other, "fence", `{"tracker/fence":{"active":true}}`)

	// Twice: the migration is re-runnable, and a replay changes nothing it already marked.
	for i := 0; i < 2; i++ {
		require.NoError(t, schema.NewAlarmPositionDerivedSchema().Migrate(db))
	}

	marked := func(tenant, key string) *bool {
		var v sql.NullBool
		require.NoError(t, db.Raw(`SELECT position_derived FROM "device-management".alarms WHERE tenant_id = ? AND alarm_key = ?`, tenant, key).Row().Scan(&v))
		if !v.Valid {
			return nil
		}
		return &v.Bool
	}
	for key, want := range map[string]bool{"fence": true, "old-fence": true, "mixed": true} {
		got := marked(tenant, key)
		require.True(t, got != nil && *got == want, "%s: position_derived = %v, want %v", key, got, want)
	}
	for _, key := range []string{"temp", "legacy", "null-json"} {
		require.Nil(t, marked(tenant, key), "%s must be left NULL", key)
	}
	require.Nil(t, marked(other, "fence"), "another tenant's rules must not mark an alarm")
}

// TestPositionWithholdingOnPostgres runs the withholding predicates on the real engine: the
// alarms predicate under the counts' JOIN, and the labels' NOT EXISTS against the schema-qualified
// alarms table, which SQLite (no schema prefix) cannot show is named correctly.
func TestPositionWithholdingOnPostgres(t *testing.T) {
	r := newScopeAlarmRig(t)
	require.NoError(t, RegisterPositionWithholding(r.api.RDB.Database))
	ctx := core.WithTenant(context.Background(), r.tenant)
	v := 1.0
	at := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, r.api.ApplyAlarmContributorEdge(ctx, r.devices["acme-pump"], "left-yard", "", "p/fence",
		AlarmEdgeRaised, string(AlarmSeverityMajor), &v, at, true))
	require.NoError(t, r.api.ApplyAlarmContributorEdge(ctx, r.devices["acme-pump"], "over-temp", "temp", "p/temp",
		AlarmEdgeRaised, string(AlarmSeverityMajor), &v, at, false))
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
}
