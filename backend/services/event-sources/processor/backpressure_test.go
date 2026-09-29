// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func refuseAll(string) error {
	return &messaging.BackpressureError{Stream: "inst-1_inbound-events", Durable: "d", Ratio: 0.95}
}

// While the pipeline refuses, a POST is answered 503 with a Retry-After BEFORE its body is
// read: nothing is decoded, and the tenant's rate-limit token is not spent, so the retry the
// 503 asks for is not charged twice.
func TestHttpBackpressureIsRefusedBeforeTheBodyIsRead(t *testing.T) {
	metered := false
	allow := func(string, string, time.Time, bool, Origin) bool { metered = true; return true }
	es, dec, fail := newTestHttpSourceAdmitting(t, allow, refuseAll, config.HttpIngest{}, nil)

	rec := httptest.NewRecorder()
	es.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/inst-1/acme/events",
		strings.NewReader(canonicalMeasurementBody)))

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "10", rec.Header().Get("Retry-After"))
	assert.False(t, dec.called, "a refused request must not be decoded or published")
	assert.False(t, fail.called)
	assert.False(t, metered, "a refused request must not spend the tenant's rate-limit token")
}

// The gate can close between the check and the publish. The publish's refusal is answered
// the same way: 503 and a Retry-After, never 202. Before, it was a bare 503.
func TestHttpPublishRefusalAnswers503WithRetryAfter(t *testing.T) {
	es, dec, _ := newTestHttpSource(t, nil)
	dec.publishErr = &messaging.BackpressureError{Stream: "s", Durable: "d", Ratio: 0.91}

	rec := httptest.NewRecorder()
	es.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/inst-1/acme/events",
		strings.NewReader(canonicalMeasurementBody)))

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "10", rec.Header().Get("Retry-After"), "a backpressure 503 must say when to retry")
	assert.True(t, dec.called)
}

// A publish that FAILED is a bare 503: it may have been stored before its acknowledgement
// was lost, so it must not read as the certain non-accept a Retry-After 503 is.
func TestHttpPublishFailureIsABare503(t *testing.T) {
	es, dec, _ := newTestHttpSource(t, nil)
	dec.publishErr = errors.New("jetstream unavailable")
	rec := httptest.NewRecorder()
	es.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/inst-1/acme/events",
		strings.NewReader(canonicalMeasurementBody)))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Empty(t, rec.Header().Get("Retry-After"))
}

// NEGATIVE CONTROL: an accepted event is 202 with no Retry-After.
func TestHttpAcceptedEventCarriesNoRetryAfter(t *testing.T) {
	es, dec, _ := newTestHttpSource(t, nil)
	rec := httptest.NewRecorder()
	es.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/inst-1/acme/events",
		strings.NewReader(canonicalMeasurementBody)))
	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Empty(t, rec.Header().Get("Retry-After"))
	assert.True(t, dec.called)
}

// A source cannot be built without the question: a nil admit would accept into a stream
// that is refusing, and nothing at the call site would show it.
func TestSourcesRefuseToBeBuiltWithoutAdmit(t *testing.T) {
	_, err := NewHttpEventSource("h", map[string]string{}, "inst-1", config.HttpIngest{},
		NewJsonDecoder(map[string]string{}, 0), func(string, []byte) {}, nil, nil, nil, nil, nil)
	require.ErrorIs(t, err, errNoAdmit)

	_, err = NewMqttEventSource("m", map[string]string{"host": "h", "port": "1883", "topic": "t"},
		nil, "", "", NewJsonDecoder(map[string]string{}, 0),
		func(string, []byte) {},
		func(string, string, *model.UnresolvedEvent, interface{}, uint64) error { return nil },
		func(string, string, []byte, error) error { return nil },
		nil, nil, func(error) {})
	require.ErrorIs(t, err, errNoAdmit)
}

// An external broker's message is dropped while the pipeline refuses: paho has already
// acknowledged it, so there is no lever to make the device retry. It is not metered and not
// queued for decode; admit counts the drop.
func TestExternalMqttDropsWhileThePipelineRefuses(t *testing.T) {
	metered := false
	allow := func(string, string, time.Time, bool, Origin) bool { metered = true; return true }
	es, received := newTestMqttSource(t, allow)
	asked := 0
	es.admit = func(string) error { asked++; return refuseAll("") }

	es.onMessage(nil, &fakeMqttMessage{topic: "inst-1/acme/events", payload: []byte(`{"device":"d1"}`)})

	assert.Equal(t, 1, asked, "the source must ask the pipeline before queueing")
	assert.Equal(t, 0, *received, "a refused message must not be counted as received")
	assert.False(t, metered, "a refused message must not spend a rate-limit token")
	assert.Len(t, es.messages, 0, "a refused message must not be queued for decode")
}
