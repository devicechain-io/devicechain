// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// What the database does with a sub-microsecond instant it is handed, with no Go-side
// truncation in front of it. This is the probe behind the S8-0 release note: it decides
// whether a raw row redelivered across the deploy could be stored at a different
// occurred_time than the one it already has. The row is inserted through the column-array
// path directly, bypassing BuildMeasurementRows.
func TestIntegrationDatabaseHandlingOfSubMicrosecondInstant(t *testing.T) {
	api := newPostgresApi(t, "s80probe")
	ctx := core.WithTenant(context.Background(), "acme")
	at := time.Date(2026, 10, 19, 10, 0, 0, 999, time.UTC) // .000000999Z
	ev := pgEvent("acme", "device-1", "probe", at, "x")
	require.NoError(t, upsertParentEvents(ctx, api.RDB.DB(ctx), []*Event{&ev}))
	row := &MeasurementEvent{EventId: ev.EventId, PayloadId: []byte{1}, DeviceToken: "device-1",
		EventType: ev.EventType, OccurredTime: at, Name: "temp"}
	require.NoError(t, insertPayloadRows(ctx, api.RDB.DB(ctx), measurementColumns, []*MeasurementEvent{row}))

	var got MeasurementEvent
	require.NoError(t, api.RDB.DB(ctx).Where("name = ?", "temp").First(&got).Error)
	t.Logf("handed %s, read back %s", at.Format(time.RFC3339Nano), got.OccurredTime.UTC().Format(time.RFC3339Nano))
	assert.True(t, got.OccurredTime.UTC().Equal(time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)),
		"the database TRUNCATES a .000000999Z instant (rounding would read back .000001Z)")
}

// Both arrival orders against the real unique index: a derived value persisted from the
// live (ns, +02:00) instant and the same value re-derived from the stored instant are one
// row, whichever is written first, and the stored instant is the truncated UTC one.
func TestIntegrationDerivedRowIsOneRowInBothArrivalOrders(t *testing.T) {
	api := newPostgresApi(t, "s80order")
	ctx := core.WithTenant(context.Background(), "acme")
	for name, f := range canonicalFixtures {
		for _, liveFirst := range []bool{true, false} {
			order := "live-first"
			if !liveFirst {
				order = "rederived-first"
			}
			t.Run(name+"/"+order, func(t *testing.T) {
				device := name + order
				ev := pgEvent("acme", device, device, f.canonical, "p-"+device)
				live := derivedRequest(f.live)
				live.Event, live.DeviceToken = ev, device
				stored := derivedRequest(f.canonical)
				stored.Event, stored.DeviceToken = ev, device
				stored.Value = float64Ptr(99) // a different formula's value: first wins

				first, second := live, stored
				if !liveFirst {
					first, second = stored, live
				}
				for _, r := range []*MeasurementEventCreateRequest{first, second} {
					_, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx), []*MeasurementEventCreateRequest{r})
					require.NoError(t, err)
				}
				var rows []MeasurementEvent
				require.NoError(t, api.RDB.DB(ctx).Where("device_token = ?", device).Find(&rows).Error)
				require.Len(t, rows, 1, "one sample, one derived row")
				assert.True(t, rows[0].OccurredTime.UTC().Equal(f.canonical), "stored %s", rows[0].OccurredTime)
			})
		}
	}
}
