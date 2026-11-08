// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func utc(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }

func TestValidateUsageRange(t *testing.T) {
	assert.NoError(t, ValidateUsageRange(utc(2026, 11, 1, 0), utc(2026, 11, 1, 23)), "one day is a range")
	assert.NoError(t, ValidateUsageRange(utc(2026, 1, 1, 0), utc(2027, 1, 1, 0)), "366 days is the bound")
	assert.ErrorIs(t, ValidateUsageRange(utc(2026, 11, 2, 0), utc(2026, 11, 1, 0)), ErrUsageRange)
	assert.ErrorIs(t, ValidateUsageRange(utc(2026, 1, 1, 0), utc(2027, 1, 2, 0)), ErrUsageRange, "367 days")
}

// A day with no record reads zero and is present, never absent: the contract a bill is built on.
// Completeness comes from the ledger alone, so a day with no row is complete only once all its
// hours were measured.
func TestFillUsageDaysZeroFillsEveryDay(t *testing.T) {
	f, e := utc(2026, 11, 1, 0), utc(2026, 11, 3, 0)
	rows := []tenantUsageRow{{Day: utc(2026, 11, 2, 0), EventsIngested: 7, DevicesActive: 3, DevicesPeak: 2, DeviceHours: 48,
		RolledUpAt: utc(2026, 11, 3, 1)}}
	ledger := map[string]int64{"2026-11-01": usageFullDay, "2026-11-02": usageFullDay, "2026-11-03": 1<<5 - 1}

	got := fillUsageDays("acme", f, e, rows, ledger)
	require.Len(t, got, 3)

	assert.Equal(t, int64(0), got[0].EventsIngested, "a day with no row reads zero")
	assert.Equal(t, 0, got[0].DevicesPeak)
	assert.True(t, got[0].Complete, "every hour measured: zero is its final figure")

	assert.Equal(t, int64(7), got[1].EventsIngested)
	assert.Equal(t, 2.0, got[1].DevicesAvg())
	assert.True(t, got[1].Complete)

	assert.False(t, got[2].Complete, "five of 24 hours measured")
	for _, d := range got {
		assert.Equal(t, "acme", d.TenantId)
	}
}

// A day the rollup never measured is NOT complete, even long after it ended: a zero there means
// "not measured", and a bill must not read it as "used nothing". Nor is a day with one hour missing.
func TestFillUsageDaysAnUnmeasuredDayIsNotComplete(t *testing.T) {
	day := utc(2026, 11, 1, 0)
	rows := []tenantUsageRow{{Day: day, EventsIngested: 5, RolledUpAt: utc(2026, 11, 2, 1)}}
	got := fillUsageDays("acme", day, day.AddDate(0, 0, 1), rows, map[string]int64{"2026-11-01": usageFullDay &^ (1 << 13)})
	require.Len(t, got, 2)
	assert.False(t, got[0].Complete, "hour 13 was never measured")
	assert.False(t, got[1].Complete, "no ledger row at all")
	assert.True(t, errors.Is(ValidateUsageRange(day, day.AddDate(0, 0, -1)), ErrUsageRange))
}

func TestFirstHourOwed(t *testing.T) {
	d := utc(2026, 11, 1, 0)
	ledger := map[string]int64{"2026-11-01": usageFullDay &^ (1 << 22)}
	h, ok := firstHourOwed(ledger, d, d.Add(48*time.Hour))
	require.True(t, ok)
	assert.Equal(t, utc(2026, 11, 1, 22), h)

	ledger["2026-11-01"] = usageFullDay
	h, ok = firstHourOwed(ledger, d, d.Add(48*time.Hour))
	require.True(t, ok)
	assert.Equal(t, utc(2026, 11, 2, 0), h, "the next day has no ledger row")

	_, ok = firstHourOwed(ledger, d, d.Add(24*time.Hour))
	assert.False(t, ok)
}
