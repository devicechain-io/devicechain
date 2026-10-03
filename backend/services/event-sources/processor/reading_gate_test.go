// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/model"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventlimit"
	"github.com/devicechain-io/dc-microservice/governance"
)

// admitAllReadings is a reading stage with no ceiling, for tests about something else.
func admitAllReadings(string, string, time.Time, bool, Origin, int) bool { return true }

// readingLimiter is a reading limiter at the given ceiling, through the one definition of a
// reading ceiling, as production builds them.
func readingLimiter(rps float64, burst int) *core.TenantRateLimiter {
	return core.NewTenantRateLimiter(governance.ReadingCeiling(core.StaticCeiling(rps, burst)))
}

type readingShed struct {
	source, tenant string
	readings       int
}

func newTestReadingGate(rps float64, burst int) (ReadingGate, *[]readingShed) {
	var sheds []readingShed
	gate := NewReadingGate(readingLimiter(rps, burst), readingLimiter(rps, burst), readingLimiter(rps, burst),
		func(source, tenant string, n int) { sheds = append(sheds, readingShed{source, tenant, n}) })
	return gate, &sheds
}

// Every reading is charged: at the default ceiling (1000/s, burst 2000) seven 256-reading
// events fit the burst and the eighth, at the same instant, does not. A gate charging one
// unit per message would admit all eight and thousands more.
func TestReadingGateChargesEveryReading(t *testing.T) {
	gate, sheds := newTestReadingGate(1000, 2000)
	for i := 0; i < 7; i++ {
		require.True(t, gate("src", "acme", time.Time{}, false, OriginAuthenticated, 256), "event %d", i+1)
	}
	assert.False(t, gate("src", "acme", time.Time{}, false, OriginAuthenticated, 256),
		"7 x 256 = 1792 of a 2000 burst; another 256 must be refused")
	assert.Equal(t, []readingShed{{"src", "acme", 256}}, *sheds)
	assert.True(t, gate("src", "beta", time.Time{}, false, OriginAuthenticated, 256),
		"another tenant's allowance is its own")
}

// A recovered backlog is metered on the timeline it was sent on, as RateGate meters its
// messages: 40 full events sent 0.3 s apart an hour ago are within 1000 readings/s and are
// admitted in full; the same 40 sent now are a burst, and only the burst's worth pass.
func TestReadingGateMetersADrainOnTheSendTimeline(t *testing.T) {
	gate, _ := newTestReadingGate(1000, 2000)
	base := time.Now().Add(-time.Hour)
	admitted := 0
	for i := 0; i < 40; i++ {
		if gate("gw", "acme", base.Add(time.Duration(i)*300*time.Millisecond), false, OriginAuthenticated, 256) {
			admitted++
		}
	}
	assert.Equal(t, 40, admitted, "a compliant drain is admitted in full")

	live, _ := newTestReadingGate(1000, 2000)
	admitted = 0
	for i := 0; i < 40; i++ {
		if live("gw", "acme", time.Time{}, false, OriginAuthenticated, 256) {
			admitted++
		}
	}
	assert.InDelta(t, 7, admitted, 1, "the same readings arriving at once are a burst")
}

// HTTP names its tenant before any credential is checked, so its readings are charged in an
// allowance of their own: exhausting it leaves the tenant's authenticated readings untouched.
func TestAnHTTPReadingFloodCannotSpendAuthenticatedReadingAllowance(t *testing.T) {
	gate, _ := newTestReadingGate(1e-9, 256)
	require.True(t, gate("http", "acme", time.Time{}, false, OriginUntrusted, 256))
	require.False(t, gate("http", "acme", time.Time{}, false, OriginUntrusted, 1), "the HTTP allowance is spent")
	assert.True(t, gate("gw", "acme", time.Time{}, false, OriginAuthenticated, 256),
		"captured readings are charged in the authenticated allowance, which HTTP did not touch")
}

// A redelivery paid on delivery 1 and is not charged again; a first delivery is.
func TestAReadingRedeliveryIsNotCharged(t *testing.T) {
	gate, sheds := newTestReadingGate(1e-9, 256)
	require.True(t, gate("gw", "acme", time.Time{}, false, OriginAuthenticated, 256))
	assert.True(t, gate("gw", "acme", time.Time{}, true, OriginAuthenticated, 256), "a redelivery is exempt")
	assert.False(t, gate("gw", "acme", time.Time{}, false, OriginAuthenticated, 256), "a first delivery is charged")
	assert.Len(t, *sheds, 1)
}

func TestNewReadingGateRefusesANilLimiter(t *testing.T) {
	assert.Panics(t, func() { NewReadingGate(nil, readingLimiter(1, 1), readingLimiter(1, 1), nil) })
	assert.Panics(t, func() { NewReadingGate(readingLimiter(1, 1), nil, readingLimiter(1, 1), nil) })
	assert.Panics(t, func() { NewReadingGate(readingLimiter(1, 1), readingLimiter(1, 1), nil, nil) })
}

// The message stage still charges ONE token per message, whatever it carries: the routing
// both gates share must not have changed what RateGate charges.
func TestARateGateIsStillOneTokenPerMessage(t *testing.T) {
	lim := core.StaticCeiling(1e-9, 3)
	gate := NewRateGate(core.NewTenantRateLimiter(lim), core.NewTenantRateLimiter(lim), core.NewTenantRateLimiter(lim), nil)
	for _, origin := range []Origin{OriginAuthenticated, OriginUntrusted} {
		gate := gate
		if origin == OriginUntrusted {
			gate = NewRateGate(core.NewTenantRateLimiter(lim), core.NewTenantRateLimiter(lim), core.NewTenantRateLimiter(lim), nil)
		}
		admitted := 0
		for i := 0; i < 10; i++ {
			if gate("s", "acme", time.Time{}, false, origin) {
				admitted++
			}
		}
		assert.Equal(t, 3, admitted, "origin %v: a burst of 3 admits 3 messages", origin)
	}
}

// meterTime is the one live/backlog decision: a recent or absent send time is live (zero),
// an old one is metered at itself.
func TestMeterTimeDecidesLiveOrBacklog(t *testing.T) {
	assert.True(t, meterTime(time.Time{}).IsZero())
	assert.True(t, meterTime(time.Now()).IsZero(), "a fresh message is live")
	assert.True(t, meterTime(time.Now().Add(time.Hour)).IsZero(), "a future append time is live, never backlog")
	old := time.Now().Add(-time.Hour)
	assert.Equal(t, old, meterTime(old), "an old message is metered at its send time")
}

// readingsOf charges what model.ReadingCount counts: the metric keys of a measurement, not
// its entries; at least one for an event with no readings; and the per-event maximum for a
// kind it does not know.
func TestReadingsOfChargesTheReadingCount(t *testing.T) {
	wide := &model.UnresolvedMeasurementsPayload{Entries: []model.UnresolvedMeasurementsEntry{
		{Measurements: map[string]string{"a": "1", "b": "2", "c": "3"}},
		{Measurements: map[string]string{"d": "4"}},
	}}
	assert.Equal(t, 4, readingsOf(wide))
	assert.Equal(t, 1, readingsOf(&model.UnresolvedMeasurementsPayload{}), "an event with no readings still costs one")
}

func TestAnUncountedPayloadIsChargedTheMaximum(t *testing.T) {
	assert.Equal(t, eventlimit.MaxReadingsPerEvent, readingsOf(struct{}{}))
}

// ==================== HTTP: the tenant's own ceiling before the shared gate ====================

// 🔴 THE ORDER. While the pipeline refuses, a tenant over its own reading ceiling is answered
// 429 — its own overage — and the shared gate is not even asked. Before, the shared gate was
// asked first, so a tenant over its ceiling kept receiving the 503 every other tenant got,
// having closed the gate itself.
func TestHttpTenantOverItsCeilingIs429EvenWhileThePipelineRefuses(t *testing.T) {
	gate, _ := newTestReadingGate(1e-9, 256)
	asked := 0
	admit := func(s string) error { asked++; return refuseAll(s) }
	es, dec, _ := newTestHttpSourceGated(t, nil, gate, admit, config.HttpIngest{}, nil)

	post := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		es.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/inst-1/acme/events",
			strings.NewReader(wideEntryBody(256))))
		return rec
	}

	first := post()
	assert.Equal(t, http.StatusServiceUnavailable, first.Code, "within its ceiling: the shared refusal")
	assert.Equal(t, 1, asked)
	assert.False(t, dec.called, "a refused request is not published")

	second := post()
	assert.Equal(t, http.StatusTooManyRequests, second.Code, "over its own ceiling: its own 429, not the shared 503")
	assert.Equal(t, "1", second.Header().Get("Retry-After"))
	assert.Equal(t, 1, asked, "a request over its tenant's ceiling must not reach the shared gate")
}

// The counterweight: within its ceiling, a request is still refused by the shared gate with
// the backpressure 503 and its Retry-After — per-tenant charging did not swallow that path.
func TestHttpRequestWithinItsCeilingIs503WhileThePipelineRefuses(t *testing.T) {
	gate, _ := newTestReadingGate(1000, 2000)
	es, dec, _ := newTestHttpSourceGated(t, nil, gate, refuseAll, config.HttpIngest{}, nil)
	rec := httptest.NewRecorder()
	es.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/inst-1/acme/events",
		strings.NewReader(canonicalMeasurementBody)))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "10", rec.Header().Get("Retry-After"))
	assert.False(t, dec.called)
}

// At the default ceiling, seven 256-reading posts are accepted and the eighth is 429 with a
// Retry-After, counted in readings. On a gate charging one unit per message all eight are 202.
func TestHttpEventSource_ReadingCeilingAnswers429WithRetryAfter(t *testing.T) {
	gate, sheds := newTestReadingGate(1000, 2000)
	es, _, _ := newTestHttpSourceGated(t, nil, gate, admitAll, config.HttpIngest{}, nil)
	codes := make([]int, 0, 8)
	var last *httptest.ResponseRecorder
	for i := 0; i < 8; i++ {
		last = httptest.NewRecorder()
		es.handler().ServeHTTP(last, httptest.NewRequest(http.MethodPost, "/inst-1/acme/events",
			strings.NewReader(wideEntryBody(256))))
		codes = append(codes, last.Code)
	}
	assert.Equal(t, []int{202, 202, 202, 202, 202, 202, 202, 429}, codes)
	assert.Equal(t, "1", last.Header().Get("Retry-After"))
	assert.Contains(t, last.Body.String(), "counted in readings")
	assert.Equal(t, []readingShed{{"http-test", "acme", 256}}, *sheds)
}

func TestSourcesRefuseToBeBuiltWithoutAReadingGate(t *testing.T) {
	_, err := NewHttpEventSource("h", map[string]string{}, "inst-1", config.HttpIngest{},
		NewJsonDecoder(map[string]string{}), func(string, []byte) {}, nil, nil, nil, nil, admitAll, nil)
	require.ErrorIs(t, err, errNoReadingGate)

	_, err = NewMqttEventSource("m", "cid", map[string]string{"host": "h", "port": "1883", "topic": "t"},
		nil, "", "", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {},
		func(string, string, *model.UnresolvedEvent, interface{}, uint64) error { return nil },
		func(string, string, []byte, error) error { return nil },
		nil, nil, admitAll, alwaysOwns, func(error) {})
	require.ErrorIs(t, err, errNoReadingGate)

	assert.Panics(t, func() {
		NewGatewayJetStreamSource(nil, "gw", NewJsonDecoder(map[string]string{}), func(string, []byte) {},
			nil, func(string, string, []byte, error) error { return nil }, nil, nil)
	})
	assert.Panics(t, func() {
		NewDecodeWorker(1, "s", NewJsonDecoder(map[string]string{}), make(chan rawMessage), nil, nil, nil)
	})
}

// ==================== The decode worker's reading stage ====================

// A decoded message over its tenant's reading ceiling is not handed on, and it is settled
// nil (acked): left unacked it would be redelivered into the same gate for ever.
func TestDecodeWorkerShedsOverCeilingReadingsAndAcks(t *testing.T) {
	raw := make(chan rawMessage, 1)
	called := false
	w := NewDecodeWorker(1, "s", NewJsonDecoder(map[string]string{}), raw,
		func(string, string, time.Time, bool, Origin, int) bool { return false },
		func(string, string, *model.UnresolvedEvent, interface{}, uint64, func(error)) { called = true },
		func(string, string, []byte, error) error { t.Error("a shed is not a decode failure"); return nil })
	settled := make(chan error, 1)
	raw <- rawMessage{tenant: "acme", payload: []byte(wideEntryBody(10)), origin: OriginAuthenticated,
		done: func(err error) { settled <- err }}
	close(raw)
	w.Process()

	select {
	case err := <-settled:
		assert.NoError(t, err, "a reading shed is a deliberate drop: settled nil")
	default:
		t.Fatal("a shed message was never settled")
	}
	assert.False(t, called, "a shed message must not be handed on for publishing")
}

// The capture source tells the reading stage what the message stage was told: the send time
// it was metered at (the append time, for a backlog message), the redelivery flag and the
// origin, with the message's reading count.
func TestDecodeWorkerPassesSendTimeRedeliveryAndOrigin(t *testing.T) {
	h := newCaptureHarness(t)
	appended := time.Now().Add(-time.Hour).Truncate(time.Millisecond)

	ack := &recordingAck{}
	m := capturedMsg(captureSubject, wideEntryBody(3), 2, 7, ack)
	m.Subject = "inst-1.acme.devices.d1.events"
	m.AppendTime = appended
	h.source.handle(m)
	require.True(t, ack.settled(t), "message never settled")

	fresh := &recordingAck{}
	m = capturedMsg("inst-1.acme.devices.d1.events", wideEntryBody(5), 1, 8, fresh)
	m.AppendTime = time.Now()
	h.source.handle(m)
	require.True(t, fresh.settled(t), "message never settled")

	h.mu.Lock()
	calls := append([]readingCall(nil), h.readingCalls...)
	h.mu.Unlock()
	require.Len(t, calls, 2)
	assert.Equal(t, readingCall{"acme", appended, true, OriginAuthenticated, 3}, calls[0],
		"a backlog redelivery: metered at its append time, flagged, authenticated, 3 readings")
	assert.Equal(t, readingCall{"acme", time.Time{}, false, OriginAuthenticated, 5}, calls[1],
		"a live first delivery is metered at now")
}

// ==================== External MQTT ====================

// The tenant's own ceiling is checked before the shared gate on the external-broker source
// too: a message over it is dropped without asking the pipeline, and one within it is still
// dropped while the pipeline refuses.
func TestExternalMqttChecksTheTenantCeilingBeforeThePipeline(t *testing.T) {
	es, received := newTestMqttSource(t, func(string, string, time.Time, bool, Origin) bool { return false })
	asked := 0
	es.admit = func(string) error { asked++; return nil }

	es.onMessage(nil, &fakeMqttMessage{topic: "inst-1/acme/events", payload: []byte(`{"device":"d1"}`)})

	assert.Equal(t, 0, asked, "a message over its tenant's ceiling must not reach the shared gate")
	assert.Equal(t, 0, *received)
	assert.Len(t, es.messages, 0)
}
