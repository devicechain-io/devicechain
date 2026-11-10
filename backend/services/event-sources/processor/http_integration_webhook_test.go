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

func TestGenericAckPathsWithFeedOffPublishUplinks(t *testing.T) {
	for _, kind := range []integrationcodec.Type{integrationcodec.GenericJSON, integrationcodec.GenericBinary} {
		t.Run(string(kind), func(t *testing.T) {
			h := newRouteHarness(t)
			h.backend.desc = func() integrationcodec.Descriptor {
				s := `{"idFrom":"header:X-Device"}`
				return integrationcodec.Descriptor{Type: kind, Identity: "DEV_EUI", Generic: &s}
			}
			h.route.Metrics.DownlinkReport = func(result string) { h.bump("report/" + result) }
			body := `{"temperature":21.5}`
			if kind == integrationcodec.GenericBinary {
				body = "\x01\xff"
				h.withDecoder(framePlan(), nil)
			}
			rec := h.post(csPath+"/ack", goodBearer, body, func(r *http.Request) {
				r.Header.Set("X-Device", "0011223344556677")
				if kind == integrationcodec.GenericBinary {
					r.Header.Set("Content-Type", "application/octet-stream")
				}
			})
			require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			require.Len(t, h.published, 1)
			require.Equal(t, "dev-0011223344556677", h.published1().event.Device)
			require.Equal(t, 1, h.count("uplink/published"))
			require.Zero(t, h.count("report/not_routed"))
		})
	}
}

func TestGenericFeedOnRefusesUplinkOnAckPath(t *testing.T) {
	h := newRouteHarness(t)
	sink := withReportSink(h)
	h.backend.desc = func() integrationcodec.Descriptor {
		s := `{"idFrom":"pointer:/device"}`
		return integrationcodec.Descriptor{Type: integrationcodec.GenericJSON, Identity: "DEV_EUI", Generic: &s}
	}
	rec := h.post(csPath+"/downlink-x", goodBearer, `{"device":"0011223344556677","temperature":21.5}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Empty(t, sink.reports)
	h.nothingPublished()
}
