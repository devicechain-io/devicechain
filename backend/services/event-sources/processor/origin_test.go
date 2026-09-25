// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each transport tells the ONE gate whether its tenant was authenticated. The gate
// decides what that means; the transport only has to say it truthfully, and these are
// the three places it does.
func TestEveryTransportPassesItsOrigin(t *testing.T) {
	record := func(into *[]Origin) RateGate {
		return func(_, _ string, _ time.Time, _ bool, origin Origin) bool {
			*into = append(*into, origin)
			return true
		}
	}

	var mqtt []Origin
	es, _ := newTestMqttSource(t, record(&mqtt))
	es.onMessage(nil, &fakeMqttMessage{topic: "inst-1/acme/events", payload: []byte(`{"device":"d1"}`)})
	assert.Equal(t, []Origin{OriginAuthenticated}, mqtt, "the platform-broker MQTT source")

	var capture []Origin
	h := newCaptureHarness(t)
	h.gate = record(&capture)
	h.source.handle(capturedMsgAt(captureSubject, validEvent, 1, time.Now(), &recordingAck{}))
	assert.Equal(t, []Origin{OriginAuthenticated}, capture, "the capture-stream source")

	var httpOrigins []Origin
	hs, _, _ := newTestHttpSource(t, record(&httpOrigins))
	rec := httptest.NewRecorder()
	hs.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/inst-1/acme/events",
		strings.NewReader(canonicalMeasurementBody)))
	assert.Equal(t, []Origin{OriginUntrusted}, httpOrigins, "HTTP ingest's path-segment tenant")
}

// Through the real gate: an untrusted origin is metered in the bounded pool of the
// untrusted limiter, and the ZERO Origin — a transport that forgot to say — is treated
// the same way, never as authenticated. Neither reaches the live or backlog limiter.
func TestUntrustedAndUnsaidOriginsAreBounded(t *testing.T) {
	for _, origin := range []Origin{OriginUntrusted, 0} {
		live := core.NewTenantRateLimiter(core.StaticCeiling(0.001, 1))
		backlog := core.NewTenantRateLimiter(core.StaticCeiling(0.001, 1))
		untrusted := core.NewTenantRateLimiter(core.StaticCeiling(0.001, 1))
		gate := NewRateGate(live, backlog, untrusted, nil)

		for i := 0; i < 5000; i++ {
			gate("http", fmt.Sprintf("invented-%d", i), time.Time{}, false, origin)
		}
		confirmed, pooled, overflow := untrusted.BucketCounts()
		require.Equalf(t, 0, confirmed, "origin %d: no invented name may get a confirmed bucket", origin)
		assert.Equalf(t, 1024, pooled, "origin %d: the pool is full and no larger", origin)
		assert.Truef(t, overflow, "origin %d: names past the pool share the overflow", origin)
		for name, l := range map[string]*core.TenantRateLimiter{"live": live, "backlog": backlog} {
			c, p, o := l.BucketCounts()
			assert.Truef(t, c == 0 && p == 0 && !o,
				"origin %d: an untrusted message reached the %s limiter (%d confirmed, %d pooled, overflow %v)",
				origin, name, c, p, o)
		}
	}
}
