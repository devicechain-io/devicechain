// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplyDefaultsFillsHostIdButNotGroups pins that HostId defaults while an
// empty Groups stays empty — the two have different meaning (all-groups vs
// unset), and defaulting Groups would silently narrow the subscription.
func TestApplyDefaultsFillsHostIdButNotGroups(t *testing.T) {
	c := &SparkplugConfiguration{}
	c.ApplyDefaults()
	assert.Equal(t, DefaultHostId, c.HostId)
	assert.Empty(t, c.Groups)

	// A configured HostId is preserved, not overwritten.
	c2 := &SparkplugConfiguration{HostId: "plant-a"}
	c2.ApplyDefaults()
	assert.Equal(t, "plant-a", c2.HostId)
}

// TestValidateAcceptsCleanConfig is the counterweight to the rejection cases: a
// well-formed config with real group names must pass untouched.
func TestValidateAcceptsCleanConfig(t *testing.T) {
	c := &SparkplugConfiguration{HostId: "devicechain", Groups: []string{"plant-a", "plant-b"}}
	require.NoError(t, c.Validate())

	// Empty Groups (subscribe to all) is valid.
	c2 := &SparkplugConfiguration{HostId: "devicechain"}
	require.NoError(t, c2.Validate())
}

// TestValidateRejectsTopicMetacharacters proves the fail-closed guard: any value
// that could escape its MQTT topic level is rejected, for the HostId (which
// addresses the retained STATE message) and for a group (which scopes a
// subscription). A '/' in either would silently mis-target.
func TestValidateRejectsTopicMetacharacters(t *testing.T) {
	cases := []struct {
		name string
		cfg  SparkplugConfiguration
	}{
		{"empty hostId", SparkplugConfiguration{HostId: ""}},
		{"hostId with slash", SparkplugConfiguration{HostId: "a/b"}},
		{"hostId with plus", SparkplugConfiguration{HostId: "a+b"}},
		{"hostId with hash", SparkplugConfiguration{HostId: "a#b"}},
		{"empty group", SparkplugConfiguration{HostId: "h", Groups: []string{""}}},
		{"group with slash", SparkplugConfiguration{HostId: "h", Groups: []string{"plant/a"}}},
		{"group with wildcard", SparkplugConfiguration{HostId: "h", Groups: []string{"+"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, tc.cfg.Validate())
		})
	}
}
