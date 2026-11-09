// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

func seedBoiler(t *testing.T, api *Api) context.Context {
	t.Helper()
	if err := api.RDB.Database.AutoMigrate(&Device{}, &DeviceType{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	dt := &DeviceType{TokenReference: rdb.TokenReference{Token: "boiler"}}
	if err := api.RDB.Database.WithContext(ctx).Create(dt).Error; err != nil {
		t.Fatalf("seed device type: %v", err)
	}
	d := &Device{
		TokenReference: rdb.TokenReference{Token: "boiler-7"},
		NamedEntity:    rdb.NamedEntity{Name: sql.NullString{String: "Boiler 7", Valid: true}},
		DeviceTypeId:   dt.ID,
	}
	d.ID = dev
	if err := api.RDB.Database.WithContext(ctx).Create(d).Error; err != nil {
		t.Fatalf("seed device: %v", err)
	}
	return ctx
}

// An alarm event names the device that raised it. The numeric id alone is useless to the
// person a notification reaches, so the producer, which owns devices, reads the device once
// at emission and puts its token, name and type token on the event.
func TestAlarmEventNamesItsDevice(t *testing.T) {
	api, cap := newContributorTestApi(t)
	ctx := seedBoiler(t, api)

	raiseEdge(t, api, ctx, "over-temp", "r1", "MAJOR", 10)
	resolveEdge(t, api, ctx, "over-temp", "r1", 20)

	if len(cap.events) != 2 {
		t.Fatalf("want a RAISED and a CLEARED event, got %d", len(cap.events))
	}
	for _, ev := range cap.events {
		if ev.DeviceToken != "boiler-7" || ev.DeviceName != "Boiler 7" || ev.DeviceTypeToken != "boiler" {
			t.Fatalf("%s event names the device %q/%q/%q, want boiler-7/Boiler 7/boiler",
				ev.EventType, ev.DeviceToken, ev.DeviceName, ev.DeviceTypeToken)
		}
	}
}

// A device with no name leaves the name empty (it is omitted downstream, never invented), and
// a device that cannot be read publishes an unnamed event rather than failing the transition.
func TestAlarmEventWithoutAReadableDeviceIsStillPublished(t *testing.T) {
	api, cap := newContributorTestApi(t) // no devices table at all
	ctx := core.WithTenant(context.Background(), "acme")
	raiseEdge(t, api, ctx, "over-temp", "r1", "MAJOR", 10)
	if len(cap.events) != 1 {
		t.Fatalf("the transition must still publish its event, got %d", len(cap.events))
	}
	if ev := cap.events[0]; ev.DeviceToken != "" || ev.DeviceName != "" || ev.DeviceTypeToken != "" {
		t.Fatalf("an unreadable device must leave the names empty, got %q/%q/%q",
			ev.DeviceToken, ev.DeviceName, ev.DeviceTypeToken)
	}
}

// An acknowledgement does not notify anyone, so it must not pay a device read: a bulk acknowledge
// emits one per alarm.
func TestAcknowledgementEventsAreNotNamed(t *testing.T) {
	api, _ := newContributorTestApi(t)
	ctx := seedBoiler(t, api)
	ev := &AlarmStateChangeEvent{EventType: AlarmEventAcknowledged, OriginatorType: "device", OriginatorId: dev}
	api.describeAlarmDevice(ctx, ev)
	if ev.DeviceToken != "" {
		t.Fatalf("an acknowledgement was named: %+v", ev)
	}
}
