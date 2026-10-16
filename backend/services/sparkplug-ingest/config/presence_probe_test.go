// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPresenceProbeDefaultsWhenUnset pins the fail-safe: an unwritten interval is the
// platform default, never zero and never "off". A Host that re-checks nothing is the hole
// the probe exists to close, so the absence of the key must not reopen it.
func TestPresenceProbeDefaultsWhenUnset(t *testing.T) {
	c := NewSparkplugConfiguration()
	assert.Equal(t, DefaultPresenceProbeSeconds, c.Presence.ProbeSeconds)
	assert.Equal(t, DefaultPresenceProbeSeconds*time.Second, c.Presence.ProbeInterval())
	require.NoError(t, c.Validate())

	// The duration accessor applies the same default on its own, for a struct that was
	// built without ApplyDefaults.
	assert.Equal(t, DefaultPresenceProbeSeconds*time.Second, Presence{}.ProbeInterval())
	assert.Equal(t, DefaultPresenceProbeSeconds*time.Second, Presence{ProbeSeconds: -5}.ProbeInterval(),
		"a negative value renders as the default rather than a negative duration (Validate refuses it before this is reached)")
}

// TestPresenceProbeHonoursAWrittenValue is the counterweight: a value at or above the
// floor is used as written, not defaulted over.
func TestPresenceProbeHonoursAWrittenValue(t *testing.T) {
	c := &SparkplugConfiguration{Presence: Presence{ProbeSeconds: 900}}
	c.ApplyDefaults()
	require.NoError(t, c.Validate())
	assert.Equal(t, 900*time.Second, c.Presence.ProbeInterval())

	atFloor := &SparkplugConfiguration{Presence: Presence{ProbeSeconds: MinPresenceProbeSeconds}}
	atFloor.ApplyDefaults()
	require.NoError(t, atFloor.Validate(), "the floor itself is a valid interval")
}

// TestPresenceProbeRefusesBelowFloor proves a written interval under the floor fails the
// load rather than being clamped: 1..59 and any negative value. ApplyDefaults must leave a
// negative value alone for Validate to see it; a defaults pass that "fixed" it would turn a
// written mistake into the default silently.
func TestPresenceProbeRefusesBelowFloor(t *testing.T) {
	for _, s := range []int{1, 30, MinPresenceProbeSeconds - 1, -1, -300} {
		c := &SparkplugConfiguration{Presence: Presence{ProbeSeconds: s}}
		c.ApplyDefaults()
		assert.Equal(t, s, c.Presence.ProbeSeconds, "ApplyDefaults must not rewrite a written value (%d)", s)
		err := c.Validate()
		require.Error(t, err, "probeSeconds=%d must be refused", s)
		assert.Contains(t, err.Error(), "presence.probeSeconds")
	}
}

// TestPresenceProbeWindowDefaultsAndBounds: the answer window is the platform default
// when unwritten, and a written one must sit in [floor, interval) — refused, not clamped,
// outside it. The interval it is compared against is the EFFECTIVE one, so a window
// written alongside no interval is checked against the default.
func TestPresenceProbeWindowDefaultsAndBounds(t *testing.T) {
	c := NewSparkplugConfiguration()
	assert.Equal(t, 1800, c.Presence.ProbeSeconds, "the default interval is thirty minutes")
	assert.Equal(t, 10, c.Presence.ProbeWindowSeconds, "the default answer window is ten seconds")
	assert.Equal(t, 10*time.Second, c.Presence.ProbeWindow())
	assert.Equal(t, 10*time.Second, Presence{}.ProbeWindow())
	assert.Equal(t, 10*time.Second, Presence{ProbeWindowSeconds: -3}.ProbeWindow())

	for _, tc := range []struct {
		interval, window int
		ok               bool
	}{
		{0, MinPresenceProbeWindowSeconds, true},
		{0, MinPresenceProbeWindowSeconds - 1, false},
		{0, 1, false},
		{0, -1, false},
		{0, DefaultPresenceProbeSeconds - 1, true},
		{0, DefaultPresenceProbeSeconds, false},
		{60, 59, true},
		{60, 60, false},
		{60, 61, false},
		{900, 30, true},
	} {
		c := &SparkplugConfiguration{Presence: Presence{ProbeSeconds: tc.interval, ProbeWindowSeconds: tc.window}}
		c.ApplyDefaults()
		assert.Equal(t, tc.window, c.Presence.ProbeWindowSeconds, "ApplyDefaults rewrote a written window (%d)", tc.window)
		err := c.Validate()
		if tc.ok {
			assert.NoError(t, err, "interval=%d window=%d", tc.interval, tc.window)
			assert.Equal(t, time.Duration(tc.window)*time.Second, c.Presence.ProbeWindow())
		} else if assert.Error(t, err, "interval=%d window=%d must be refused", tc.interval, tc.window) {
			assert.Contains(t, err.Error(), "presence.probeWindowSeconds")
		}
	}

	// Validate's own contract, without a defaults pass first: an unwritten interval is the
	// effective default, not zero, so a window written alone is judged against 1800.
	assert.NoError(t, (&SparkplugConfiguration{Presence: Presence{ProbeWindowSeconds: 30}}).Validate(),
		"a window written with no interval was judged against an interval of zero")
}
