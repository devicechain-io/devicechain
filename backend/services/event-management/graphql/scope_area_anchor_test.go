// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The SHARED-AREA fixture of the customer-scoped area work (ADR-081 decision 12): one area,
// "floor-shared", holding one device of each of two customers. Each device's event carries
// the anchor rows the event resolver writes for it, one per tracked edge at the event's
// instant: its customer and the shared area.
//
//	acme-pump   -> customer:acme   + area:floor-shared  (+ area:floor-acme)
//	globex-pump -> customer:globex + area:floor-shared  (+ area:floor-globex)
//
// It is built here, in event-management's own terms, because S2 (the anchor filter for scoped
// callers) is tested against exactly this data.
type sharedAreaEvents struct {
	api    *model.Api
	tenant string
}

func newSharedAreaEvents(t *testing.T) sharedAreaEvents {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.Event{}, &model.EventAnchor{}))
	f := sharedAreaEvents{api: model.NewApi(&rdb.RdbManager{Database: db}), tenant: "tenant1"}
	write := core.WithTenant(context.Background(), f.tenant)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for _, d := range []struct{ device, customer, own string }{
		{"acme-pump", "acme", "floor-acme"}, {"globex-pump", "globex", "floor-globex"},
	} {
		event := model.Event{DeviceToken: d.device, EventType: eventtype.Measurement, OccurredTime: at, ProcessedTime: at, Source: "mqtt"}
		event.EventId = model.DeriveEventId(f.tenant, &event, nil)
		require.NoError(t, db.WithContext(write).Create(&event).Error)
		for _, a := range [][2]string{{"customer", d.customer}, {"area", "floor-shared"}, {"area", d.own}} {
			require.NoError(t, db.WithContext(write).Create(&model.EventAnchor{
				EventId: event.EventId, DeviceToken: d.device, EventType: eventtype.Measurement,
				OccurredTime: at, AnchorType: a[0], AnchorToken: a[1],
			}).Error)
		}
	}
	return f
}

func (f sharedAreaEvents) ctx(anchors ...string) context.Context {
	var scope []string // nil: an unscoped caller
	if len(anchors) > 0 {
		scope = anchors
	}
	return auth.WithClaims(core.WithTenant(context.Background(), f.tenant), &auth.Claims{
		Tenant: f.tenant, Username: "member@example.com", TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.EventRead)}, ScopeAnchors: scope})
}

func (f sharedAreaEvents) devicesOn(t *testing.T, ctx context.Context, anchorType, token string) []string {
	t.Helper()
	found, err := f.api.Events(ctx, model.EventSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 50}, AnchorType: &anchorType, AnchorToken: &token})
	require.NoError(t, err)
	out := []string{}
	for _, e := range found.Results {
		out = append(out, e.DeviceToken)
	}
	return out
}

// The controls of the defect below, which hold today and must keep holding: the unscoped
// caller finds both customers' events on the shared area, and a scoped caller finds its own
// through its own customer anchor.
func TestTheSharedAreaFixtureHoldsOneEventPerCustomer(t *testing.T) {
	f := newSharedAreaEvents(t)
	require.ElementsMatch(t, []string{"acme-pump", "globex-pump"}, f.devicesOn(t, f.ctx(), "area", "floor-shared"),
		"control: the unscoped caller sees both customers' events on the shared area")
	require.Equal(t, []string{"acme-pump"}, f.devicesOn(t, f.ctx("customer:acme"), "customer", "acme"),
		"control: a scoped caller finds its own events by its own customer anchor")
}

// 🔴 THE LIVE SILENT EMPTY (customer-scoped area dashboards spec, section 1). A scoped
// caller filtering events by an AREA anchor gets nothing, with no error: the anchor-filter
// subquery reads event_anchors under that table's OwnAnchor rule, which admits only the
// rows naming an anchor in the caller's scope - the customer:acme row, never the
// area:floor-shared row - so it can match no event. The answer reads as "no data", not as a
// refusal, which is the worst way to be wrong.
//
// The expected value is the Acme device's event alone: not zero (today) and not both
// customers' (the leak a naive fix would introduce). Slice S2 repairs the filter and turns
// this test on.
func TestAScopedCallerFilteringByAnAreaAnchorIsNotAnsweredEmpty(t *testing.T) {
	f := newSharedAreaEvents(t)
	require.Equal(t, []string{"acme-pump"}, f.devicesOn(t, f.ctx("customer:acme"), "area", "floor-shared"),
		"Acme's member filtering by the shared area must get exactly Acme's event")
	require.Equal(t, []string{"globex-pump"}, f.devicesOn(t, f.ctx("customer:globex"), "area", "floor-shared"))
}
