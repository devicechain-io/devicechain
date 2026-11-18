// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type deletionCascadePublisher struct {
	order  []string
	events []*AlarmStateChangeEvent
	fail   bool
}

func TestDeletionAlarmCascadeIncludesAlarmInsertedBeforeDelete(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	api.ownershipNow = func() time.Time { return now }
	d := deletionDevice(t, api, ctx, "device")
	other := deletionDevice(t, api, ctx, "other")
	active := &Alarm{OriginatorType: "device", OriginatorId: d.ID, AlarmKey: "hot", State: "ACTIVE", RaisedTime: now.Add(-time.Hour)}
	active.Token = "active-alarm"
	cleared := &Alarm{OriginatorType: "device", OriginatorId: d.ID, AlarmKey: "old", State: "CLEARED"}
	cleared.Token = "cleared-alarm"
	unrelated := &Alarm{OriginatorType: "device", OriginatorId: other.ID, AlarmKey: "hot", State: "ACTIVE"}
	unrelated.Token = "unrelated-alarm"
	for _, alarm := range []*Alarm{active, cleared, unrelated} {
		require.NoError(t, api.RDB.DB(ctx).Create(alarm).Error)
	}
	late := &Alarm{OriginatorType: "device", OriginatorId: d.ID, AlarmKey: "late", State: "ACTIVE", RaisedTime: now.Add(-time.Minute)}
	late.Token = "late-alarm"
	inserted := false
	// Insert on the same transaction immediately before the DELETE executes. This
	// deterministically models a writer arriving after the old alarm SELECT.
	db := api.RDB.DB(ctx)
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("test:insert_late_alarm", func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Name != "Alarm" || inserted {
			return
		}
		inserted = true
		tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Create(late).Error)
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Delete().Remove("test:insert_late_alarm")) })
	deleteForOutbox(t, api, ctx, d.Token)
	require.True(t, inserted)
	var remaining []*Alarm
	require.NoError(t, db.Unscoped().Find(&remaining).Error)
	require.Len(t, remaining, 1)
	require.Equal(t, unrelated.Token, remaining[0].Token)
	rows := deletionRows(t, api, ctx)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].Cascade)
	var events []*AlarmStateChangeEvent
	require.NoError(t, json.Unmarshal(*rows[0].Cascade, &events))
	var want []*AlarmStateChangeEvent
	for _, alarm := range []*Alarm{active, late} {
		alarm.State = "CLEARED"
		event := newAlarmStateChangeEvent(alarm, AlarmEventCleared, "", now)
		event.ClearReason = AlarmClearReasonOriginatorDeleted
		describeAlarmDevice(event, d)
		want = append(want, event)
	}
	require.ElementsMatch(t, want, events)
}

func (p *deletionCascadePublisher) PublishAlarmEvent(context.Context, *AlarmStateChangeEvent) {
	panic("best-effort publisher used by outbox")
}
func (p *deletionCascadePublisher) PublishAlarmEventDedup(_ context.Context, e *AlarmStateChangeEvent, id string) error {
	if p.fail {
		return errors.New("alarm broker unavailable")
	}
	if id == "" {
		panic("missing stable dedup id")
	}
	p.order = append(p.order, "alarm")
	p.events = append(p.events, e)
	return nil
}
func (p *deletionCascadePublisher) PublishEntityDeleted(context.Context, *entityevent.EntityDeletedEvent) error {
	p.order = append(p.order, "entity")
	return nil
}

func TestDeletionAlarmCascade(t *testing.T) {
	for _, mode := range []string{"success", "alarm failure", "no alarm publisher", "no active alarms"} {
		t.Run(mode, func(t *testing.T) {
			api := newDeleteEmitTestApi(t)
			ctx := core.WithTenant(context.Background(), "acme")
			now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
			api.ownershipNow = func() time.Time { return now }
			d := deletionDevice(t, api, ctx, "device")
			dt := &DeviceType{TokenReference: rdb.TokenReference{Token: "boiler"}}
			require.NoError(t, api.RDB.DB(ctx).Create(dt).Error)
			d.DeviceTypeId = dt.ID
			d.Name = sql.NullString{String: "Boiler", Valid: true}
			require.NoError(t, api.RDB.DB(ctx).Save(d).Error)
			active := &Alarm{OriginatorType: "device", OriginatorId: d.ID, AlarmKey: "hot", MetricKey: "temp", State: "ACTIVE", Severity: "MAJOR", RaisedTime: now.Add(-time.Hour), ScopeAnchors: rdb.Anchors{"customer:acme"}}
			active.Token = "active-alarm"
			if mode != "no active alarms" {
				require.NoError(t, api.RDB.DB(ctx).Create(active).Error)
			}
			cleared := &Alarm{OriginatorType: "device", OriginatorId: d.ID, AlarmKey: "old", State: "CLEARED"}
			cleared.Token = "cleared-alarm"
			require.NoError(t, api.RDB.DB(ctx).Create(cleared).Error)
			p := &deletionCascadePublisher{fail: mode == "alarm failure"}
			api.EntityDeletedPublisher = p
			if mode != "no alarm publisher" {
				api.AlarmPublisher = p
			}
			deleteForOutbox(t, api, ctx, d.Token)
			rows := deletionRows(t, api, ctx)
			require.Len(t, rows, 1)
			if mode != "no active alarms" {
				require.NotNil(t, rows[0].Cascade)
				var events []*AlarmStateChangeEvent
				require.NoError(t, json.Unmarshal(*rows[0].Cascade, &events))
				require.Len(t, events, 1)
				active.State = "CLEARED"
				want := newAlarmStateChangeEvent(active, AlarmEventCleared, "", now)
				want.ClearReason = AlarmClearReasonOriginatorDeleted
				want.DeviceToken, want.DeviceName, want.DeviceTypeToken = d.Token, "Boiler", "boiler"
				require.Equal(t, want, events[0])
				require.Equal(t, rows[0].DeletedTime, events[0].OccurredTime)
			}
			_, err := api.DrainEntityDeletions(ctx, EntityDeletionDrainPageSize)
			if mode == "alarm failure" || mode == "no alarm publisher" {
				require.Error(t, err)
				require.Empty(t, p.order)
				require.Len(t, deletionRows(t, api, ctx), 1)
				return
			}
			require.NoError(t, err)
			if mode == "no active alarms" {
				require.Equal(t, []string{"entity"}, p.order)
			} else {
				require.Equal(t, []string{"alarm", "entity"}, p.order)
			}
			require.Empty(t, deletionRows(t, api, ctx))
		})
	}
}
