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

// 🔴 WHY THIS FILE EXISTS. Every read in api_read.go issued LIMIT/OFFSET with no
// ORDER BY at all. Row order is unspecified without one, so the defect was not
// "results look shuffled": `pageSize: 1` — the obvious way to ask a device where it
// is NOW — returned an ARBITRARY fix rather than the latest, and paging over rows
// that tie could hand back one row twice while skipping another entirely. Both
// failure modes return a well-formed answer that is simply wrong, which is why
// nothing upstream noticed.
//
// The ordering is asserted on measurements as well as locations because the same
// defect and the same fix apply to all four reads. Tested on location alone, a later
// refactor can drop it from the siblings and stay green.

// orderingBase is the reference instant the ordering fixtures hang off.
var orderingBase = time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

// shuffledOffsets is the INSERT order of the fixtures, in minutes past orderingBase.
// It is deliberately neither ascending nor descending: an ordering assertion that a
// sorted insert would satisfy by accident measures the fixture, not the ORDER BY.
var shuffledOffsets = []int{2, 0, 4, 1, 3}

// latitudeAt maps a minute offset to its own distinct latitude, so a result can be
// identified by WHAT IT SAYS rather than merely by its timestamp.
func latitudeAt(offset int) float64 {
	return 33.741 + float64(offset)/1000.0
}

// seedLocationFixes writes one location fix per supplied minute offset, in the order
// given, each carrying the latitude that names it.
func seedLocationFixes(t *testing.T, api *Api, ctx context.Context, offsets []int) {
	t.Helper()
	for _, offset := range offsets {
		occurred := orderingBase.Add(time.Duration(offset) * time.Minute)
		ev := eventOfType("acme", "device-1", esmodel.Location, occurred, fmt.Sprintf("fix-%d", offset))
		_, err := api.CreateLocationEvents(ctx, api.RDB.DB(ctx), []*LocationEventCreateRequest{{
			Event:     ev,
			Latitude:  f64(latitudeAt(offset)),
			Longitude: f64(-84.388),
		}})
		require.NoErrorf(t, err, "seed fix at +%dm", offset)
	}
}

// TestLocationEventsReadNewestFirst is the "where is this device now" question.
func TestLocationEventsReadNewestFirst(t *testing.T) {
	api := newEventTablesApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedLocationFixes(t, api, ctx, shuffledOffsets)

	results, err := api.LocationEvents(ctx, EventSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100},
	})
	require.NoError(t, err)
	require.Len(t, results.Results, len(shuffledOffsets))

	for i := 1; i < len(results.Results); i++ {
		prev, cur := results.Results[i-1].OccurredTime, results.Results[i].OccurredTime
		assert.Truef(t, prev.After(cur),
			"result %d (%s) must be strictly NEWER than result %d (%s) — the read is newest-first",
			i-1, prev.UTC(), i, cur.UTC())
	}

	// The same claim stated in the values, so a read that returned the right times
	// attached to the wrong rows still fails.
	wantLatitudes := []float64{
		latitudeAt(4), latitudeAt(3), latitudeAt(2), latitudeAt(1), latitudeAt(0),
	}
	for i, want := range wantLatitudes {
		require.Truef(t, results.Results[i].Latitude.Valid, "result %d must carry a latitude", i)
		assert.Equalf(t, want, results.Results[i].Latitude.Float64,
			"result %d must be the fix from +%dm", i, 4-i)
	}

	// 🔴 The question the missing ORDER BY made unanswerable: one row, and it must be
	// the LATEST one — identified by its coordinate, not merely by a timestamp that
	// happens to look plausible.
	latest, err := api.LocationEvents(ctx, EventSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 1},
	})
	require.NoError(t, err)
	require.Len(t, latest.Results, 1)
	require.True(t, latest.Results[0].Latitude.Valid)
	assert.Equal(t, latitudeAt(4), latest.Results[0].Latitude.Float64,
		"a pageSize:1 read must return the most recent fix, not an arbitrary one")
	assert.True(t, orderingBase.Add(4*time.Minute).Equal(latest.Results[0].OccurredTime))
}

// TestLocationEventsPagingIsTotalOverTiedOccurredTimes covers the case occurred_time
// alone cannot order: a device that samples several sensors and publishes each under
// ONE shared timestamp. Ties are real, and a tie under LIMIT/OFFSET reintroduces the
// repeat-and-skip in miniature — page 1 and page 2 can both return the same row while
// another is never returned at all.
func TestLocationEventsPagingIsTotalOverTiedOccurredTimes(t *testing.T) {
	api := newEventTablesApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	const tied = 5
	for i := 0; i < tied; i++ {
		ev := eventOfType("acme", "device-1", esmodel.Location, orderingBase, fmt.Sprintf("tied-%d", i))
		_, err := api.CreateLocationEvents(ctx, api.RDB.DB(ctx), []*LocationEventCreateRequest{{
			Event:     ev,
			Latitude:  f64(latitudeAt(i)),
			Longitude: f64(-84.388),
		}})
		require.NoErrorf(t, err, "seed tied row %d", i)
	}

	// Negative control: the rows really do share one occurred_time, so this test
	// exercises the tie rather than sidestepping it.
	var stored []LocationEvent
	require.NoError(t, api.RDB.DB(ctx).Find(&stored).Error)
	require.Len(t, stored, tied, "all tied rows must have persisted as distinct rows")
	for _, row := range stored {
		require.True(t, orderingBase.Equal(row.OccurredTime), "every fixture row shares one timestamp")
	}
	want := make(map[string]bool, tied)
	for _, row := range stored {
		want[string(row.PayloadId)] = true
	}
	require.Len(t, want, tied, "the tied rows are distinct readings with distinct identities")

	// Page through one row at a time, exactly as a client walking the result set does.
	seen := make(map[string]bool, tied)
	order := make([][]byte, 0, tied)
	for page := int32(1); page <= tied; page++ {
		got, err := api.LocationEvents(ctx, EventSearchCriteria{
			Pagination: rdb.Pagination{PageNumber: page, PageSize: 1},
		})
		require.NoErrorf(t, err, "page %d", page)
		require.Lenf(t, got.Results, 1, "page %d must hold exactly one row", page)
		id := string(got.Results[0].PayloadId)
		assert.Falsef(t, seen[id], "page %d returned a row an earlier page already returned — "+
			"a repeat means some other row is being skipped", page)
		seen[id] = true
		order = append(order, got.Results[0].PayloadId)
	}
	assert.Equal(t, want, seen,
		"paging one row at a time must yield exactly the stored set — no repeats, no omissions")

	// 🔴 MEASURED, so that the next reader is not misled about which half of this test
	// is doing the work. On this in-memory sqlite harness the assertion above does NOT
	// fail when the tie-break is removed: sqlite returns the tied rows in the same scan
	// order for every one of the identical LIMIT/OFFSET queries, so the repeat-and-skip
	// never materialises here even with no total order at all. It is stated anyway
	// because it is the CONTRACT — and Postgres, which reorders freely under a
	// non-total sort, is where it can bite.
	//
	// The assertion below is the one that fails when the tie-break is dropped, and it
	// is the mechanism stated directly: within a tie the order falls back to the row's
	// own identity, which is what makes the order TOTAL and therefore stable across
	// pages.
	for i := 1; i < len(order); i++ {
		assert.Positivef(t, compareBytes(order[i-1], order[i]),
			"tied rows must come back ordered by their own identity (page %d vs %d)", i, i+1)
	}
}

// compareBytes reports the sign of a lexicographic comparison, matching the SQL
// ordering of the identity column.
func compareBytes(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] > b[i] {
				return 1
			}
			return -1
		}
	}
	return len(a) - len(b)
}

// TestMeasurementEventsReadNewestFirst asserts the same property on a sibling read.
// "What did this sensor last report" is the same question as "where is this device
// now", and it was equally unanswerable.
func TestMeasurementEventsReadNewestFirst(t *testing.T) {
	api := newEventTablesApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	for _, offset := range shuffledOffsets {
		occurred := orderingBase.Add(time.Duration(offset) * time.Minute)
		ev := eventOfType("acme", "device-1", esmodel.Measurement, occurred, fmt.Sprintf("m-%d", offset))
		_, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx), []*MeasurementEventCreateRequest{{
			Event: ev,
			Name:  "temperature",
			Value: f64(20.5 + float64(offset)),
		}})
		require.NoErrorf(t, err, "seed measurement at +%dm", offset)
	}

	results, err := api.MeasurementEvents(ctx, EventSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100},
	})
	require.NoError(t, err)
	require.Len(t, results.Results, len(shuffledOffsets))

	for i := 1; i < len(results.Results); i++ {
		prev, cur := results.Results[i-1].OccurredTime, results.Results[i].OccurredTime
		assert.Truef(t, prev.After(cur),
			"measurement %d (%s) must be strictly NEWER than %d (%s)", i-1, prev.UTC(), i, cur.UTC())
	}

	latest, err := api.MeasurementEvents(ctx, EventSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 1},
	})
	require.NoError(t, err)
	require.Len(t, latest.Results, 1)
	require.True(t, latest.Results[0].Value.Valid)
	assert.Equal(t, 24.5, latest.Results[0].Value.Float64,
		"a pageSize:1 measurement read must return the most recent reading")
}
