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
