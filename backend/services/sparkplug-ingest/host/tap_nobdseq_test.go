// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"

	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// The session machine accepts a node birth that declares no bdSeq (it only cannot correlate that
// node's death), so the tap reports it with HasBdSeq false - and the dispatcher, which confirms a
// reboot only on two known, different sequences, must be handed exactly that fact.
func TestTheTapReportsAnAcceptedNodeBirthWithoutABdSeqAsSuch(t *testing.T) {
	rig := newSpRig(t, spWindow, 0)
	_, col, _ := tapClient(t, rig)
	feed := rig.feeder()
	nb := encodeT(t, &sppb.Payload{Seq: proto.Uint64(0), Metrics: []*sppb.Payload_Metric{
		{Name: proto.String("Setpoint"), Alias: proto.Uint64(10), Datatype: proto.Uint32(datatypeInt8),
			Value: &sppb.Payload_Metric_IntValue{IntValue: 1}},
	}})
	feed.publish(wireNBirth, nb)
	col.wait(t, "the NBIRTH without a bdSeq was never reported", func() bool { return len(col.births()) == 1 })
	assert.False(t, col.births()[0].HasBdSeq, "a birth that declared no bdSeq was reported as having one")
}
