// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseTopicNodeVsDevice is the load-bearing distinction: a device-level
// message carries a fifth topic level and a node-level message does not.
// Mis-counting the segments would attribute device data to the node (or reject a
// valid device topic), so both shapes and the boundary are pinned.
func TestParseTopicNodeVsDevice(t *testing.T) {
	node, err := ParseTopic("spBv1.0/plant-a/NBIRTH/edge-1")
	require.NoError(t, err)
	assert.Equal(t, "plant-a", node.GroupID)
	assert.Equal(t, NBIRTH, node.MessageType)
	assert.Equal(t, "edge-1", node.EdgeNodeID)
	assert.Empty(t, node.DeviceID)
	assert.False(t, node.IsDevice())

	dev, err := ParseTopic("spBv1.0/plant-a/DDATA/edge-1/sensor-7")
	require.NoError(t, err)
	assert.Equal(t, "plant-a", dev.GroupID)
	assert.Equal(t, DDATA, dev.MessageType)
	assert.Equal(t, "edge-1", dev.EdgeNodeID)
	assert.Equal(t, "sensor-7", dev.DeviceID)
	assert.True(t, dev.IsDevice())
}

// TestParseTopicState pins the separate STATE grammar — three levels, outside
// the {group} tree (B4). A parser that assumed the node/device shape would
// mis-read STATE as group="STATE".
func TestParseTopicState(t *testing.T) {
	st, err := ParseTopic("spBv1.0/STATE/devicechain")
	require.NoError(t, err)
	assert.True(t, st.IsState)
	assert.Equal(t, "devicechain", st.HostID)
	assert.Equal(t, STATE, st.MessageType)
	assert.False(t, st.IsDevice())
}

// TestParseTopicRejectsMalformed is the fail-closed guard: anything that is not
// a well-formed Sparkplug topic is an error, not a mis-attributed message. The
// unknown-message-type case is the sharp one — it is exactly how a typo'd or
// non-Sparkplug topic would otherwise be decoded as protobuf.
func TestParseTopicRejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"spBv1.0",
		"spBv1.0/plant-a",
		"other/plant-a/NBIRTH/edge-1",            // wrong namespace
		"spBv1.0/plant-a/NBOGUS/edge-1",          // unknown message type
		"spBv1.0/plant-a/NDATA",                  // too few levels
		"spBv1.0/plant-a/NDATA/edge-1/dev/extra", // too many levels
		"spBv1.0/plant-a/NBIRTH/edge-1/dev",      // node type with a device level
		"spBv1.0/plant-a/DBIRTH/edge-1",          // device type missing its device level
		"spBv1.0/plant-a/NDATA/",                 // empty node
		"spBv1.0//NDATA/edge-1",                  // empty group
		"spBv1.0/STATE/",                         // empty host id
		"spBv1.0/STATE/host/extra",               // STATE with extra level
	}
	for _, topic := range cases {
		t.Run(topic, func(t *testing.T) {
			_, err := ParseTopic(topic)
			require.Error(t, err)
		})
	}
}
