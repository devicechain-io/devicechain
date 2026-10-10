// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests drive the per-reading ingest ceiling through the service's own wiring:
// buildRateLimiter, buildEventSources and createNatsComponents, against a real JetStream
// server where the defect is a broker's. The first two use only what the service exported
// before the reading ceiling existed, so they compile against the old code too and fail
// there on their VALUES.

// readingsBody is one measurement event carrying n readings (one entry, n metric keys).
func readingsBody(device string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"m%d":"%d"`, i, i)
	}
	return fmt.Sprintf(`{"device":%q,"eventType":"Measurement","payload":{"entries":[{"measurements":{%s}}]}}`,
		device, b.String())
}

// brokerIngest is one event-sources process wired as main wires it, over an embedded
// JetStream server whose streams hold maxMsgs messages, with a device-management durable on
// inbound-events that never reads.
type brokerIngest struct {
	reg *prometheus.Registry
	js  nats.JetStreamContext
	url *url.URL
}

func startBrokerIngest(t *testing.T, maxMsgs int64, sources func(natsHost string) []config.EventSource) *brokerIngest {
	t.Helper()
	savedConfig, savedSources, savedMs, savedNats := Configuration, EventSources, Microservice, NatsManager
	savedGateway, savedGatewayId := GatewaySource, GatewaySourceId
	savedInbound, savedFailed, savedCapture := InboundEventsWriter, FailedDecodeWriter, CaptureReader
	t.Cleanup(func() {
		Configuration, EventSources, Microservice, NatsManager = savedConfig, savedSources, savedMs, savedNats
		GatewaySource, GatewaySourceId = savedGateway, savedGatewayId
		InboundEventsWriter, FailedDecodeWriter, CaptureReader = savedInbound, savedFailed, savedCapture
	})

	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, NoLog: true, NoSigs: true,
		StoreDir: dctest.JetStreamStoreDir(t),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second))
	t.Cleanup(srv.Shutdown)
	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	reg := prometheus.NewRegistry()
	microservice := func(area string, reg *prometheus.Registry) *core.Microservice {
		ms := &core.Microservice{InstanceId: "inst-1", FunctionalArea: area, Readiness: core.NewReadinessGate()}
		ms.UseMetricsRegistry(reg)
		ms.Readiness.MarkReadyWithoutAuthSurface()
		ms.InstanceConfiguration.Infrastructure.Nats.Hostname = u.Hostname()
		ms.InstanceConfiguration.Infrastructure.Nats.Port = uint32(port)
		ms.InstanceConfiguration.Infrastructure.Nats.StreamMaxMsgs = maxMsgs
		return ms
	}

	dm := messaging.NewNatsManager(microservice("device-management", prometheus.NewRegistry()),
		core.NewNoOpLifecycleCallbacks(), func(*messaging.NatsManager) error { return nil })
	require.NoError(t, dm.Initialize(context.Background()))
	t.Cleanup(func() { _ = dm.Stop(context.Background()) })
	_, err = dm.NewReader(streams.InboundEvents)
	require.NoError(t, err)

	Microservice = microservice("event-sources", reg)
	initializeMetrics()
	Configuration = &config.EventSourcesConfiguration{EventSources: sources(u.Hostname())}
	Configuration.ApplyDefaults()
	GatewaySource = nil
	buildTestRateLimiters(t)
	require.NoError(t, buildEventSources())

	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	NatsManager.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
			return messaging.MaxDeliveryLettered, nil
		}, nil
	})
	ctx := context.Background()
	require.NoError(t, NatsManager.Initialize(ctx))
	require.NoError(t, NatsManager.Start(ctx))
	t.Cleanup(func() { _ = NatsManager.Stop(context.Background()) })

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	return &brokerIngest{reg: reg, js: js, url: u}
}

// counter reads the sum of a counter's series whose labels include want; a counter that is
// not exported at all reads -1, so "absent" and "zero" cannot be confused.
func (b *brokerIngest) counter(t *testing.T, name string, want map[string]string) float64 {
	t.Helper()
	mfs, err := b.reg.Gather()
	require.NoError(t, err)
	total, found := 0.0, false
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		found = true
	series:
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if v, ok := want[lp.GetName()]; ok && v != lp.GetValue() {
					continue series
				}
			}
			total += m.GetCounter().GetValue()
		}
	}
	if !found {
		return -1
	}
	return total
}

func (b *brokerIngest) inboundState(t *testing.T) nats.StreamState {
	t.Helper()
	info, err := b.js.StreamInfo(messaging.StreamName("inst-1", streams.InboundEvents))
	require.NoError(t, err)
	return info.State
}

// maxAdmittedEvents is the most 256-reading events a tenant at the default ceiling (1000
// readings/s, burst 2000) can be admitted in `elapsed`, plus one for the boundary.
func maxAdmittedEvents(elapsed time.Duration) int {
	return int((float64(config.DefaultIngestBurst)+config.DefaultIngestReadingsPerSecond*elapsed.Seconds())/256) + 1
}

// 🔴 THE DEFECT. One tenant at its DEFAULT ceiling posts events of 256 readings each. Charged
// one unit per message, all of them were within its ceiling of 1000 a second, so they filled
// the shared inbound stream until the backpressure gate closed — and then a second tenant,
// sending one reading, was refused too. Charged per reading, the flooding tenant is held to
// about 2000 readings (its burst) as its own 429, the gate never closes, and the bystander
// keeps getting 202.
func TestOneTenantsLargeEventsAreShedAtItsOwnCeilingAndAnotherTenantIsStillAccepted(t *testing.T) {
	b := startBrokerIngest(t, 100, func(string) []config.EventSource {
		return []config.EventSource{{
			Id: "http1", Type: config.SourceTypeHttp, Configuration: map[string]string{"port": "8081"},
			Decoder: config.EventDecoder{Type: processor.DECODER_TYPE_JSON},
		}}
	})
	ctx := context.Background()
	source := EventSources[0].(*processor.HttpEventSource)
	source.Port = 0
	require.NoError(t, source.Initialize(ctx))
	require.NoError(t, source.Start(ctx))
	t.Cleanup(func() { _ = source.Stop(context.Background()) })

	post := func(tenant, body string) int {
		resp, err := http.Post("http://"+source.Addr()+"/inst-1/"+tenant+"/events", "application/json",
			strings.NewReader(body))
		require.NoError(t, err)
		_ = resp.Body.Close()
		// The gate is measured by the traffic that consults it, every few seconds; this fills
		// far faster, so it says when each measurement happens.
		NatsManager.MeasureBackpressureForTesting(t, streams.InboundEvents)
		return resp.StatusCode
	}

	// NEGATIVE CONTROL: the bystander can be accepted at all, before the flood.
	require.Equal(t, http.StatusAccepted, post("bystander", readingsBody("b1", 1)))

	codes := map[int]int{}
	started := time.Now()
	for i := 0; i < 100; i++ {
		codes[post("acme", readingsBody("a1", 256))]++
	}
	elapsed := time.Since(started)

	// NEGATIVE CONTROL: a tenant sending the measured shape (one reading per event) is not
	// shed. 50 of them, so the stream (100, gate at 90) cannot be what refuses them.
	compliant := 0
	for i := 0; i < 50; i++ {
		if post("compliant", readingsBody("c1", 1)) == http.StatusAccepted {
			compliant++
		}
	}

	bystander := post("bystander", readingsBody("b1", 1))

	t.Logf("acme: %v in %v; compliant 202s: %d; bystander after the flood: %d", codes, elapsed, compliant, bystander)
	assert.Positive(t, codes[http.StatusTooManyRequests], "the flooding tenant must be refused as its own 429")
	assert.Zero(t, codes[http.StatusServiceUnavailable], "the flooding tenant must not close the shared gate")
	assert.GreaterOrEqual(t, codes[http.StatusAccepted], 7, "its burst admits seven full events")
	assert.LessOrEqual(t, codes[http.StatusAccepted], maxAdmittedEvents(elapsed),
		"no more than its burst plus its rate in readings may be admitted")
	assert.Equal(t, http.StatusAccepted, bystander, "another tenant must still be accepted after the flood")
	assert.Equal(t, 50, compliant, "a tenant sending one reading per event is not shed")
	assert.Equal(t, uint64(codes[http.StatusAccepted]+2+compliant), b.inboundState(t).LastSeq,
		"the stream holds exactly the events answered 202")

	// The accounting: a reading shed is a message received and shed for its readings, never
	// on the message stage's rate-limited counter.
	shed := float64(codes[http.StatusTooManyRequests])
	assert.Equal(t, shed, b.counter(t, "devicechain_eventsources_total_msg_reading_limited", map[string]string{"source": "http1"}))
	assert.Equal(t, 256*shed, b.counter(t, "devicechain_eventsources_total_readings_rate_limited", map[string]string{"source": "http1"}))
	assert.LessOrEqual(t, b.counter(t, "devicechain_eventsources_total_msg_rate_limited", map[string]string{"source": "http1"}), 0.0,
		"no message was shed at the message stage (a counter with no series is not exported, and reads -1)")
	assert.Equal(t, float64(152), b.counter(t, "devicechain_eventsources_total_inbound_messages", map[string]string{"source": "http1"}),
		"every post was received")
}

// The same defect on the platform broker's capture path. 40 captured events of 256 readings
// each, all appended at once, from one tenant at the default ceiling: charged per message all
// 40 reached inbound-events; charged per reading only the burst's worth do, and the rest are
// ACKED (left unacked they would spin into the same gate for ever) and counted.
func TestCapturedLargeEventsAreShedAtTheTenantsReadingCeiling(t *testing.T) {
	b := startBrokerIngest(t, 1000, func(natsHost string) []config.EventSource {
		return []config.EventSource{{
			Id: "mqtt1", Type: processor.TYPE_MQTT, Configuration: map[string]string{"host": natsHost, "port": "1883"},
			Decoder: config.EventDecoder{Type: processor.DECODER_TYPE_JSON},
		}}
	})
	require.NotNil(t, GatewaySource, "a source pointed at the platform broker must be the gateway source")

	const sent = 40
	for i := 0; i < sent; i++ {
		_, err := b.js.Publish(messaging.DeviceEventsSubject("inst-1", "acme", "sensor-001"),
			[]byte(readingsBody("sensor-001", 256)))
		require.NoError(t, err)
	}

	ctx := context.Background()
	started := time.Now()
	require.NoError(t, GatewaySource.Initialize(ctx))
	require.NoError(t, GatewaySource.Start(ctx))
	t.Cleanup(func() { _ = GatewaySource.Stop(context.Background()) })

	capture := messaging.StreamName("inst-1", streams.DeviceEventsCapture)
	require.Eventually(t, func() bool {
		for ci := range b.js.ConsumersInfo(capture) {
			if ci.AckFloor.Stream == sent && ci.NumAckPending == 0 {
				return true
			}
		}
		return false
	}, 20*time.Second, 10*time.Millisecond, "every captured message must be settled and ACKED, the shed ones included")
	elapsed := time.Since(started)

	kept := int(b.inboundState(t).Msgs)
	t.Logf("captured %d, reached inbound-events %d in %v", sent, kept, elapsed)
	assert.GreaterOrEqual(t, kept, 7, "the burst admits seven full events")
	assert.LessOrEqual(t, kept, maxAdmittedEvents(elapsed), "the rest must be shed at the tenant's reading ceiling")
	assert.Equal(t, float64(sent-kept),
		b.counter(t, "devicechain_eventsources_total_msg_reading_limited", map[string]string{"source": "mqtt1"}),
		"every shed message is counted")
}

// ==================== Wiring of the three reading limiters ====================

// HTTP's readings are charged in an allowance of their own: an HTTP flood naming a tenant
// cannot spend the reading allowance its captured telemetry is charged in, where a shed is
// an ack-drop of data the broker already PUBACKed.
func TestHTTPReadingFloodDoesNotShedCapturedReadings(t *testing.T) {
	wireIngest(t, nil, 0.001, 2)
	require.True(t, readingGate("http1", "acme", time.Time{}, false, processor.OriginUntrusted, 256))
	require.False(t, readingGate("http1", "acme", time.Time{}, false, processor.OriginUntrusted, 256),
		"the HTTP reading allowance is spent")
	assert.True(t, readingGate("gw", "acme", time.Now(), false, processor.OriginAuthenticated, 256),
		"captured readings are charged in an allowance HTTP did not touch")
	assert.True(t, readingGate("gw", "acme", time.Now().Add(-time.Hour), false, processor.OriginAuthenticated, 256),
		"and so are backlog readings")
}

// The contention floor sheds live readings and never backlog ones, in both construction
// branches: the backlog drains messages the broker already PUBACKed.
func TestTheContentionFloorNeverShedsBacklogReadings(t *testing.T) {
	for _, tc := range []struct {
		name string
		um   func(t *testing.T) *fakeUM
	}{
		{"with user-management", newFakeUM},
		{"without user-management", func(*testing.T) *fakeUM { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wireIngest(t, tc.um(t), 1000, 2000)
			Configuration.Contention.ManualFloor = 3
			// With user-management a tenant is not shed until its priority resolves; the
			// first look-up starts that, so wait for the live refusal.
			require.Eventually(t, func() bool {
				return !readingGate("mqtt-ext", "acme", time.Time{}, false, processor.OriginAuthenticated, 1)
			}, 10*time.Second, 20*time.Millisecond, "live readings must be shed at the deepest floor")
			assert.False(t, readingGate("http1", "acme", time.Time{}, false, processor.OriginUntrusted, 256),
				"HTTP readings are new ingress and are shed too")
			assert.True(t, readingGate("gw", "acme", time.Now().Add(-time.Hour), false, processor.OriginAuthenticated, 256),
				"backlog readings are never shed by the floor")
		})
	}
}

// The reading gate is built over the READING limiters, not the message ones. At a tier of
// 10 a second with a burst of 20, a message limiter could never admit a 256-reading charge
// (a token bucket never admits more than its burst); the reading limiter, floored at one
// full event, admits exactly one and then refuses on rate.
func TestTheReadingGateIsBuiltOverTheReadingLimiters(t *testing.T) {
	w := wireIngest(t, nil, 10, 20)
	client := &http.Client{Timeout: 5 * time.Second}
	post := func() int {
		resp, err := client.Post(fmt.Sprintf("http://%s/inst-1/acme/events", w.addr), "application/json",
			strings.NewReader(readingsBody("d1", 256)))
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, http.StatusAccepted, post(), "one full event fits the floored reading burst")
	assert.Equal(t, http.StatusTooManyRequests, post(), "the second is refused on rate")
}
