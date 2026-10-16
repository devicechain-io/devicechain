// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventtime

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Every instant the count can hold comes back as the same instant, to the nanosecond,
// in UTC, including both ends of the range and the epoch itself.
func TestWireNanosRoundTripsToTheNanosecond(t *testing.T) {
	for name, at := range map[string]time.Time{
		"now-ish":            time.Date(2026, 10, 16, 9, 30, 15, 123456789, time.UTC),
		"one nanosecond":     time.Date(2026, 10, 16, 9, 30, 15, 1, time.UTC),
		"device offset":      time.Date(2026, 10, 16, 9, 30, 15, 987654321, time.FixedZone("x", -7*3600-30*60)),
		"epoch":              time.Unix(0, 0),
		"before the epoch":   time.Date(1969, 12, 31, 23, 59, 59, 999999999, time.UTC),
		"lowest it holds":    time.Unix(0, math.MinInt64),
		"highest it holds":   time.Unix(0, math.MaxInt64),
		"leap-day reading":   time.Date(2028, 2, 29, 23, 59, 59, 999999999, time.UTC),
		"microsecond stored": time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC),
	} {
		nanos, ok := WireNanos(at)
		require.True(t, ok, name)
		back := FromWireNanos(nanos)
		require.True(t, at.Equal(back), "%s: %s came back as %s", name, at, back)
		require.Equal(t, time.UTC, back.Location(), name)
		require.Equal(t, at.UTC().Format(time.RFC3339Nano), back.Format(time.RFC3339Nano), name)
	}
}

// An instant outside the count's range is reported as such, never wrapped onto another
// instant: the caller writes the string form for it.
func TestWireNanosRefusesWhatTheCountCannotHold(t *testing.T) {
	for name, at := range map[string]time.Time{
		"zero time":             {},
		"year 1":                time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
		"one ns below range":    time.Unix(0, math.MinInt64).Add(-time.Nanosecond),
		"one ns above range":    time.Unix(0, math.MaxInt64).Add(time.Nanosecond),
		"a device in year 9999": time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
	} {
		_, ok := WireNanos(at)
		require.False(t, ok, name)
		// The string form holds it, to the nanosecond.
		parsed, err := time.Parse(time.RFC3339Nano, WireString(at))
		require.NoError(t, err, name)
		require.True(t, at.Equal(parsed), name)
	}
}
