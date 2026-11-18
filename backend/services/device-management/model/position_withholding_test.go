// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// newWithholdingTestApi is the contributor test database with the position-withholding callback
// registered, as buildApis registers it in the service, and the disposition table added.
func newWithholdingTestApi(t *testing.T) *Api {
	t.Helper()
	api, _ := newContributorTestApi(t)
	if err := api.RDB.Database.AutoMigrate(&AlarmDisposition{}); err != nil {
		t.Fatalf("migrate dispositions: %v", err)
	}
	if err := RegisterPositionWithholding(api.RDB.Database); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Registering twice (a second buildApis over the same handle) installs nothing more.
	if err := RegisterPositionWithholding(api.RDB.Database); err != nil {
		t.Fatalf("register again: %v", err)
	}
	return api
}

// caller is a request context for a member of the test tenant holding authorities.
func caller(authorities ...auth.Authority) context.Context {
	names := make([]string, 0, len(authorities))
	for _, a := range authorities {
		names = append(names, string(a))
	}
	return auth.WithClaims(core.WithTenant(context.Background(), "t1"),
		&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "t1", Authorities: names})
}

// raiseWithPosition drives the integrator as the raise-alarm consumer does: the tenant and no caller.
func raiseWithPosition(t *testing.T, api *Api, key, rule string, sec int, requiresPosition *bool) {
	t.Helper()
	v := 1.0
	ctx := core.WithTenant(context.Background(), "t1")
	if err := api.ApplyAlarmContributorEdge(ctx, dev, key, "temperature", rule, AlarmEdgeRaised, "MAJOR", &v, tsec(sec), requiresPosition); err != nil {
		t.Fatalf("raise %s: %v", key, err)
	}
}

func alarmKeys(alarms []Alarm) map[string]bool {
	out := map[string]bool{}
	for _, a := range alarms {
		out[a.AlarmKey] = true
	}
	return out
}

// TestGeofenceAlarmsWithheldWithoutLocationRead: a caller holding alarm:read but not location:read
// (the read-only viewer baseline) reads none of an alarm a geofence rule raised, on any alarm read
// path: the search, the token lookup behind acknowledge/clear/label, the counts and the labels. The
// threshold alarm beside it is untouched. A caller holding location:read, and the integrator (no
// caller), read both.
func TestGeofenceAlarmsWithheldWithoutLocationRead(t *testing.T) {
	api := newWithholdingTestApi(t)
	raiseWithPosition(t, api, "left-yard", "p/fence", 10, verdict(true))
	raiseWithPosition(t, api, "over-temp", "p/temp", 10, verdict(false))

	locator := caller(auth.AlarmRead, auth.AlarmWrite, auth.LocationRead)
	fence := loadAlarm(t, api, locator, "left-yard")
	temp := loadAlarm(t, api, locator, "over-temp")
	if fence == nil || temp == nil {
		t.Fatalf("a caller with location:read must read both alarms: fence=%v temp=%v", fence, temp)
	}
	if fence.PositionDerived == nil || !*fence.PositionDerived || temp.PositionDerived == nil || *temp.PositionDerived {
		t.Fatalf("the integrator must mark only the geofence alarm: fence=%v temp=%v", fence.PositionDerived, temp.PositionDerived)
	}
	for _, token := range []string{fence.Token, temp.Token} {
		if _, _, err := api.SetAlarmDisposition(locator, AlarmDispositionRequest{Token: token, Disposition: "TRUE_POSITIVE"}, nil); err != nil {
			t.Fatalf("label %s: %v", token, err)
		}
	}

	viewer := caller(auth.AlarmRead, auth.AlarmWrite, auth.DeviceRead)

	res, err := api.Alarms(viewer, AlarmSearchCriteria{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := alarmKeys(res.Results); !got["over-temp"] || got["left-yard"] || res.Pagination.TotalRecords != 1 {
		t.Fatalf("search must withhold the geofence alarm, total included: %v total=%d", got, res.Pagination.TotalRecords)
	}

	byToken, err := api.AlarmsByToken(viewer, []string{fence.Token, temp.Token})
	if err != nil {
		t.Fatalf("by token: %v", err)
	}
	if len(byToken) != 1 || byToken[0].Token != temp.Token {
		t.Fatalf("token lookup must withhold the geofence alarm: %d found", len(byToken))
	}
	byId, err := api.AlarmsById(viewer, []uint{fence.ID})
	if err != nil {
		t.Fatalf("by id: %v", err)
	}
	if len(byId) != 0 {
		t.Fatalf("id lookup must withhold the geofence alarm")
	}

	if _, err := api.AcknowledgeAlarm(viewer, fence.Token, nil); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("acknowledging a withheld alarm must answer not found, got %v", err)
	}
	if _, err := api.ClearAlarm(viewer, fence.Token); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("clearing a withheld alarm must answer not found, got %v", err)
	}

	counts, err := api.AlarmCountsBySeverity(viewer, AlarmCountCriteria{}, nil)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	total := int64(0)
	for _, c := range counts {
		total += int64(c.Count)
	}
	if total != 1 {
		t.Fatalf("counts must not count the geofence alarm: %+v", counts)
	}

	labels, err := api.AlarmDispositions(viewer, AlarmDispositionSearchCriteria{})
	if err != nil {
		t.Fatalf("labels: %v", err)
	}
	if len(labels.Results) != 1 || labels.Results[0].AlarmToken != temp.Token {
		t.Fatalf("labels must withhold the geofence alarm's cycles: %+v", labels.Results)
	}

	// The integrator (no caller) and a caller holding location:read still see both.
	all, err := api.Alarms(core.WithTenant(context.Background(), "t1"), AlarmSearchCriteria{})
	if err != nil || len(all.Results) != 2 {
		t.Fatalf("a context with no caller must read both alarms: %v %v", all, err)
	}
	located, err := api.Alarms(locator, AlarmSearchCriteria{})
	if err != nil || len(located.Results) != 2 {
		t.Fatalf("a caller with location:read must read both alarms: %v %v", located, err)
	}
	labelsLocated, err := api.AlarmDispositions(locator, AlarmDispositionSearchCriteria{})
	if err != nil || len(labelsLocated.Results) != 2 {
		t.Fatalf("a caller with location:read must read both labels: %v %v", labelsLocated, err)
	}
}

// TestPositionDerivedIsSticky: an alarm a threshold rule raised becomes position-derived when a
// geofence rule contributes to the same key, and stays so after that rule resolves: the alarm's
// history now carries the fence crossing.
func TestPositionDerivedIsSticky(t *testing.T) {
	api := newWithholdingTestApi(t)
	raiseWithPosition(t, api, "k", "p/temp", 10, verdict(false))
	viewer := caller(auth.AlarmRead)
	if a := loadAlarm(t, api, viewer, "k"); a == nil {
		t.Fatalf("a threshold alarm must be readable without location:read")
	}
	raiseWithPosition(t, api, "k", "p/fence", 11, verdict(true))
	ctx := core.WithTenant(context.Background(), "t1")
	if err := api.ApplyAlarmContributorEdge(ctx, dev, "k", "temperature", "p/fence", AlarmEdgeResolved, "", nil, tsec(12), verdict(true)); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if a := loadAlarm(t, api, viewer, "k"); a != nil {
		t.Fatalf("once a geofence rule contributed, the alarm must stay withheld")
	}
	a := loadAlarm(t, api, ctx, "k")
	derived, found, err := api.AlarmPositionDerived(viewer, a.Token)
	if err != nil || !found || derived == nil || !*derived {
		t.Fatalf("AlarmPositionDerived must read the flag regardless of the caller: derived=%v found=%v err=%v", derived, found, err)
	}
}

func verdict(b bool) *bool { return &b }

// TestUnclassifiedAlarmsAreWithheld: an alarm whose position dependency is not known yet (NULL:
// written by a replica older than the column, or folded from an edge whose producer predates the
// flag) is withheld from a caller without location:read until it is classified, as a
// position-derived one is: fail closed. The classifier is Postgres SQL, so on this database it
// fails and the alarm stays unclassified, which is exactly the state under test.
func TestUnclassifiedAlarmsAreWithheld(t *testing.T) {
	api := newWithholdingTestApi(t)
	raiseWithPosition(t, api, "old-producer", "p/unknown", 10, nil)
	raiseWithPosition(t, api, "over-temp", "p/temp", 10, verdict(false))
	tenantCtx := core.WithTenant(context.Background(), "t1")
	if a := loadAlarm(t, api, tenantCtx, "old-producer"); a == nil || a.PositionDerived != nil {
		t.Fatalf("an edge with no verdict must leave a new alarm unclassified: %+v", a)
	}
	viewer := caller(auth.AlarmRead)
	if a := loadAlarm(t, api, viewer, "old-producer"); a != nil {
		t.Fatalf("an unclassified alarm must be withheld from a caller without location:read")
	}
	if a := loadAlarm(t, api, viewer, "over-temp"); a == nil {
		t.Fatalf("an alarm classified not position-derived must be readable")
	}
	if a := loadAlarm(t, api, caller(auth.AlarmRead, auth.LocationRead), "old-producer"); a == nil {
		t.Fatalf("a caller with location:read reads an unclassified alarm")
	}

	// An edge with no verdict on an alarm classified false unclassifies it: the older producer's
	// rule may test a fence. An alarm already marked stays marked.
	raiseWithPosition(t, api, "over-temp", "p/other", 11, nil)
	if a := loadAlarm(t, api, tenantCtx, "over-temp"); a.PositionDerived != nil {
		t.Fatalf("an unknown contributor must unclassify an alarm classified false, got %v", *a.PositionDerived)
	}
	raiseWithPosition(t, api, "fence", "p/fence", 10, verdict(true))
	raiseWithPosition(t, api, "fence", "p/other", 11, nil)
	if a := loadAlarm(t, api, tenantCtx, "fence"); a.PositionDerived == nil || !*a.PositionDerived {
		t.Fatalf("an alarm already position-derived must stay so")
	}
}

// TestClassifierMatchesTheMigration: the service's classifier and the migration's literal copy
// are one statement. The migration names the area's tables schema-qualified and the service relies
// on search_path; with that qualifier removed they must agree, word for word. The copy compared is
// the stamping one (migration_alarm_position_stamp.go), the latest the service runs. The control: a
// copy with any token changed must NOT agree, so the comparison is not vacuous, and the earlier,
// unstamped copy must not agree either, so a service that dropped the stamp would be seen.
func TestClassifierMatchesTheMigration(t *testing.T) {
	service, migration := normalizeClassifierSQL(classifyAlarmsSQL), normalizeClassifierSQL(schema.AlarmPositionClassifyStampedSQL)
	if service != migration {
		t.Fatalf("the classifier and the migration diverged:\nservice:   %s\nmigration: %s", service, migration)
	}
	if !strings.Contains(migration, "v.snapshot -> 'rules'") || !strings.Contains(migration, "s.found = 0") ||
		!strings.Contains(migration, "position_classified_version = a.contributor_version") {
		t.Fatalf("the comparison must cover the statement's body: %s", migration)
	}
	altered := strings.Replace(schema.AlarmPositionClassifyStampedSQL, "s.found = 0", "s.found = 1", 1)
	if normalizeClassifierSQL(altered) == service {
		t.Fatalf("normalizing must not erase a real difference")
	}
	if normalizeClassifierSQL(schema.AlarmPositionClassifySQL) == service {
		t.Fatalf("the service must run the stamping classifier, not the one the column's migration ran")
	}
}

// TestClassificationIsStampedWithItsContributorVersion: every verdict the integrator writes says
// which contributor set it was decided for, a false verdict on the edge carries a current stamp
// forward with the fold, and a stamp that is already behind (an older replica folded in between,
// bumping the version without knowing the stamp) stays behind rather than being refreshed by the
// next fold, so the database's stale-false trigger still sees it.
func TestClassificationIsStampedWithItsContributorVersion(t *testing.T) {
	api := newWithholdingTestApi(t)
	ctx := core.WithTenant(context.Background(), "t1")
	stampOf := func(key string) (uint, *uint) {
		t.Helper()
		a := loadAlarm(t, api, ctx, key)
		return a.ContributorVersion, a.PositionClassifiedVersion
	}
	requireStamp := func(key string, want uint) {
		t.Helper()
		v, s := stampOf(key)
		if s == nil || *s != want {
			t.Fatalf("%s: stamp = %v at contributor version %d, want %d", key, s, v, want)
		}
	}

	raiseWithPosition(t, api, "k", "p/temp", 10, verdict(false))
	requireStamp("k", 1)
	raiseWithPosition(t, api, "k", "p/temp2", 11, verdict(false))
	requireStamp("k", 2)

	// What a replica older than the stamp writes on a fold: the set and the version, nothing else.
	if err := api.RDB.DB(ctx).Exec(`UPDATE alarms SET contributor_version = contributor_version + 1 WHERE alarm_key = 'k'`).Error; err != nil {
		t.Fatalf("simulate an older replica's fold: %v", err)
	}
	raiseWithPosition(t, api, "k", "p/temp3", 12, verdict(false))
	if v, s := stampOf("k"); s == nil || *s != 2 || v != 4 {
		t.Fatalf("a stamp behind the version must stay behind: stamp %v at version %d", s, v)
	}

	raiseWithPosition(t, api, "fence", "p/fence", 10, verdict(true))
	requireStamp("fence", 1)
	raiseWithPosition(t, api, "fence", "p/temp", 11, verdict(true))
	requireStamp("fence", 2)
}
