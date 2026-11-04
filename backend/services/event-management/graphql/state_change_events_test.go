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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// seedStateChange writes one presence transition the way the persistence worker does: the
// payload row, and the anchors of the event that carried it.
func seedStateChange(t *testing.T, db *gorm.DB, tenant, device, state string, at time.Time, anchors ...string) {
	t.Helper()
	event := model.Event{DeviceToken: device, EventType: eventtype.StateChange, OccurredTime: at, ProcessedTime: at, Source: "mqtt"}
	event.EventId = model.DeriveEventId(tenant, &event, []byte(state))
	write := core.WithTenant(context.Background(), tenant)
	require.NoError(t, db.WithContext(write).Create(&model.StateChangeEvent{
		EventId: event.EventId, DeviceToken: device, EventType: eventtype.StateChange,
		OccurredTime: at, State: state, SessionId: 7,
	}).Error)
	for _, token := range anchors {
		require.NoError(t, db.WithContext(write).Create(&model.EventAnchor{
			EventId: event.EventId, DeviceToken: device, EventType: eventtype.StateChange,
			OccurredTime: at, AnchorType: "customer", AnchorToken: token,
		}).Error)
	}
}

func TestStateChangeEventsReadTimelineWithScopeAndAuthority(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.Event{}, &model.EventAnchor{}, &model.StateChangeEvent{}))
	api := model.NewApi(&rdb.RdbManager{Database: db})

	const tenant = "tenant1"
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	seedStateChange(t, db, tenant, "pump-1", "CONNECTED", t0, "acme")
	seedStateChange(t, db, tenant, "pump-1", "DISCONNECTED", t0.Add(time.Hour), "acme")
	seedStateChange(t, db, tenant, "pump-2", "CONNECTED", t0.Add(2*time.Hour), "globex")
	// Another tenant's transition must never surface.
	seedStateChange(t, db, "tenant2", "pump-1", "CONNECTED", t0.Add(3*time.Hour), "acme")

	r := &SchemaResolver{}
	read := func(ctx context.Context, c EventSearchCriteriaInput) []string {
		t.Helper()
		found, err := r.StateChangeEvents(ctx, struct{ Criteria EventSearchCriteriaInput }{Criteria: c})
		require.NoError(t, err)
		out := []string{}
		for _, row := range found.Results() {
			out = append(out, row.DeviceToken()+":"+row.State())
		}
		return out
	}
	page := EventSearchCriteriaInput{PageNumber: 1, PageSize: 10}

	assert.Equal(t, []string{"pump-2:CONNECTED", "pump-1:DISCONNECTED", "pump-1:CONNECTED"},
		read(scopedCtx(api, tenant, nil, string(auth.EventRead)), page), "newest first, own tenant only")

	dev := page
	token := "pump-1"
	dev.DeviceToken = &token
	assert.Equal(t, []string{"pump-1:DISCONNECTED", "pump-1:CONNECTED"},
		read(scopedCtx(api, tenant, nil, string(auth.EventRead)), dev))

	bounded := dev
	start := t0.Add(30 * time.Minute).Format(time.RFC3339)
	bounded.StartTime = &start
	assert.Equal(t, []string{"pump-1:DISCONNECTED"},
		read(scopedCtx(api, tenant, nil, string(auth.EventRead)), bounded), "startTime bounds the timeline")

	assert.Equal(t, []string{"pump-1:DISCONNECTED", "pump-1:CONNECTED"},
		read(scopedCtx(api, tenant, []string{"customer:acme"}, string(auth.EventRead)), page),
		"a member limited to Acme sees only transitions recorded while the device was Acme's")

	_, err = r.StateChangeEvents(scopedCtx(api, tenant, nil, string(auth.LocationRead)),
		struct{ Criteria EventSearchCriteriaInput }{Criteria: page})
	assert.Error(t, err, "event:read is required")
}
