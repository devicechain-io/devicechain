// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Slice S2 of the customer-scoped area dashboards work: an AREA or ASSET anchor filter
// answers a scoped caller with its own devices' rows in that place - not nothing (the old
// silent empty), and not the other customers' rows (the leak a naive fix would add).
//
// Every assertion is on VALUES, not on "no Globex rows": Acme's reading is 1 and Globex's
// is 10, so a read that returned both would show 11 and a read that returned neither
// would show nothing.

const areaTenant = "tenant1"

// areaWorld is the store the tests below read. Time runs down the list:
//
//	acme-pump   -> customer:acme   + area:floor-shared + area:floor-acme   + asset:press   value 1
//	globex-pump -> customer:globex + area:floor-shared + area:floor-globex + asset:press   value 10
//	mover       -> customer:acme   + area:floor-a                                          value 100
//	mover       -> customer:acme   + area:floor-b                                          value 200
//	sold        -> customer:acme   + area:floor-shared                                     value 1000
//	sold        -> customer:globex + area:floor-shared                                     value 2000
//
// mover changed floors and sold changed owners: each event keeps the anchors it was
// recorded with, which is the whole of the history rule. Every reading is recorded as a
// measurement, a location fix and an alert, all on the same anchors.
type areaWorld struct {
	api *model.Api
}

var worldStart = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func newAreaWorld(t *testing.T) areaWorld {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.Event{}, &model.EventAnchor{},
		&model.MeasurementEvent{}, &model.LocationEvent{}, &model.AlertEvent{}))
	w := areaWorld{api: model.NewApi(&rdb.RdbManager{Database: db})}
	record := func(device string, at time.Time, value float64, anchors ...[2]string) {
		write := core.WithTenant(context.Background(), areaTenant)
		for _, kind := range []eventtype.EventType{eventtype.Measurement, eventtype.Location, eventtype.Alert} {
			e := model.Event{DeviceToken: device, EventType: kind, OccurredTime: at, ProcessedTime: at, Source: "mqtt"}
			e.EventId = model.DeriveEventId(areaTenant, &e, nil)
			require.NoError(t, db.WithContext(write).Create(&e).Error)
			for _, a := range anchors {
				require.NoError(t, db.WithContext(write).Create(&model.EventAnchor{EventId: e.EventId,
					DeviceToken: device, EventType: kind, OccurredTime: at, AnchorType: a[0], AnchorToken: a[1]}).Error)
			}
			payload := []byte(device + at.String())
			switch kind {
			case eventtype.Measurement:
				require.NoError(t, db.WithContext(write).Create(&model.MeasurementEvent{EventId: e.EventId,
					PayloadId: payload, DeviceToken: device, EventType: kind, OccurredTime: at, Name: "temp",
					Value: sql.NullFloat64{Float64: value, Valid: true}}).Error)
			case eventtype.Location:
				require.NoError(t, db.WithContext(write).Create(&model.LocationEvent{EventId: e.EventId,
					PayloadId: payload, DeviceToken: device, EventType: kind, OccurredTime: at,
					Latitude: sql.NullFloat64{Float64: value, Valid: true}}).Error)
			case eventtype.Alert:
				require.NoError(t, db.WithContext(write).Create(&model.AlertEvent{EventId: e.EventId,
					PayloadId: payload, DeviceToken: device, EventType: kind, OccurredTime: at, Type: "door",
					Level: 1, Message: device}).Error)
			}
		}
	}
	cust := func(c string) [2]string { return [2]string{"customer", c} }
	area := func(a string) [2]string { return [2]string{"area", a} }
	press := [2]string{"asset", "press"}
	record("acme-pump", worldStart, 1, cust("acme"), area("floor-shared"), area("floor-acme"), press)
	record("globex-pump", worldStart, 10, cust("globex"), area("floor-shared"), area("floor-globex"), press)
	record("mover", worldStart.Add(1*time.Hour), 100, cust("acme"), area("floor-a"))
	record("mover", worldStart.Add(2*time.Hour), 200, cust("acme"), area("floor-b"))
	record("sold", worldStart.Add(3*time.Hour), 1000, cust("acme"), area("floor-shared"))
	record("sold", worldStart.Add(4*time.Hour), 2000, cust("globex"), area("floor-shared"))
	return w
}

func (w areaWorld) ctx(scope ...string) context.Context {
	var anchors []string // nil: an unscoped caller
	if len(scope) > 0 {
		anchors = scope
	}
	return scopedCtx(w.api, areaTenant, anchors, string(auth.EventRead), string(auth.LocationRead))
}

// seen is what the four paged reads return for one anchor: the base events counted, the
// measurement VALUES, the location LATITUDES and the alert DEVICES (the message carries
// the device), sorted so the comparison is order-free.
type seen struct {
	events    int
	values    []float64
	latitudes []float64
	alerts    []string
}

func (w areaWorld) read(ctx context.Context, anchorType, token string) (seen, error) {
	crit := model.EventSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100}}
	if anchorType != "" {
		crit.AnchorType, crit.AnchorToken = &anchorType, &token
	}
	out := seen{values: []float64{}, latitudes: []float64{}, alerts: []string{}}
	events, err := w.api.Events(ctx, crit)
	if err != nil {
		return out, err
	}
	out.events = len(events.Results)
	ms, err := w.api.MeasurementEvents(ctx, crit)
	if err != nil {
		return out, err
	}
	for _, m := range ms.Results {
		out.values = append(out.values, m.Value.Float64)
	}
	ls, err := w.api.LocationEvents(ctx, crit)
	if err != nil {
		return out, err
	}
	for _, l := range ls.Results {
		out.latitudes = append(out.latitudes, l.Latitude.Float64)
	}
	as, err := w.api.AlertEvents(ctx, crit)
	if err != nil {
		return out, err
	}
	for _, a := range as.Results {
		out.alerts = append(out.alerts, a.Message)
	}
	sort.Float64s(out.values)
	sort.Float64s(out.latitudes)
	sort.Strings(out.alerts)
	return out, nil
}

func (w areaWorld) mustRead(t *testing.T, ctx context.Context, anchorType, token string) seen {
	t.Helper()
	s, err := w.read(ctx, anchorType, token)
	require.NoError(t, err)
	return s
}

// The central acceptance: on the shared area each customer reads its OWN devices' values
// on every read, and the unscoped caller (the positive control, whose answer is
// unchanged) reads everyone's.
func TestAScopedCallerOnASharedAreaSeesItsOwnValuesOnEveryRead(t *testing.T) {
	w := newAreaWorld(t)

	// acme on floor-shared: acme-pump (1) and the Acme period of "sold" (1000).
	acme := w.mustRead(t, w.ctx("customer:acme"), "area", "floor-shared")
	assert.Equal(t, []float64{1, 1000}, acme.values, "measurementEvents: Acme's values, never the sum with Globex's")
	assert.Equal(t, []float64{1, 1000}, acme.latitudes, "locationEvents: Acme's fixes")
	assert.Equal(t, []string{"acme-pump", "sold"}, acme.alerts, "alertEvents: Acme's devices")
	assert.Equal(t, 6, acme.events, "events: three kinds of event for each of two readings")

	globex := w.mustRead(t, w.ctx("customer:globex"), "area", "floor-shared")
	assert.Equal(t, []float64{10, 2000}, globex.values, "measurementEvents: Globex's values")
	assert.Equal(t, []float64{10, 2000}, globex.latitudes)
	assert.Equal(t, []string{"globex-pump", "sold"}, globex.alerts)
	assert.Equal(t, 6, globex.events)

	// Control: the unscoped caller is unchanged and sees all of it.
	all := w.mustRead(t, w.ctx(), "area", "floor-shared")
	assert.Equal(t, []float64{1, 10, 1000, 2000}, all.values, "control: unscoped sees every customer")
	assert.Equal(t, 12, all.events, "control: unscoped events on the shared area")
}

// The same for an ASSET anchor: acme-pump and globex-pump sit on asset:press.
func TestAScopedCallerOnASharedAssetSeesItsOwnValues(t *testing.T) {
	w := newAreaWorld(t)
	assert.Equal(t, []float64{1}, w.mustRead(t, w.ctx("customer:acme"), "asset", "press").values)
	assert.Equal(t, []float64{10}, w.mustRead(t, w.ctx("customer:globex"), "asset", "press").values)
	assert.Equal(t, []float64{1, 10}, w.mustRead(t, w.ctx(), "asset", "press").values, "control: unscoped")
}

// A scope that owns no event (here a group no event carries) answers an area filter with
// an empty page, not an error and not the whole area.
func TestAGroupScopeOwningNothingIsAnsweredEmptyOnASharedArea(t *testing.T) {
	w := newAreaWorld(t)
	got := w.mustRead(t, w.ctx("group:staff"), "area", "floor-shared")
	assert.Equal(t, 0, got.events)
	assert.Empty(t, got.values)
}

// An area none of Acme's devices is in answers empty, not an error: it discloses nothing
// the caller does not already know about its own devices.
func TestAScopedCallerFilteringByAnAreaWithNoneOfItsDevicesGetsEmpty(t *testing.T) {
	w := newAreaWorld(t)
	got, err := w.read(w.ctx("customer:acme"), "area", "floor-globex")
	require.NoError(t, err, "an area outside the caller's reach is an empty answer, not a refusal")
	assert.Equal(t, 0, got.events)
	assert.Empty(t, got.values)
	assert.Empty(t, w.mustRead(t, w.ctx("customer:acme"), "area", "no-such-area").values)
	// Control: the unscoped caller finds Globex's floor, so empty above is the scope's doing.
	assert.Equal(t, []float64{10}, w.mustRead(t, w.ctx(), "area", "floor-globex").values)
}

// D3, the placement period: a device that moved floors keeps its history under the floor
// it was on when each event happened.
func TestAMovedDeviceKeepsItsHistoryUnderTheFloorItWasOn(t *testing.T) {
	w := newAreaWorld(t)
	acme := w.ctx("customer:acme")
	assert.Equal(t, []float64{100}, w.mustRead(t, acme, "area", "floor-a").values, "floor A shows the first reading only")
	assert.Equal(t, []float64{200}, w.mustRead(t, acme, "area", "floor-b").values, "floor B shows the second only")
	assert.Empty(t, w.mustRead(t, w.ctx("customer:globex"), "area", "floor-a").values, "another customer never sees it")
}

// D3, the ownership period: a device sold from Acme to Globex. On the shared floor Acme
// sees the reading from its ownership, Globex the one from its own, and neither the other.
func TestASoldDeviceContributesToEachOwnerForItsOwnPeriod(t *testing.T) {
	w := newAreaWorld(t)
	acme := w.mustRead(t, w.ctx("customer:acme"), "area", "floor-shared").values
	assert.Contains(t, acme, 1000.0)
	assert.NotContains(t, acme, 2000.0, "Acme must not see the post-sale reading")
	globex := w.mustRead(t, w.ctx("customer:globex"), "area", "floor-shared").values
	assert.Contains(t, globex, 2000.0)
	assert.NotContains(t, globex, 1000.0, "Globex must not see the pre-sale reading")
}

// A customer or group filter outside the scope is refused from the claims alone, on all
// five reads, and an existing customer and a nonexistent one are refused identically. An
// unscoped caller may name any customer, and a scoped one its own.
func TestAScopedCallerFilteringByAnotherCustomerIsRefused(t *testing.T) {
	w := newAreaWorld(t)
	acme := w.ctx("customer:acme", "group:staff")
	start := worldStart.Add(-time.Hour)
	end := start.Add(24 * time.Hour)
	for _, anchor := range [][2]string{
		{"customer", "globex"}, {"customer", "no-such-customer"}, {"group", "other-group"},
	} {
		crit := model.EventSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
			AnchorType: &anchor[0], AnchorToken: &anchor[1]}
		_, err := w.api.Events(acme, crit)
		assert.ErrorIs(t, err, auth.ErrForbidden, "events %v", anchor)
		_, err = w.api.MeasurementEvents(acme, crit)
		assert.ErrorIs(t, err, auth.ErrForbidden, "measurementEvents %v", anchor)
		_, err = w.api.LocationEvents(acme, crit)
		assert.ErrorIs(t, err, auth.ErrForbidden, "locationEvents %v", anchor)
		_, err = w.api.AlertEvents(acme, crit)
		assert.ErrorIs(t, err, auth.ErrForbidden, "alertEvents %v", anchor)
		_, err = w.api.BucketedMeasurements(acme, model.MeasurementAggregationCriteria{
			StartTime: &start, EndTime: &end, IntervalSeconds: 60, AnchorType: &anchor[0], AnchorToken: &anchor[1]})
		assert.ErrorIs(t, err, auth.ErrForbidden, "bucketedMeasurements %v", anchor)
		assert.False(t, errors.Is(err, auth.ErrScopeUnsupported), "outside the scope is FORBIDDEN, not unsupported")

		// the same through the resolver, which is what the wire sees
		in := struct{ Criteria EventSearchCriteriaInput }{Criteria: EventSearchCriteriaInput{
			PageNumber: 1, PageSize: 10, Anchor: &EventAnchorInput{Type: anchor[0], Token: anchor[1]}}}
		_, err = (&SchemaResolver{}).Events(acme, in)
		assert.ErrorIs(t, err, auth.ErrForbidden, "resolver events %v", anchor)
	}

	// controls: the scope's own anchors, and the unscoped caller, are not refused.
	for _, c := range []struct {
		name string
		ctx  context.Context
		a    [2]string
	}{
		{"own customer", acme, [2]string{"customer", "acme"}},
		{"own group", acme, [2]string{"group", "staff"}},
		{"unscoped, any customer", w.ctx(), [2]string{"customer", "globex"}},
		{"unscoped, nonexistent customer", w.ctx(), [2]string{"customer", "no-such-customer"}},
	} {
		_, err := w.read(c.ctx, c.a[0], c.a[1])
		assert.NoError(t, err, c.name)
	}
}

// A known kind with no scoped semantics is refused, never answered empty.
func TestAScopedCallerFilteringByADeviceAnchorIsRefusedAsUnsupported(t *testing.T) {
	w := newAreaWorld(t)
	_, err := w.read(w.ctx("customer:acme"), "device", "acme-pump")
	assert.ErrorIs(t, err, auth.ErrScopeUnsupported)
	assert.ErrorIs(t, err, auth.ErrForbidden, "it is a FORBIDDEN refusal with a distinct reason")
	_, err = w.read(w.ctx(), "device", "acme-pump")
	assert.NoError(t, err, "control: an unscoped caller may filter by a device anchor")
}

// Another tenant's anchor rows never take part in the filter. tenant2 holds two rows that
// carry tenant1's event ids (a collision no real write makes, which is what makes the
// predicate observable): one naming Acme, which would admit Globex's event into Acme's
// self-join if the join did not pin the tenant, and one naming a floor no tenant1 event is
// on, which would put Acme's event on that floor if the filter's own read did not.
func TestAnotherTenantsAnchorRowsDoNotTakePartInTheFilter(t *testing.T) {
	w := newAreaWorld(t)
	sys := core.WithSystemContext(context.Background())
	var ids struct{ acme, globex []byte }
	var rows []model.EventAnchor
	require.NoError(t, w.api.RDB.DB(sys).Where("device_token IN ? AND event_type = ? AND anchor_type = ?",
		[]string{"acme-pump", "globex-pump"}, eventtype.Measurement, "customer").Find(&rows).Error)
	for _, r := range rows {
		if r.DeviceToken == "acme-pump" {
			ids.acme = r.EventId
		} else {
			ids.globex = r.EventId
		}
	}
	require.NotNil(t, ids.acme)
	require.NotNil(t, ids.globex)
	other := core.WithTenant(context.Background(), "tenant2")
	for _, a := range []model.EventAnchor{
		{EventId: ids.globex, DeviceToken: "globex-pump", EventType: eventtype.Measurement,
			OccurredTime: worldStart, AnchorType: "customer", AnchorToken: "acme"},
		{EventId: ids.acme, DeviceToken: "acme-pump", EventType: eventtype.Measurement,
			OccurredTime: worldStart, AnchorType: "area", AnchorToken: "floor-z"},
	} {
		a := a
		require.NoError(t, w.api.RDB.DB(other).Create(&a).Error)
	}
	acme := w.ctx("customer:acme")
	assert.Equal(t, []float64{1, 1000}, w.mustRead(t, acme, "area", "floor-shared").values,
		"tenant2's Acme row must not admit Globex's event")
	assert.Empty(t, w.mustRead(t, acme, "area", "floor-z").values,
		"tenant2's floor-z row must not put tenant1's event on that floor")
}
