// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"sort"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

// The command search's time range places a command at the instant it was sent, or queued
// when it never was, and keeps the commands whose instant falls in the range.
func TestCommandSearchTimeRange(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(t.Context(), "t1")
	at := func(h int) time.Time { return time.Date(2026, 9, 1, h, 0, 0, 0, time.UTC) }
	seed := func(token, device string, queued time.Time, sent *time.Time) {
		t.Helper()
		c := &Command{DeviceToken: device, Name: "reboot", Status: CommandQueued.String(), QueuedTime: queued}
		c.Token = token
		if sent != nil {
			c.Status = CommandSent.String()
			c.SentTime = sql.NullTime{Time: *sent, Valid: true}
		}
		require.NoError(t, api.RDB.DB(ctx).Create(c).Error)
	}
	ptr := func(v time.Time) *time.Time { return &v }

	// The range is 10:00..12:00.
	seed("queued-before-sent-inside", "d1", at(8), ptr(at(11))) // held, then sent inside: IN
	seed("queued-inside-sent-after", "d1", at(11), ptr(at(13))) // sent after: out
	seed("never-sent-inside", "d1", at(10), nil)                // never sent, queued at the start: IN
	seed("never-sent-before", "d1", at(9), nil)                 // never sent, queued before: out
	seed("sent-at-end", "d1", at(12), ptr(at(12)))              // inclusive end: IN
	seed("other-device", "d2", at(11), ptr(at(11)))             // another device: out with the device filter

	from, to := at(10).Format(time.RFC3339), at(12).Format(time.RFC3339)
	device := "d1"
	search := func(c CommandSearchCriteria) ([]string, int32) {
		t.Helper()
		c.Pagination = rdb.Pagination{PageNumber: 1, PageSize: 100}
		found, err := api.Commands(ctx, c)
		require.NoError(t, err)
		tokens := []string{}
		for _, r := range found.Results {
			tokens = append(tokens, r.Token)
		}
		sort.Strings(tokens)
		return tokens, found.Pagination.TotalRecords
	}

	tokens, total := search(CommandSearchCriteria{DeviceToken: &device, StartTime: &from, EndTime: &to})
	require.Equal(t, []string{"never-sent-inside", "queued-before-sent-inside", "sent-at-end"}, tokens)
	require.EqualValues(t, 3, total)

	// A capped page reports the range's whole total, so a chart can say exactly what it left out.
	page, err := api.Commands(ctx, CommandSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 1},
		DeviceToken: &device, StartTime: &from, EndTime: &to})
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
	require.EqualValues(t, 3, page.Pagination.TotalRecords)

	tokens, _ = search(CommandSearchCriteria{DeviceToken: &device, StartTime: &from})
	require.Equal(t, []string{"never-sent-inside", "queued-before-sent-inside", "queued-inside-sent-after", "sent-at-end"}, tokens)
	tokens, _ = search(CommandSearchCriteria{DeviceToken: &device, EndTime: &to})
	require.Equal(t, []string{"never-sent-before", "never-sent-inside", "queued-before-sent-inside", "sent-at-end"}, tokens)
	tokens, _ = search(CommandSearchCriteria{StartTime: &from, EndTime: &to})
	require.Equal(t, []string{"never-sent-inside", "other-device", "queued-before-sent-inside", "sent-at-end"}, tokens)

	// Negative control: no range finds all five of d1's.
	tokens, _ = search(CommandSearchCriteria{DeviceToken: &device})
	require.Len(t, tokens, 5)
}

func TestCommandSearchTimeRangeRejectsNonRFC3339(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(t.Context(), "t1")
	bad := "2026-09-01"
	_, err := api.Commands(ctx, CommandSearchCriteria{StartTime: &bad})
	require.ErrorContains(t, err, "startTime")
	_, err = api.Commands(ctx, CommandSearchCriteria{EndTime: &bad})
	require.ErrorContains(t, err, "endTime")
}
