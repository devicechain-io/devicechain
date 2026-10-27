// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// Through the Client, as the service builds it: the source's writable-metric allowlist is what bounds
// the capture. A metric on it is reported, one that is not is not - even though both are declared.
func TestTheClientCapturesOnlyTheSourcesWritableMetrics(t *testing.T) {
	r := newRebirthRig(t) // writableMetrics: Setpoint
	r.c.onMessage(nil, fakeMessage{topic: "spBv1.0/plant-a/NDATA/node-3", payload: encodeT(t, &sppb.Payload{
		Seq: proto.Uint64(2), Metrics: []*sppb.Payload_Metric{
			{Alias: proto.Uint64(9), Value: &sppb.Payload_Metric_IntValue{IntValue: 4}},
		}})})
	r.c.onMessage(nil, fakeMessage{topic: "spBv1.0/plant-a/DDATA/node-3/pump-1", payload: encodeT(t, &sppb.Payload{
		Seq: proto.Uint64(3), Metrics: []*sppb.Payload_Metric{
			{Alias: proto.Uint64(50), Value: &sppb.Payload_Metric_BooleanValue{BooleanValue: true}},
		}})})
	r.mu.Lock()
	defer r.mu.Unlock()
	var names []string
	for _, x := range r.reports {
		if x.Kind == ReportMetric {
			names = append(names, x.Name)
		}
	}
	require.Equal(t, []string{"Setpoint"}, names, "a metric outside the allowlist was reported, or the allowed one was not")
	assert.Equal(t, WireValue{Kind: 'i', N: 4}, r.reports[0].Value)
}
