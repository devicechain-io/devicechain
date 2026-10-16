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

// The two spellings parse, exactly as written, and nothing else does: an operator who
// typed "Compact" gets a refusal at startup, not the default.
func TestParseWireFormat(t *testing.T) {
	for name, want := range map[string]WireFormat{"legacy": WireLegacy, "compact": WireCompact} {
		got, err := ParseWireFormat(name)
		require.NoError(t, err, name)
		require.Equal(t, want, got, name)
		require.Equal(t, name, got.String())
		require.True(t, got.Valid(), name)
	}
	for _, name := range []string{"", "Legacy", "Compact", "COMPACT", " compact", "compact ", "binary", "nanos", "0", "1"} {
		_, err := ParseWireFormat(name)
		require.ErrorIs(t, err, ErrUnknownWireFormat, "%q", name)
	}
	require.False(t, WireFormat(0).Valid(), "the zero value names no format")
	require.False(t, WireFormat(3).Valid())
}

// 🔴 This release writes the legacy format unless told otherwise: the release that
// introduces the compact reader must not also be the one that writes it by default.
// Flip it in the NEXT release, and this assertion with it.
func TestTheDefaultWireFormatIsLegacyInTheReleaseThatAddsTheReader(t *testing.T) {
	require.Equal(t, WireLegacy, DefaultWireFormat)
	require.Equal(t, DefaultWireFormat, ProducerWireFormat(), "a process that loads no instance document writes the default")
}

// The process setting takes a valid format and refuses anything else without changing.
func TestSetProducerWireFormat(t *testing.T) {
	t.Cleanup(func() { require.NoError(t, SetProducerWireFormat(DefaultWireFormat)) })
	require.NoError(t, SetProducerWireFormat(WireCompact))
	require.Equal(t, WireCompact, ProducerWireFormat())
	require.ErrorIs(t, SetProducerWireFormat(0), ErrUnknownWireFormat)
	require.ErrorIs(t, SetProducerWireFormat(9), ErrUnknownWireFormat)
	require.Equal(t, WireCompact, ProducerWireFormat(), "a refused format leaves the setting as it was")
	require.NoError(t, SetProducerWireFormat(WireLegacy))
	require.Equal(t, WireLegacy, ProducerWireFormat())
}

// 🔴 RFC 3339 carries only the years 0000 to 9999, counted in the offset the string is
// written in. An instant a device can legitimately send, 9999-12-31T23:59:59-05:00, is in
// the year 10000 in UTC: written in UTC it is a string no consumer can parse, and the
// message carrying it is refused downstream. The string form falls back to the instant's
// own offset for it, and the reader keeps that offset for it, so it survives a hop.
func TestTheStringFormStaysParseableAtTheEndsOfTheYearRange(t *testing.T) {
	late := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.FixedZone("", -5*3600))
	early := time.Date(0, 1, 1, 0, 0, 0, 1, time.FixedZone("", 3*3600))
	require.Equal(t, 10000, late.UTC().Year(), "the premise: UTC cannot carry it")
	require.Equal(t, -1, early.UTC().Year(), "the premise: UTC cannot carry it")
	for name, at := range map[string]time.Time{"late": late, "early": early} {
		for form, text := range map[string]string{
			"wire":        WireString(at),
			"own offset":  string(AppendOwnOffsetWireString(nil, at)),
			"appended to": string(AppendWireString([]byte("x"), at))[1:],
		} {
			back, err := ParseWireString(text)
			require.NoError(t, err, "%s/%s: %q", name, form, text)
			require.True(t, back.Equal(at), "%s/%s", name, form)
			// And it is writable again from what the reader returned: the next hop.
			again, err := ParseWireString(WireString(back))
			require.NoError(t, err, "%s/%s: the next hop", name, form)
			require.True(t, again.Equal(at), "%s/%s", name, form)
		}
	}

	// Everything UTC can carry is written and read in UTC, the offset dropped.
	at := time.Date(9999, 12, 31, 18, 0, 0, 5, time.FixedZone("", -5*3600))
	require.Equal(t, "9999-12-31T23:00:00.000000005Z", WireString(at))
	require.Equal(t, "9999-12-31T18:00:00.000000005-05:00", string(AppendOwnOffsetWireString(nil, at)))
	back, err := ParseWireString("9999-12-31T18:00:00.000000005-05:00")
	require.NoError(t, err)
	require.Equal(t, time.UTC, back.Location())
	// An instant whose OWN offset cannot carry it, but UTC can: the legacy envelope form
	// falls back to UTC rather than write a string nobody can parse.
	own := time.Date(9999, 12, 31, 23, 0, 0, 0, time.UTC).In(time.FixedZone("", 2*3600))
	require.Equal(t, 10000, own.Year(), "the premise: its own offset cannot carry it")
	require.Equal(t, "9999-12-31T23:00:00Z", string(AppendOwnOffsetWireString(nil, own)))
	_, err = ParseWireString("10000-01-01T04:59:59Z")
	require.Error(t, err, "the string that must never be written")
}
