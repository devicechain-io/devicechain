// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// positionStreamApi is an Api over an in-memory database holding two alarms of tenant t1: one a
// geofence rule raised ("fence-alarm", position-derived) and one a threshold rule raised
// ("temp-alarm"). The withholding callback is registered, as the service registers it.
func positionStreamApi(t *testing.T) *model.Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("tenant scoping: %v", err)
	}
	if err := model.RegisterPositionWithholding(db); err != nil {
		t.Fatalf("position withholding: %v", err)
	}
	if err := db.AutoMigrate(&model.Alarm{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "t1")
	derived := true
	for i, a := range []*model.Alarm{
		{TokenReference: rdb.TokenReference{Token: "fence-alarm"}, AlarmKey: "left-yard", PositionDerived: &derived},
		{TokenReference: rdb.TokenReference{Token: "temp-alarm"}, AlarmKey: "over-temp"},
	} {
		a.OriginatorType, a.OriginatorId = "device", uint(i+1)
		a.State, a.Severity, a.RaisedTime = string(model.AlarmStateActive), string(model.AlarmSeverityMajor), time.Unix(10, 0)
		if err := db.WithContext(ctx).Create(a).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return model.NewApi(&rdb.RdbManager{Database: db})
}

func subscriber(authorities ...auth.Authority) context.Context {
	names := make([]string, 0, len(authorities))
	for _, a := range authorities {
		names = append(names, string(a))
	}
	return auth.WithClaims(core.WithTenant(context.Background(), "t1"),
		&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "t1", Authorities: names})
}

// TestAlarmStreamWithholdsGeofenceAlarmsWithoutLocationRead: the live alarm stream does not push
// an event of an alarm a geofence rule raised to a subscriber without location:read, the same
// alarm the queries withhold from it and the same rule whose detections detectionStream
// withholds. A subscriber holding location:read receives both.
func TestAlarmStreamWithholdsGeofenceAlarmsWithoutLocationRead(t *testing.T) {
	api := positionStreamApi(t)
	fence := &model.AlarmStateChangeEvent{AlarmToken: "fence-alarm", OriginatorType: "device", OriginatorId: 1}
	temp := &model.AlarmStateChangeEvent{AlarmToken: "temp-alarm", OriginatorType: "device", OriginatorId: 2}

	viewer := alarmEventFilter{position: positionGateFor(subscriber(auth.AlarmRead, auth.DeviceRead), api.AlarmPositionDerived)}
	if viewer.position == nil {
		t.Fatalf("a subscriber without location:read must get a position gate")
	}
	if viewer.matches(fence) {
		t.Fatalf("the geofence alarm's event must be withheld from a subscriber without location:read")
	}
	if !viewer.matches(temp) {
		t.Fatalf("the threshold alarm's event must still be delivered")
	}
	// The withheld verdict is remembered, and still withholds.
	if viewer.matches(fence) {
		t.Fatalf("a remembered verdict must keep withholding")
	}

	locator := alarmEventFilter{position: positionGateFor(subscriber(auth.AlarmRead, auth.LocationRead), api.AlarmPositionDerived)}
	if locator.position != nil {
		t.Fatalf("a subscriber holding location:read needs no gate")
	}
	if !locator.matches(fence) || !locator.matches(temp) {
		t.Fatalf("a subscriber holding location:read must receive both events")
	}
}

// TestAlarmPositionGateFailsClosed: an alarm the gate cannot read, or that no longer exists, is
// withheld; neither verdict is remembered, so the alarm is asked about again next time; a
// visible alarm is asked about again too, because a geofence rule may contribute to it later.
func TestAlarmPositionGateFailsClosed(t *testing.T) {
	answers := map[string]struct {
		derived, found bool
		err            error
	}{
		"broken": {err: errors.New("store down")},
		"gone":   {},
		"plain":  {found: true},
	}
	asked := map[string]int{}
	g := &positionGate{derived: func(token string) (bool, bool, error) {
		asked[token]++
		a := answers[token]
		return a.derived, a.found, a.err
	}}
	for i := 0; i < 2; i++ {
		if g.allows("broken") || g.allows("gone") {
			t.Fatalf("an unreadable or missing alarm must be withheld")
		}
		if !g.allows("plain") {
			t.Fatalf("an alarm no geofence rule raised must be delivered")
		}
	}
	for _, token := range []string{"broken", "gone", "plain"} {
		if asked[token] != 2 {
			t.Fatalf("%s: asked %d times, want 2 (only a withheld verdict is remembered)", token, asked[token])
		}
	}
}

// TestAlarmPositionGateBound: the gate's memory of withheld alarms is bounded, and an evicted
// alarm is read again rather than delivered.
func TestAlarmPositionGateBound(t *testing.T) {
	g := &positionGate{derived: func(string) (bool, bool, error) { return true, true, nil }}
	for i := 0; i < maxWithheldAlarms+10; i++ {
		if g.allows(string(rune('a'+i%26)) + time.Duration(i).String()) {
			t.Fatalf("a position-derived alarm must be withheld")
		}
	}
	if len(g.withheld) != maxWithheldAlarms || len(g.order) != maxWithheldAlarms {
		t.Fatalf("memory = %d/%d, want the bound %d", len(g.withheld), len(g.order), maxWithheldAlarms)
	}
}

// TestAlarmStreamResolverWithholdsGeofenceAlarms drives AlarmStream itself through the shared
// live hub: one upstream feed carries an event of a geofence alarm and one of a threshold alarm,
// and of two subscribers on it, the one without location:read receives only the threshold
// alarm's event while the one holding it receives both.
func TestAlarmStreamResolverWithholdsGeofenceAlarms(t *testing.T) {
	f := newScopeFixture(t)
	require.NoError(t, model.RegisterPositionWithholding(f.api.RDB.Database))
	acmeID, err := f.api.ResolveEntityToken(f.operator(), "device", "acme-pump")
	require.NoError(t, err)
	tenantCtx := core.WithTenant(context.Background(), scopeTenant)
	at := time.Unix(100, 0).UTC()
	derived := true
	for _, a := range []*model.Alarm{
		{TokenReference: rdb.TokenReference{Token: "fence-alarm"}, AlarmKey: "left-yard", PositionDerived: &derived},
		{TokenReference: rdb.TokenReference{Token: "temp-alarm"}, AlarmKey: "over-temp"},
	} {
		a.OriginatorType, a.OriginatorId, a.ScopeAnchors = "device", acmeID, rdb.Anchors{"customer:acme"}
		a.State, a.Severity, a.RaisedTime = string(model.AlarmStateActive), string(model.AlarmSeverityMajor), at
		require.NoError(t, f.api.RDB.Database.WithContext(tenantCtx).Create(a).Error)
	}
	alarms, err := f.api.Alarms(f.operator(), model.AlarmSearchCriteria{})
	require.NoError(t, err)
	require.Len(t, alarms.Results, 2)

	upstream := make(chan messaging.Message, 4)
	oldOpen := alarmLiveOpener
	alarmLiveOpener = func(*messaging.NatsManager) messaging.LiveOpener {
		return func(context.Context, string) (<-chan messaging.Message, error) { return upstream, nil }
	}
	defer func() { alarmLiveOpener = oldOpen }()
	nmgr := &messaging.NatsManager{}

	subscribe := func(base context.Context) <-chan *AlarmEventResolver {
		ctx, cancel := context.WithCancel(base)
		t.Cleanup(cancel)
		ch, err := (&SchemaResolver{}).AlarmStream(context.WithValue(ctx, gqlcore.ContextNatsKey, nmgr), alarmStreamArgs{})
		require.NoError(t, err)
		return ch
	}
	viewer := subscribe(f.member("customer:acme"))
	locator := subscribe(f.sweepMember("customer:acme"))

	for i := range alarms.Results {
		a := &alarms.Results[i]
		data, err := dmproto.MarshalAlarmStateChangeEvent(&model.AlarmStateChangeEvent{
			EventType: model.AlarmEventRaised, AlarmToken: a.Token, OriginatorType: "device", OriginatorId: acmeID,
			AlarmKey: a.AlarmKey, State: a.State, Severity: a.Severity, ScopeAnchors: []string{"customer:acme"},
			RaisedTime: at, OccurredTime: at})
		require.NoError(t, err)
		upstream <- messaging.Message{Value: data}
	}
	close(upstream)

	drain := func(ch <-chan *AlarmEventResolver) []string {
		var keys []string
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return keys
				}
				keys = append(keys, ev.AlarmKey())
			case <-time.After(5 * time.Second):
				t.Fatal("the stream did not close")
			}
		}
	}
	require.Equal(t, []string{"over-temp"}, drain(viewer), "a subscriber without location:read must not receive the geofence alarm")
	require.ElementsMatch(t, []string{"left-yard", "over-temp"}, drain(locator))
}
