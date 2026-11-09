// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/stretchr/testify/require"
)

// A NETWORK SERVER'S DOWNLINK REPORT IS WRITTEN TO ITS OWN STREAM AND NEVER TO inbound-events, where
// a device-management built before it would fail to decode it. The tenant rides the writer's
// context (the subject), and nothing in the body names one.
func TestADownlinkReportIsWrittenToItsOwnStreamAndNeverToInboundEvents(t *testing.T) {
	savedInbound, savedReports := InboundEventsWriter, LpwanDownlinkEventsWriter
	t.Cleanup(func() { InboundEventsWriter, LpwanDownlinkEventsWriter = savedInbound, savedReports })
	inbound, reports := &recordingWriter{}, &recordingWriter{}
	InboundEventsWriter, LpwanDownlinkEventsWriter = inbound, reports

	rep := cmddownlink.DownlinkReport{Integration: "cs", Kind: "ack", DevEUI: "0101010101010101",
		QueueItemID: "5e0a6c1e-6a1c-4f38-9d2b-0c7a2b7d9f11", Acknowledged: true}
	require.NoError(t, onDownlinkReport("acme", rep))
	require.Zero(t, inbound.count())
	require.Equal(t, 1, reports.count())

	var got cmddownlink.DownlinkReport
	require.NoError(t, json.Unmarshal(reports.msgs[0].Value, &got))
	require.Equal(t, rep, got)
	require.NotContains(t, string(reports.msgs[0].Value), "acme", "the tenant is the subject's, not the body's")
}

func TestADownlinkReportWithNoWriterOrTenantIsRefusedLoudly(t *testing.T) {
	saved := LpwanDownlinkEventsWriter
	t.Cleanup(func() { LpwanDownlinkEventsWriter = saved })
	rep := cmddownlink.DownlinkReport{Integration: "cs", Kind: "ack", DevEUI: "01", QueueItemID: "q"}

	LpwanDownlinkEventsWriter = nil
	require.Error(t, onDownlinkReport("acme", rep), "no stream writer must be a refusal, never a silent drop")

	LpwanDownlinkEventsWriter = &recordingWriter{}
	require.Error(t, onDownlinkReport("", rep))
	require.Zero(t, LpwanDownlinkEventsWriter.(*recordingWriter).count())

	// The paired positive.
	require.NoError(t, onDownlinkReport("acme", rep))
	require.Equal(t, 1, LpwanDownlinkEventsWriter.(*recordingWriter).count())
}
