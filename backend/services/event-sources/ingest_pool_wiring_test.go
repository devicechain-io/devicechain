// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These drive the REAL construction: buildRateLimiter and buildEventSources with the
// configuration an operator would give, a stand-in user-management over HTTP, and the
// HTTP ingest listener itself. Nothing here builds a limiter or a gate by hand, so a
// wiring that dropped the pool, the counters or the origin at the one construction site
// fails here even while every unit test of those pieces stays green.

// fakeUM stands in for user-management: the service-token mint and the tenantGovernance
// query. It knows exactly one tenant, "acme", whose own ingest ceiling it serves; every
// other tenant is answered as unknown, by code. down makes the query fail with a 503.
type fakeUM struct {
	srv  *httptest.Server
	down atomic.Bool
	// rate is acme's ingest ceiling in messages per second; zero serves 1000. A test
	// that must observe a spent bucket sets it near zero, so the bucket cannot refill
	// between the shed and the assertion that follows it.
	rate float64
}

func newFakeUM(t *testing.T) *fakeUM {
	t.Helper()
	um := &fakeUM{}
	um.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == auth.ServiceTokenPath {
			_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "svc", ExpiresAt: 1 << 40})
			return
		}
		if um.down.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get(auth.ServiceTenantHeader) != "acme" {
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]any{{
				"message": "unknown tenant", "extensions": map[string]any{"code": "UNKNOWN_TENANT"},
			}}})
			return
		}
		rate := um.rate
		if rate == 0 {
			rate = 1000
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"tenantGovernance": map[string]any{
			"ingestMessagesPerSecond": rate, "ingestBurst": 50, "shedPriority": nil, "purgeState": "active",
		}}})
	}))
	t.Cleanup(um.srv.Close)
	return um
}

func (um *fakeUM) infra(t *testing.T) (string, uint32) {
	host, portStr, err := net.SplitHostPort(um.srv.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return host, uint32(port)
}

// nopWriter accepts and discards, standing in for the NATS writers the HTTP path hands
// admitted messages to.
type nopWriter struct{}

func (nopWriter) WriteMessages(context.Context, ...messaging.Message) error { return nil }
func (nopWriter) WriteToDevice(context.Context, string, ...messaging.Message) error {
	return nil
}
func (nopWriter) HandleResponse(error) {}

// ingestWiring is one event-sources process as main builds it, with its HTTP listener
// started and its metrics on a registry the test can read.
type ingestWiring struct {
	reg  *prometheus.Registry
	addr string
}

// wireIngest builds the service's rate limiters and sources exactly as main does. um nil
// means no user-management is configured.
func wireIngest(t *testing.T, um *fakeUM, rate float64, burst int) *ingestWiring {
	t.Helper()
	savedConfig, savedSources, savedMs := Configuration, EventSources, Microservice
	savedLive, savedBacklog, savedHTTP, savedShed := RateLimiter, BacklogRateLimiter, HttpRateLimiter, ShedPriorityResolver
	savedInbound, savedFailed := InboundEventsWriter, FailedDecodeWriter
	t.Cleanup(func() {
		Configuration, EventSources, Microservice = savedConfig, savedSources, savedMs
		RateLimiter, BacklogRateLimiter, HttpRateLimiter, ShedPriorityResolver = savedLive, savedBacklog, savedHTTP, savedShed
		InboundEventsWriter, FailedDecodeWriter = savedInbound, savedFailed
	})

	reg := prometheus.NewRegistry()
	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}
	Microservice.UseMetricsRegistry(reg)
	if um != nil {
		host, port := um.infra(t)
		Microservice.InstanceConfiguration.Infrastructure.ServiceAuth.Secret = "shh"
		Microservice.InstanceConfiguration.Infrastructure.UserManagement.Hostname = host
		Microservice.InstanceConfiguration.Infrastructure.UserManagement.Port = port
	}
	initializeMetrics()
	InboundEventsWriter, FailedDecodeWriter = nopWriter{}, nopWriter{}

	Configuration = &config.EventSourcesConfiguration{
		IngestRateLimit: config.IngestRateLimit{MessagesPerSecond: rate, Burst: burst},
		EventSources: []config.EventSource{{
			Id:            "http1",
			Type:          config.SourceTypeHttp,
			Configuration: map[string]string{"port": "8081"},
			Decoder:       config.EventDecoder{Type: processor.DECODER_TYPE_JSON},
		}},
	}
	Configuration.ApplyDefaults()
	buildRateLimiter()
	require.NoError(t, buildEventSources())
	source := EventSources[0].(*processor.HttpEventSource)
	source.Port = 0
	ctx := context.Background()
	require.NoError(t, source.Initialize(ctx))
	require.NoError(t, source.Start(ctx))
	t.Cleanup(func() { _ = source.Stop(context.Background()) })
	return &ingestWiring{reg: reg, addr: source.Addr()}
}

// post sends one event for tenant to the HTTP listener and reports whether it was
// admitted (anything but 429). The body does not decode, so an admitted post is
// answered 400 without needing a downstream.
func (w *ingestWiring) post(t *testing.T, client *http.Client, tenant string) bool {
	t.Helper()
	resp, err := client.Post(fmt.Sprintf("http://%s/inst-1/%s/events", w.addr, tenant),
		"application/json", strings.NewReader("not json"))
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode != http.StatusTooManyRequests
}

// metric reads one series' value from the registry; a series that is not exported reads
// as -1, so "absent" and "zero" cannot be confused.
func (w *ingestWiring) metric(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := w.reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	series:
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if want, ok := labels[lp.GetName()]; ok && want != lp.GetValue() {
					continue series
				}
			}
			return m.GetCounter().GetValue()
		}
	}
	return -1
}

const (
	overflowMetric   = "devicechain_eventsources_ratelimit_overflow_admissions_total"
	unresolvedMetric = "devicechain_eventsources_governance_unresolved_admissions_total"
)

func TestHTTPSprayIsBoundedAndAuthenticatedTenantsAreNot(t *testing.T) {
	const burst = 2
	um := newFakeUM(t)
	w := wireIngest(t, um, 0.001, burst)
	client := &http.Client{Timeout: 5 * time.Second}

	// A spray of invented tenant names on the HTTP path.
	for i := 0; i < 3000; i++ {
		w.post(t, client, fmt.Sprintf("invented-%d", i))
	}
	confirmed, pooled, overflow := HttpRateLimiter.BucketCounts()
	assert.Equal(t, 0, confirmed, "no invented name may get a confirmed allowance")
	assert.Equal(t, 1024, pooled, "the pool is full and no larger")
	assert.True(t, overflow, "names past the pool share the overflow allowance")
	assert.Equal(t, float64(burst), w.metric(t, overflowMetric, nil),
		"the overflow served exactly its one burst and counted each admission")
	assertAuthenticatedLimitersUntouched(t)

	// The one real tenant: once user-management confirms it, it is metered in an
	// allowance of its own at its own ceiling (1000/s, burst 50), while an invented name
	// at the same moment is still shed by the drained overflow.
	require.Eventually(t, func() bool {
		w.post(t, client, "acme")
		c, _, _ := HttpRateLimiter.BucketCounts()
		return c == 1
	}, 10*time.Second, 20*time.Millisecond, "acme never got a confirmed allowance")
	time.Sleep(100 * time.Millisecond) // 1000/s: its own bucket refills well past its burst
	admitted := 0
	for w.post(t, client, "acme") && admitted < 1000 {
		admitted++
	}
	assert.GreaterOrEqual(t, admitted, 50, "acme must be metered at its own ceiling, not the overflow's")
	assert.False(t, w.post(t, client, "invented-99999"), "the overflow is still drained")

	// Authenticated origins never pool: 2000 cold capture-stream tenants, backlog-timed,
	// while the overflow is live, are each admitted.
	shed := 0
	sentAt := time.Now().Add(-time.Hour)
	for i := 0; i < 2000; i++ {
		if !ingestGate("gw", fmt.Sprintf("device-tenant-%d", i), sentAt, false, processor.OriginAuthenticated) {
			shed++
		}
	}
	assert.Zero(t, shed, "an authenticated tenant must never be metered in the untrusted pool")
}

// HTTP names its tenant in a path segment and the device credential is checked only
// after the message is admitted, so anyone who can reach the port and knows a tenant's
// name can post as that tenant. Those posts must spend an allowance of their own: had
// they spent the one the tenant's devices spend, an HTTP flood would shed the tenant's
// captured MQTT telemetry, and a shed capture message is ack-dropped, which is the
// permanent loss of data the broker already PUBACKed to the device.
//
// Driven through the real buildRateLimiter and buildEventSources, so the wiring (which
// limiter main hands the gate for HTTP) is what is under test, not only the gate.
func TestHTTPFloodOnARealTenantDoesNotDropItsCapturedTelemetry(t *testing.T) {
	um := newFakeUM(t)
	um.rate = 0.001 // acme's own ceiling: burst 50, and effectively no refill
	w := wireIngest(t, um, 0.001, 2)
	client := &http.Client{Timeout: 5 * time.Second}

	// Wait for user-management to confirm acme, then flood it over HTTP until it is shed.
	require.Eventually(t, func() bool {
		w.post(t, client, "acme")
		c, _, _ := httpLimiter().BucketCounts()
		return c == 1
	}, 10*time.Second, 20*time.Millisecond, "acme was never resolved and shed over HTTP")
	for i := 0; i < 200 && w.post(t, client, "acme"); i++ {
	}
	require.False(t, w.post(t, client, "acme"), "the HTTP flood must have spent acme's HTTP allowance")

	// acme's devices are untouched by it: a caught-up capture-stream message, and one from
	// an external MQTT broker (which passes no send time), are each still admitted.
	assert.True(t, ingestGate("gw", "acme", time.Now(), false, processor.OriginAuthenticated),
		"an HTTP flood naming acme must not shed acme's captured telemetry")
	assert.True(t, ingestGate("mqtt-ext", "acme", time.Time{}, false, processor.OriginAuthenticated),
		"an HTTP flood naming acme must not shed acme's external-broker telemetry")
}

// httpLimiter is the limiter HTTP ingest is metered in.
func httpLimiter() *core.TenantRateLimiter { return HttpRateLimiter }

// assertAuthenticatedLimitersUntouched pins that HTTP ingest reached neither of the
// limiters authenticated traffic is metered in: no bucket, pooled or confirmed, and no
// overflow.
func assertAuthenticatedLimitersUntouched(t *testing.T) {
	t.Helper()
	for name, l := range map[string]*core.TenantRateLimiter{"live": RateLimiter, "backlog": BacklogRateLimiter} {
		c, p, o := l.BucketCounts()
		assert.Truef(t, c == 0 && p == 0 && !o,
			"HTTP ingest reached the %s limiter (%d confirmed, %d pooled, overflow %v)", name, c, p, o)
	}
}

// The contention floor sheds HTTP ingest exactly as it sheds live authenticated
// traffic: HTTP is new ingress, judged at admission. At the deepest floor the fail-safe
// bronze band keeps nothing, so HTTP is refused outright while the capture backlog,
// which the floor never sheds, is still admitted.
//
// Both construction branches are driven: buildRateLimiter builds the HTTP limiter once
// with user-management configured (the production branch) and once without, and a floor
// dropped from either one is a separate defect.
func TestTheContentionFloorShedsHTTPIngest(t *testing.T) {
	for _, tc := range []struct {
		name string
		um   func(t *testing.T) *fakeUM
	}{
		{"with user-management", newFakeUM},
		{"without user-management", func(*testing.T) *fakeUM { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			um := tc.um(t)
			w := wireIngest(t, um, 1000, 100)
			Configuration.Contention.ManualFloor = 3
			client := &http.Client{Timeout: 5 * time.Second}

			// With user-management, a tenant is not shed until its priority has resolved (an
			// unresolved one is admitted at its base ceiling; see shedAdjusted), and the first
			// look-up only starts that resolution. So wait for the resolved case, then prove
			// it is resolution and not an exhausted bucket that refuses: acme's ceiling of
			// 1000/s refills faster than this loop can spend it.
			//
			// The condition does not fail the test itself: Eventually polls on its own
			// goroutine, and a poll still in flight when the test ends would fail a finished
			// test and panic the package instead of reporting this assertion.
			shed := func() bool {
				resp, err := client.Post(fmt.Sprintf("http://%s/inst-1/acme/events", w.addr),
					"application/json", strings.NewReader("not json"))
				if err != nil {
					return false
				}
				_ = resp.Body.Close()
				return resp.StatusCode == http.StatusTooManyRequests
			}
			require.Eventually(t, shed, 10*time.Second, 20*time.Millisecond,
				"HTTP ingest must be shed at the deepest contention floor")
			if um != nil {
				_, resolved := ShedPriorityResolver.Resolve("acme")
				require.True(t, resolved, "the shed must be the resolved priority's, not a spent bucket's")
			}
			assert.False(t, ingestGate("mqtt-ext", "acme", time.Time{}, false, processor.OriginAuthenticated),
				"the counterweight: live authenticated traffic is shed at this floor too")
			assert.True(t, ingestGate("gw", "acme", time.Now().Add(-time.Hour), false, processor.OriginAuthenticated),
				"the capture backlog is never shed by the floor")
		})
	}
}

// With no user-management configured the spray is bounded just the same, and nothing is
// counted as unresolved: with no authority, the platform default IS the answer.
func TestHTTPSprayIsBoundedWithoutUserManagement(t *testing.T) {
	w := wireIngest(t, nil, 0.001, 2)
	client := &http.Client{Timeout: 5 * time.Second}

	for i := 0; i < 3000; i++ {
		w.post(t, client, fmt.Sprintf("invented-%d", i))
	}
	confirmed, pooled, overflow := HttpRateLimiter.BucketCounts()
	assert.Equal(t, 0, confirmed)
	assert.Equal(t, 1024, pooled)
	assert.True(t, overflow)
	assert.Equal(t, float64(2), w.metric(t, overflowMetric, nil),
		"the HTTP limiter's overflow served its one burst and counted each admission")
	assertAuthenticatedLimitersUntouched(t)
	for _, cause := range []string{"pending", "unreachable", "unknown-tenant"} {
		assert.Equalf(t, float64(0), w.metric(t, unresolvedMetric, map[string]string{"cause": cause}),
			"cause %q: a static ceiling is not unresolved", cause)
	}
}

// With user-management failing, admissions at the platform default are counted as
// unreachable — the cause that pages — even while a spray of novel names is starving the
// resolver's refresh budget.
func TestSprayWithUserManagementDownStillCountsUnreachable(t *testing.T) {
	um := newFakeUM(t)
	um.down.Store(true)
	w := wireIngest(t, um, 0.001, 1000)
	client := &http.Client{Timeout: 5 * time.Second}

	for i := 0; i < 500; i++ {
		w.post(t, client, fmt.Sprintf("invented-%d", i))
	}
	require.Eventually(t, func() bool {
		w.post(t, client, "acme")
		return w.metric(t, unresolvedMetric, map[string]string{"cause": "unreachable", "dimension": "ingest"}) > 0
	}, 10*time.Second, 20*time.Millisecond, "an unreachable user-management was never counted as unreachable")
}

func TestShedAdjustedCopiesSource(t *testing.T) {
	withFloor(t, 3)
	for _, src := range []core.CeilingSource{core.CeilingPending, core.CeilingResolved,
		core.CeilingStatic, core.CeilingUnreachable, core.CeilingUnknownTenant} {
		base := func(string) core.TenantCeiling {
			return core.TenantCeiling{RatePerSecond: 1000, Burst: 2000, Source: src}
		}
		for _, prio := range []func(string) (int, bool){resolved(bestEffortPriority), resolved(goldPriority),
			func(string) (int, bool) { return bronzePriority, false }} {
			assert.Equalf(t, src, shedAdjusted(base, prio)("acme").Source, "source %v", src)
		}
	}
}
