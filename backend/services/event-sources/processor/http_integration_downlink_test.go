// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/integration"
	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/integrationcodec"
	"github.com/devicechain-io/dc-microservice/sourcelimit"
	"github.com/stretchr/testify/require"
)

// A NETWORK SERVER'S REPORT ON A DOWNLINK (ChirpStack ack / txack) goes through the same bearer
// proof, enabled check and device resolution as an uplink, and is then written to the downlink
// report stream stamped with the integration the token proved. Every refusal is paired with the
// request that is accepted.

type reportSink struct {
	mu      sync.Mutex
	tenants []string
	reports []cmddownlink.DownlinkReport
	err     error
}

func (s *reportSink) publish(tenant string, r cmddownlink.DownlinkReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.tenants = append(s.tenants, tenant)
	s.reports = append(s.reports, r)
	return nil
}

func withReportSink(h *routeHarness) *reportSink {
	s := &reportSink{}
	h.route.PublishDownlink = s.publish
	h.route.DownlinkLimiter = sourcelimit.Must(sourcelimit.Limits{Burst: 100000, Every: time.Millisecond, MaxKeys: 100})
	h.route.Metrics.DownlinkReport = func(result string) { h.bump("report/" + result) }
	return s
}

func TestAnAckIsPublishedForTheIntegrationsDeviceStampedWithTheProvenIntegration(t *testing.T) {
	h := newRouteHarness(t)
	sink := withReportSink(h)
	rec := h.post(csPath+"?event=ack", goodBearer, fixtureBody(t, "chirpstack/ack.json"))
	require.Equal(t, http.StatusAccepted, rec.Code)
	h.nothingPublished()
	require.Equal(t, []string{"acme"}, sink.tenants, "the tenant is the one the path proved, never one in the body")
	require.Equal(t, []cmddownlink.DownlinkReport{{
		Integration: "cs", Kind: "ack", DevEUI: "0101010101010101",
		QueueItemID: "5e0a6c1e-6a1c-4f38-9d2b-0c7a2b7d9f11", Acknowledged: true, FCntDown: 12,
	}}, sink.reports)
	require.Equal(t, 1, h.count("report/published"))
	require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/accepted"))

	// The timeout form of the same event: acknowledged is absent, and the report says false.
	rec = h.post(csPath+"?event=ack", goodBearer, fixtureBody(t, "chirpstack/ack_timeout.json"))
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.False(t, sink.reports[1].Acknowledged)
	rec = h.post(csPath+"?event=txack", goodBearer, fixtureBody(t, "chirpstack/txack.json"))
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Equal(t, "txack", sink.reports[2].Kind)
}

func TestADownlinkReportNeedsTheIntegrationsBearerToken(t *testing.T) {
	h := newRouteHarness(t)
	sink := withReportSink(h)
	body := fixtureBody(t, "chirpstack/ack.json")
	require.Equal(t, http.StatusUnauthorized, h.post(csPath+"?event=ack", "wrong-token-0123456789abcdefghijklmnopqrstuvwxy", body).Code)
	require.Equal(t, http.StatusUnauthorized, h.post(csPath+"?event=ack", "", body).Code)
	require.Empty(t, sink.reports, "an unauthenticated report reached the stream")
	require.Equal(t, http.StatusAccepted, h.post(csPath+"?event=ack", goodBearer, body).Code)
	require.Len(t, sink.reports, 1)

	// A disabled integration's valid token is refused, not published.
	h.backend.enabled = false
	h.route.Auth.Forget("acme", "cs", goodBearer)
	require.Equal(t, http.StatusForbidden, h.post(csPath+"?event=ack", goodBearer, body).Code)
	require.Len(t, sink.reports, 1)
}

func TestADownlinkReportForADeviceTheIntegrationDoesNotGovernIsCountedAndDropped(t *testing.T) {
	h := newRouteHarness(t)
	sink := withReportSink(h)
	h.backend.resolve = func(string, string) (integration.Resolution, error) {
		return integration.Resolution{Outcome: integration.Unregistered}, nil
	}
	require.Equal(t, http.StatusAccepted, h.post(csPath+"?event=ack", goodBearer, fixtureBody(t, "chirpstack/ack.json")).Code)
	require.Empty(t, sink.reports)
	require.Equal(t, 1, h.count("report/unknown_device"))
	require.Zero(t, h.count("report/published"))

	// The paired positive, for a different device (an unregistered answer is remembered per device).
	h.backend.resolve = nil
	other := strings.Replace(fixtureBody(t, "chirpstack/ack.json"), "0101010101010101", "0303030303030303", 1)
	require.Equal(t, http.StatusAccepted, h.post(csPath+"?event=ack", goodBearer, other).Code)
	require.Len(t, sink.reports, 1)
	require.Equal(t, "0303030303030303", sink.reports[0].DevEUI)
}

func TestADownlinkReportLookupOrPublishFailureIsA503(t *testing.T) {
	h := newRouteHarness(t)
	sink := withReportSink(h)
	h.backend.resolve = func(string, string) (integration.Resolution, error) {
		return integration.Resolution{}, errors.New("device-management is unreachable")
	}
	require.Equal(t, http.StatusServiceUnavailable, h.post(csPath+"?event=ack", goodBearer, fixtureBody(t, "chirpstack/ack.json")).Code)
	require.Empty(t, sink.reports)

	h.backend.resolve = nil
	sink.err = errors.New("stream unavailable")
	require.Equal(t, http.StatusServiceUnavailable, h.post(csPath+"?event=ack", goodBearer, fixtureBody(t, "chirpstack/ack.json")).Code)
	require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/publish_failed"))
	sink.err = nil
	require.Equal(t, http.StatusAccepted, h.post(csPath+"?event=ack", goodBearer, fixtureBody(t, "chirpstack/ack.json")).Code)
}

func TestWithoutADownlinkStreamAReportIsIgnoredWithA204AsBefore(t *testing.T) {
	h := newRouteHarness(t)
	h.route.Metrics.DownlinkReport = func(result string) { h.bump("report/" + result) }
	rec := h.post(csPath+"?event=ack", goodBearer, fixtureBody(t, "chirpstack/ack.json"))
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/ignored_type"))
	require.Equal(t, 1, h.count("report/not_routed"))
	h.nothingPublished()
}

func TestAMalformedDownlinkReportIsRefusedAndNeverPublished(t *testing.T) {
	h := newRouteHarness(t)
	sink := withReportSink(h)
	body := strings.Replace(fixtureBody(t, "chirpstack/ack.json"), "5e0a6c1e-6a1c-4f38-9d2b-0c7a2b7d9f11", "not-a-uuid", 1)
	require.Equal(t, http.StatusBadRequest, h.post(csPath+"?event=ack", goodBearer, body).Code)
	require.Empty(t, sink.reports)
}

func TestADownlinkReportIsNotShedWithTelemetryButAnUplinkIs(t *testing.T) {
	h := newRouteHarness(t)
	sink := withReportSink(h)
	h.allowOK.Store(false) // the tenant is over its message ceiling
	require.Equal(t, http.StatusAccepted, h.post(csPath+"?event=ack", goodBearer, fixtureBody(t, "chirpstack/ack.json")).Code)
	require.Len(t, sink.reports, 1, "a dropped acknowledgement would turn a delivered command into a TIMEOUT")

	require.Equal(t, http.StatusAccepted, h.up().Code)
	h.nothingPublished()
	require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/shed_accepted"), "telemetry is still shed")

	// Without the stream wired, a report is shed like any other message.
	h2 := newRouteHarness(t)
	h2.allowOK.Store(false)
	require.Equal(t, http.StatusAccepted, h2.post(csPath+"?event=ack", goodBearer, fixtureBody(t, "chirpstack/ack.json")).Code)
	require.Equal(t, 1, h2.count("request/CHIRPSTACK_V4/shed_accepted"))
}

// Off, a report is not even read: a body that would be refused with the feed on is answered 204 and
// dropped, exactly as before the reports existed, and the documentation says so.
func TestWithTheFeedOffAnAckOrTxAckIsNotReadAt_All(t *testing.T) {
	h := newRouteHarness(t)
	h.route.Metrics.DownlinkReport = func(result string) { h.bump("report/" + result) }
	for _, body := range []string{fixtureBody(t, "chirpstack/ack.json"), "not json", `{}`} {
		for _, ev := range []string{"ack", "txack"} {
			require.Equal(t, http.StatusNoContent, h.post(csPath+"?event="+ev, goodBearer, body).Code, ev+" "+body[:min(10, len(body))])
		}
	}
	require.Empty(t, h.failed, "an unread event is never reported as a failed request")
	require.Equal(t, 6, h.count("report/not_routed"))
	h.nothingPublished()
}

func TestATxAckWithNoQueueItemIsAnsweredA204AndCountedWhenTheFeedIsOn(t *testing.T) {
	h := newRouteHarness(t)
	sink := withReportSink(h)
	body := `{"deviceInfo":{"devEui":"0101010101010101"},"fCntDown":3,"gatewayId":"0102030405060708"}`
	require.Equal(t, http.StatusNoContent, h.post(csPath+"?event=txack", goodBearer, body).Code)
	require.Equal(t, 1, h.count("report/untracked"))
	require.Empty(t, sink.reports)
	require.Empty(t, h.failed)
	// An ack missing it is still refused.
	require.Equal(t, http.StatusBadRequest, h.post(csPath+"?event=ack", goodBearer, body).Code)
}

func TestARouteThatCarriesReportsNeedsALimiterForThem(t *testing.T) {
	h := newRouteHarness(t)
	h.route.PublishDownlink = (&reportSink{}).publish
	h.route.DownlinkLimiter = nil
	require.Error(t, h.route.Validate())
}

// One tenant's flood of reports is limited by its own allowance and cannot evict another tenant's.
func TestAFloodOfReportsIsLimitedPerTenantAndAnotherTenantsStillGetThrough(t *testing.T) {
	h := newRouteHarness(t)
	sink := withReportSink(h)
	h.route.DownlinkLimiter = sourcelimit.Must(sourcelimit.Limits{Burst: 3, Every: time.Hour, MaxKeys: 100})
	body := fixtureBody(t, "chirpstack/ack.json")
	for i := 0; i < 10; i++ {
		require.Equal(t, http.StatusAccepted, h.post(csPath+"?event=ack", goodBearer, body).Code)
	}
	require.Len(t, sink.reports, 3, "only the allowance reached the shared stream")
	require.Equal(t, 7, h.count("report/rate_limited"))
	require.Equal(t, 7, h.count("request/CHIRPSTACK_V4/shed_accepted"))

	// Another tenant's allowance is its own.
	other := "/inst-1/globex/integrations/cs"
	for i := 0; i < 3; i++ {
		require.Equal(t, http.StatusAccepted, h.post(other+"?event=ack", goodBearer, body).Code)
	}
	require.Len(t, sink.reports, 6)
	require.Equal(t, "globex", sink.tenants[5])

	// Telemetry is not charged to it, nor it to telemetry.
	require.Equal(t, http.StatusAccepted, h.up().Code)
	require.Len(t, h.published, 1)
}

// The Things Stack posts each message type to its own path, and the body says what it is.
func TestTheThingsStackDownlinkReportsArePublishedAndNotShedWithTelemetry(t *testing.T) {
	h := newRouteHarness(t)
	sink := withReportSink(h)
	h.backend.desc = func() integrationcodec.Descriptor {
		return integrationcodec.Descriptor{Type: integrationcodec.TTSV3, Identity: "DEV_EUI"}
	}
	h.allowOK.Store(false) // the tenant is over its message ceiling
	for file, path := range map[string]string{
		"tts/downlink_ack_dc.json":    "/downlink-ack",
		"tts/downlink_nack_dc.json":   "/downlinknack",
		"tts/downlink_failed_dc.json": "/downlink_failed",
	} {
		require.Equal(t, http.StatusAccepted, h.post(csPath+path, goodBearer, fixtureBody(t, file)).Code, file)
	}
	require.Len(t, sink.reports, 3)
	kinds := map[string]int{}
	for _, r := range sink.reports {
		kinds[r.Kind]++
		require.Equal(t, "app1/dev1", r.DeviceRef)
		require.Equal(t, "dc:0123456789abcdef0123456789abcdef", r.QueueItemID)
		require.Equal(t, "cs", r.Integration)
	}
	require.Equal(t, map[string]int{"ack": 2, "failed": 1}, kinds)

	// A downlink the platform did not push is answered 204 and published to nothing.
	n := len(sink.reports)
	require.Equal(t, http.StatusNoContent, h.post(csPath+"/downlink-ack", goodBearer, fixtureBody(t, "tts/downlink_ack_foreign.json")).Code)
	require.Len(t, sink.reports, n)
}
