// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/integration"
	"github.com/stretchr/testify/require"
)

// THE DECODER RIG'S `mixed` PHASE, IN A TEST. A device was resolved, more than the device cache's
// TTL passed, and then device-management became unreachable. The uplink must still be accepted
// and published (parked on the stream until device-management, which re-checks the integration,
// the device and its alias at the event, comes back), not refused 503.
func TestAnUplinkIsAcceptedFromAStaleDeviceWhileDeviceManagementIsDown(t *testing.T) {
	h := newRouteHarness(t)
	require.Equal(t, http.StatusAccepted, h.up().Code, "warm")
	require.Zero(t, h.count("request/CHIRPSTACK_V4/"+ResultResolveStale), "a fresh answer is not stale")

	h.backend.mu.Lock()
	h.backend.down = true
	h.backend.mu.Unlock()
	h.backend.resolve = func(string, string) (integration.Resolution, error) {
		return integration.Resolution{}, errors.New("connection refused")
	}
	h.now = h.now.Add(integration.DefaultPositiveTTL + 8*time.Second)

	rec := h.up()
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Zero(t, h.count("request/CHIRPSTACK_V4/"+ResultResolveUnavailable))
	require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/"+ResultResolveStale), "a stale serve is counted")
	require.Equal(t, 2, h.count("request/CHIRPSTACK_V4/"+ResultAccepted), "in addition to the result it ended in")
	h.mu.Lock()
	require.Len(t, h.published, 2, "the uplink was published, to be resolved when device-management returns")
	h.mu.Unlock()

	// The counterweight: past the stale bound the device lookup's failure is a 503 again. The
	// token pre-check is let answer, so the 503 is the device lookup's own.
	h.backend.mu.Lock()
	h.backend.down = false
	h.backend.mu.Unlock()
	h.now = h.now.Add(integration.DefaultDeviceStaleMax)
	require.Equal(t, http.StatusServiceUnavailable, h.up().Code)
	require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/"+ResultResolveUnavailable))
}

// A device-management that answers that the identifier is no longer registered is believed even
// when a positive answer is still held: nothing is published for it.
func TestAStaleDeviceIsNotServedOverADefinitiveUnregistered(t *testing.T) {
	h := newRouteHarness(t)
	require.Equal(t, http.StatusAccepted, h.up().Code, "warm")
	h.backend.resolve = func(string, string) (integration.Resolution, error) {
		return integration.Resolution{Outcome: integration.Unregistered}, nil
	}
	h.now = h.now.Add(integration.DefaultPositiveTTL + time.Minute)
	require.Equal(t, http.StatusAccepted, h.up().Code)
	require.Zero(t, h.count("request/CHIRPSTACK_V4/"+ResultResolveStale))
	require.Equal(t, 1, h.count("uplink/unknown_device"))
	h.mu.Lock()
	require.Len(t, h.published, 1, "only the warm-up was published")
	h.mu.Unlock()
}

// A downlink report goes through the same device lookup and is served the same way: the adapter
// matches it against the downlink it holds by queue item, integration and device token, so a
// stale device can only fail to match, never settle another device's downlink.
func TestADownlinkReportIsAcceptedFromAStaleDeviceAndCounted(t *testing.T) {
	h := newRouteHarness(t)
	sink := withReportSink(h)
	require.Equal(t, http.StatusAccepted, h.post(csPath+"?event=ack", goodBearer, fixtureBody(t, "chirpstack/ack.json")).Code)

	h.backend.resolve = func(string, string) (integration.Resolution, error) {
		return integration.Resolution{}, errors.New("connection refused")
	}
	h.now = h.now.Add(integration.DefaultPositiveTTL + time.Minute)
	rec := h.post(csPath+"?event=ack", goodBearer, fixtureBody(t, "chirpstack/ack.json"))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Len(t, sink.reports, 2)
	require.Equal(t, "dev-0101010101010101", sink.reports[1].DeviceToken)
	require.Equal(t, 1, h.count("request/CHIRPSTACK_V4/"+ResultResolveStale))
}
