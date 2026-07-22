// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"testing"

	"github.com/devicechain-io/dc-sparkplug-ingest/codec"
	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// nbirthPayload builds a realistic NBIRTH-shaped Sparkplug payload (seq 0 + two
// metrics) and encodes it through the SP0 codec, the same bytes a real producer
// would put on the wire.
func nbirthPayload(t *testing.T) []byte {
	t.Helper()
	p := &sppb.Payload{
		Timestamp: proto.Uint64(1_700_000_000_123),
		Seq:       proto.Uint64(0),
		Metrics: []*sppb.Payload_Metric{
			{Name: proto.String("temperature"), Alias: proto.Uint64(1), Datatype: proto.Uint32(10),
				Value: &sppb.Payload_Metric_DoubleValue{DoubleValue: 21.5}},
			{Name: proto.String("uptime_s"), Alias: proto.Uint64(2), Datatype: proto.Uint32(8),
				Value: &sppb.Payload_Metric_LongValue{LongValue: 42}},
		},
	}
	b, err := codec.Encode(p)
	require.NoError(t, err)
	return b
}

// TestDescribeNodeMessage is the decode+classify check-that-cannot-fail: a real
// NBIRTH payload on a node topic must be classified as an NBIRTH for the right
// node, carrying its seq and metric count. If the topic parse and the payload
// decode are wired to the wrong fields, the asserted values diverge.
func TestDescribeNodeMessage(t *testing.T) {
	rec, err := Describe("spBv1.0/plant-a/NBIRTH/edge-1", nbirthPayload(t))
	require.NoError(t, err)

	assert.Equal(t, NBIRTH, rec.Topic.MessageType)
	assert.Equal(t, "plant-a", rec.Topic.GroupID)
	assert.Equal(t, "edge-1", rec.Topic.EdgeNodeID)
	assert.False(t, rec.Topic.IsDevice())
	assert.True(t, rec.HasSeq)
	assert.Equal(t, uint64(0), rec.Seq)
	assert.Equal(t, 2, rec.MetricCount)
	assert.Equal(t, uint64(1_700_000_000_123), rec.PayloadTimestamp)
	assert.Nil(t, rec.StateOnline)
}

// TestDescribeDeviceMessage confirms a five-level device topic is attributed to
// its device, not folded into the node.
func TestDescribeDeviceMessage(t *testing.T) {
	rec, err := Describe("spBv1.0/plant-a/DDATA/edge-1/sensor-7", nbirthPayload(t))
	require.NoError(t, err)
	assert.Equal(t, DDATA, rec.Topic.MessageType)
	assert.Equal(t, "edge-1", rec.Topic.EdgeNodeID)
	assert.Equal(t, "sensor-7", rec.Topic.DeviceID)
	assert.True(t, rec.Topic.IsDevice())
}

// TestDescribeState decodes a Host STATE message from its JSON body.
func TestDescribeState(t *testing.T) {
	body, err := State{Online: true, Timestamp: 1_700_000_000_500}.Marshal()
	require.NoError(t, err)

	rec, err := Describe("spBv1.0/STATE/devicechain", body)
	require.NoError(t, err)
	assert.True(t, rec.Topic.IsState)
	assert.Equal(t, "devicechain", rec.Topic.HostID)
	require.NotNil(t, rec.StateOnline)
	assert.True(t, *rec.StateOnline)
	assert.Equal(t, uint64(1_700_000_000_500), rec.PayloadTimestamp)
}

// TestDescribeRejectsGarbagePayload is the fail-closed guard: a data topic whose
// body is not valid protobuf is an error, never a silently-empty Record that
// would be logged and counted as a clean zero-metric message.
func TestDescribeRejectsGarbagePayload(t *testing.T) {
	_, err := Describe("spBv1.0/plant-a/NDATA/edge-1", []byte{0x0a, 0x7f})
	require.Error(t, err)
}

// TestDescribeRejectsBadTopic confirms an unparseable topic short-circuits before
// any decode attempt.
func TestDescribeRejectsBadTopic(t *testing.T) {
	_, err := Describe("not/a/sparkplug/topic", nbirthPayload(t))
	require.Error(t, err)
}
