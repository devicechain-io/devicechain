// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"sort"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

// The time range of the alarm search keeps the alarms whose span OVERLAPS it: a chart drawing
// alarms as spans must get the alarm raised before the range that was still active during it.

func rangeAlarm(t *testing.T, r *alarmTypeRig, token string, raised time.Time, cleared *time.Time) {
	t.Helper()
	a := Alarm{
		TokenReference: rdb.TokenReference{Token: token},
		OriginatorType: string(entity.TypeDevice), OriginatorId: r.devices["t1/d1"],
		AlarmKey: token, MetricKey: "temp", State: string(AlarmStateActive), Severity: "MAJOR",
		RaisedTime: raised,
	}
	if cleared != nil {
		a.State = string(AlarmStateCleared)
		a.ClearedTime = sql.NullTime{Time: *cleared, Valid: true}
	}
	require.NoError(t, r.db.WithContext(tenantCtx("t1")).Create(&a).Error)
}

func TestAlarmSearchTimeRangeIsOverlap(t *testing.T) {
	r := newAlarmTypeRig(t)
	r.device(t, "t1", "pump", "d1")
	at := func(h int) time.Time { return time.Date(2026, 9, 1, h, 0, 0, 0, time.UTC) }
	ptr := func(v time.Time) *time.Time { return &v }

	// The range is 10:00..12:00.
	rangeAlarm(t, r, "before-cleared", at(6), ptr(at(8)))         // over before the range: out
	rangeAlarm(t, r, "before-active", at(6), nil)                 // raised before, still active: IN
	rangeAlarm(t, r, "before-cleared-inside", at(6), ptr(at(11))) // raised before, cleared inside: IN
	rangeAlarm(t, r, "inside", at(10), ptr(at(11)))               // wholly inside: IN
	rangeAlarm(t, r, "spans", at(9), ptr(at(13)))                 // covers the whole range: IN
	rangeAlarm(t, r, "after", at(13), nil)                        // raised after the range: out
	rangeAlarm(t, r, "at-the-edges", at(12), ptr(at(14)))         // raised exactly at the end: IN (inclusive)
	rangeAlarm(t, r, "cleared-at-start", at(7), ptr(at(10)))      // cleared exactly at the start: IN (inclusive)

	search := func(c AlarmSearchCriteria) ([]string, int32) {
		t.Helper()
		c.Pagination = rdb.Pagination{PageNumber: 1, PageSize: 100}
		found, err := r.api.Alarms(tenantCtx("t1"), c)
		require.NoError(t, err)
		keys := []string{}
		for _, a := range found.Results {
			keys = append(keys, a.AlarmKey)
		}
		sort.Strings(keys)
		return keys, found.Pagination.TotalRecords
	}
	from, to := at(10).Format(time.RFC3339), at(12).Format(time.RFC3339)

	keys, total := search(AlarmSearchCriteria{StartTime: &from, EndTime: &to})
	require.Equal(t, []string{"at-the-edges", "before-active", "before-cleared-inside", "cleared-at-start", "inside", "spans"}, keys)
	require.EqualValues(t, 6, total, "the total counts the range, so a capped page reports exactly what it left out")

	// One bound at a time.
	keys, _ = search(AlarmSearchCriteria{StartTime: &from})
	require.Equal(t, []string{"after", "at-the-edges", "before-active", "before-cleared-inside", "cleared-at-start", "inside", "spans"}, keys)
	keys, _ = search(AlarmSearchCriteria{EndTime: &to})
	require.Equal(t, []string{"at-the-edges", "before-active", "before-cleared", "before-cleared-inside", "cleared-at-start", "inside", "spans"}, keys)

	// A capped page still reports the whole range's total.
	c := AlarmSearchCriteria{StartTime: &from, EndTime: &to}
	c.Pagination = rdb.Pagination{PageNumber: 1, PageSize: 2}
	page, err := r.api.Alarms(tenantCtx("t1"), c)
	require.NoError(t, err)
	require.Len(t, page.Results, 2)
	require.EqualValues(t, 6, page.Pagination.TotalRecords)

	// Negative control: no range finds all eight.
	keys, _ = search(AlarmSearchCriteria{})
	require.Len(t, keys, 8)

	// Another tenant's alarm in the range is not found.
	r.device(t, "t2", "pump", "d1")
	r.alarm(t, "t2", "d1", "MAJOR", AlarmStateActive)
	keys, _ = search(AlarmSearchCriteria{StartTime: &from, EndTime: &to})
	require.Len(t, keys, 6)
}

func TestAlarmSearchTimeRangeRejectsNonRFC3339(t *testing.T) {
	r := newAlarmTypeRig(t)
	bad := "yesterday"
	_, err := r.api.Alarms(tenantCtx("t1"), AlarmSearchCriteria{StartTime: &bad})
	require.ErrorContains(t, err, "startTime")
	_, err = r.api.Alarms(tenantCtx("t1"), AlarmSearchCriteria{EndTime: &bad})
	require.ErrorContains(t, err, "endTime")
}
