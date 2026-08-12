// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The other TWO ordered reads.
//
// 🔑 This file exists because of a counting mistake worth naming. `newestFirst` was
// applied to all FOUR reads — Events, LocationEvents, MeasurementEvents,
// AlertEvents — and the first round of tests pinned two of them. Two of four is
// exactly the shape of the defect this whole change is about: the fix was applied
// everywhere and the guard was applied where someone happened to be looking, so
// deleting the ordering from AlertEvents or from the base Events read would have
// stayed green.
//
// The base `Events` read is the one that most needs its own test rather than an
// argument by analogy, because it is the only one whose tie-break is a DIFFERENT
// column: the payload tables key on payload_id, the base table on event_id. A
// copy-paste that gave the base read `payload_id` would not compile against
// sqlite's stricter column checking here but could still slip through as a
// silently unordered read on a real database, so it is asserted rather than
// assumed.

// seedAlerts writes one alert per supplied minute offset, in the order given, each
// carrying a message that names it — so a result is identified by WHAT IT SAYS and
// not merely by a timestamp that happens to look plausible.
func seedAlerts(t *testing.T, api *Api, ctx context.Context, offsets []int) {
	t.Helper()
	for _, offset := range offsets {
		occurred := orderingBase.Add(time.Duration(offset) * time.Minute)
		ev := eventOfType("acme", "device-1", esmodel.Alert, occurred, fmt.Sprintf("alert-%d", offset))
		_, err := api.CreateAlertEvents(ctx, api.RDB.DB(ctx), []*AlertEventCreateRequest{{
			Event:             ev,
			EntryOccurredTime: ev.OccurredTime,
			Type:              "overheat",
			Level:             uint32(offset),
			Message:           fmt.Sprintf("alert at +%dm", offset),
			Source:            "test",
		}})
		require.NoErrorf(t, err, "seed alert at +%dm", offset)
	}
}

func TestAlertEventsReadNewestFirst(t *testing.T) {
	api := newEventTablesApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedAlerts(t, api, ctx, shuffledOffsets)

	results, err := api.AlertEvents(ctx, EventSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100},
	})
	require.NoError(t, err)
	require.Len(t, results.Results, len(shuffledOffsets))

	for i := 1; i < len(results.Results); i++ {
		prev, cur := results.Results[i-1].OccurredTime, results.Results[i].OccurredTime
		assert.Truef(t, prev.After(cur),
			"result %d (%s) must be strictly NEWER than result %d (%s)",
			i-1, prev.UTC(), i, cur.UTC())
	}

	// Stated in the values too, so a read returning the right times attached to the
	// wrong rows still fails.
	for i, offset := range []int{4, 3, 2, 1, 0} {
		assert.Equalf(t, fmt.Sprintf("alert at +%dm", offset), results.Results[i].Message,
			"result %d must be the alert from +%dm", i, offset)
	}

	latest, err := api.AlertEvents(ctx, EventSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 1},
	})
	require.NoError(t, err)
	require.Len(t, latest.Results, 1)
	assert.Equal(t, "alert at +4m", latest.Results[0].Message,
		"a pageSize:1 read must return the most recent alert, not an arbitrary one")
}

// The base events read is ordered too, and on its OWN identity column.
//
// Seeding through the alert path is deliberate rather than incidental: every
// payload create upserts its parent, so this exercises the base table exactly as
// production fills it, and it proves the ordering holds for a caller who never
// touches a payload table at all.
func TestBaseEventsReadNewestFirst(t *testing.T) {
	api := newEventTablesApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedAlerts(t, api, ctx, shuffledOffsets)

	results, err := api.Events(ctx, EventSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100},
	})
	require.NoError(t, err)
	require.Len(t, results.Results, len(shuffledOffsets))

	for i := 1; i < len(results.Results); i++ {
		prev, cur := results.Results[i-1].OccurredTime, results.Results[i].OccurredTime
		assert.Truef(t, prev.After(cur),
			"result %d (%s) must be strictly NEWER than result %d (%s)",
			i-1, prev.UTC(), i, cur.UTC())
	}

	require.True(t, orderingBase.Add(4*time.Minute).Equal(results.Results[0].OccurredTime),
		"the newest base event must come back first")

	latest, err := api.Events(ctx, EventSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 1},
	})
	require.NoError(t, err)
	require.Len(t, latest.Results, 1)
	assert.True(t, orderingBase.Add(4*time.Minute).Equal(latest.Results[0].OccurredTime),
		"a pageSize:1 read must return the most recent event, not an arbitrary one")
}
