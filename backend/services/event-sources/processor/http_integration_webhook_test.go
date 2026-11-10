// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/integrationcodec"
	"github.com/stretchr/testify/require"
)

func TestGenericAckRoutePublishesProvenDeviceAndBypassesTelemetryShedding(t *testing.T) {
	h := newRouteHarness(t)
	sink := withReportSink(h)
	h.backend.desc = func() integrationcodec.Descriptor {
		s := `{"idFrom":"pointer:/device"}`
		return integrationcodec.Descriptor{Type: integrationcodec.GenericJSON, Identity: "DEV_EUI", Generic: &s}
	}
	h.allowOK.Store(false)
	key := "dc:0123456789abcdef0123456789abcdef"
	for _, path := range []string{"/ack", "/downlink-ack", "/downlink_nack"} {
		rec := h.post(csPath+path, goodBearer, `{"device":"aabbccddeeff0011","correlationId":"`+key+`","status":"acknowledged"}`)
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	}
	require.Len(t, sink.reports, 3)
	for _, rep := range sink.reports {
		require.Equal(t, cmddownlink.DownlinkReport{Integration: "cs", Kind: "ack", DevEUI: "AABBCCDDEEFF0011", DeviceRef: "webhook", DeviceToken: "dev-aabbccddeeff0011", QueueItemID: key, Acknowledged: true}, rep)
	}
	require.Equal(t, 3, h.count("report/published"))
	h.nothingPublished()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	for path, want := range map[string]bool{"ack": true, "downlink-ack": true, "downlink/nack": true, "acknowledge": false, "ackx": false, "": false} {
		require.Equal(t, want, isDownlinkEvent(integrationcodec.GenericJSON, r, path), path)
	}
	require.False(t, isDownlinkEvent(integrationcodec.GenericBinary, r, "ack"))
}
