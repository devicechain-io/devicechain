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

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/assert"
)

// newTestHttpSource builds an HTTP event source over a real JSON decoder with
// capturing callbacks, returning the source plus pointers to what the callbacks
// observed. allow is the rate-limit gate; a nil allow leaves ingest unmetered.
func newTestHttpSource(t *testing.T, allow RateGate) (*HttpEventSource, *capturedDecode, *capturedFailure) {
	t.Helper()
	dec := &capturedDecode{}
	fail := &capturedFailure{}
	es, err := NewHttpEventSource("http-test", map[string]string{}, "inst-1", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {},
		func(source string, tenant string, event *model.UnresolvedEvent, payload interface{}, captureSeq uint64) error {
			dec.called = true
			dec.tenant = tenant
			dec.event = event
			dec.captureSeq = captureSeq
			return dec.publishErr
		},
		func(source string, tenant string, raw []byte, err error) error {
			fail.called = true
			fail.tenant = tenant
			fail.err = err
			return nil
		},
		allow)
	assert.NoError(t, err)
	return es, dec, fail
}

type capturedDecode struct {
	called     bool
	tenant     string
	event      *model.UnresolvedEvent
	captureSeq uint64
	// publishErr is returned to the source as the publish outcome, so a test can
	// exercise what the handler tells the CLIENT when the event never reached the
	// stream.
	publishErr error
}

type capturedFailure struct {
	called bool
	tenant string
	err    error
}

// A well-formed measurement event posted to /{instanceId}/{tenant}/events is
// decoded, the tenant is taken from the path, and the response is 202 Accepted.
func TestHttpEventSource_DecodeSuccess(t *testing.T) {
	es, dec, fail := newTestHttpSource(t, nil)

	body := `{"device":"sensor-001","eventType":"Measurement","payload":{"measurements":{"temp":21.5}}}`
	req := httptest.NewRequest(http.MethodPost, "/inst-1/acme/events", strings.NewReader(body))
	rec := httptest.NewRecorder()
	es.handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.True(t, dec.called, "decoded callback should fire")
	assert.False(t, fail.called, "failed callback should not fire")
	assert.Equal(t, "acme", dec.tenant)
	assert.Equal(t, "sensor-001", dec.event.Device)
	assert.Equal(t, model.Measurement, dec.event.EventType)
}

// A well-formed event whose PUBLISH fails must not be answered 202.
//
// 202 means accepted, and returning it for an event that never reached the
// stream told the caller its data was safe when it had been dropped — the same
// silent loss ADR-030 removes from the MQTT path, on the one transport that can
// actually say otherwise. A client that retries on 5xx now loses nothing.
//
// HTTP carries no capture sequence, so it also publishes no dedup id: there is no
// broker redelivery on this path to deduplicate.
func TestHttpEventSource_PublishFailureIsNotReportedAsAccepted(t *testing.T) {
	es, dec, fail := newTestHttpSource(t, nil)
	dec.publishErr = errors.New("jetstream unavailable")

	body := `{"device":"sensor-001","eventType":"Measurement","payload":{"measurements":{"temp":21.5}}}`
	req := httptest.NewRequest(http.MethodPost, "/inst-1/acme/events", strings.NewReader(body))
	rec := httptest.NewRecorder()
	es.handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"an event that never reached the stream must not be reported as accepted")
	assert.True(t, dec.called, "the publish should have been attempted")
	assert.False(t, fail.called, "a publish failure is not a decode failure")
	assert.Zero(t, dec.captureSeq, "HTTP has no capture sequence, so it must publish no dedup id")
}

// A body that cannot be decoded routes to the failed callback (with the path
// tenant) and returns 400.
func TestHttpEventSource_DecodeFailure(t *testing.T) {
	es, dec, fail := newTestHttpSource(t, nil)

	req := httptest.NewRequest(http.MethodPost, "/inst-1/acme/events", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	es.handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.True(t, fail.called, "failed callback should fire")
	assert.False(t, dec.called, "decoded callback should not fire")
	assert.Equal(t, "acme", fail.tenant)
}

// A request with no tenant segment does not match the route (405/404), and
// neither callback fires.
func TestHttpEventSource_MissingTenant(t *testing.T) {
	es, dec, fail := newTestHttpSource(t, nil)

	req := httptest.NewRequest(http.MethodPost, "/inst-1//events", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	es.handler().ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusAccepted, rec.Code)
	assert.False(t, dec.called)
	assert.False(t, fail.called)
}

// A request whose tenant is over its ingest rate limit is shed with 429 before
// the body is decoded — neither callback fires.
func TestHttpEventSource_RateLimited(t *testing.T) {
	es, dec, fail := newTestHttpSource(t, func(string, string, time.Time) bool { return false })

	body := `{"device":"sensor-001","eventType":"Measurement","payload":{"measurements":{"temp":21.5}}}`
	req := httptest.NewRequest(http.MethodPost, "/inst-1/acme/events", strings.NewReader(body))
	rec := httptest.NewRecorder()
	es.handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.False(t, dec.called, "decoded callback should not fire when shed")
	assert.False(t, fail.called, "failed callback should not fire when shed")
}

// A wrong method on the route is rejected (the route is POST-only).
func TestHttpEventSource_WrongMethod(t *testing.T) {
	es, _, _ := newTestHttpSource(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/inst-1/acme/events", nil)
	rec := httptest.NewRecorder()
	es.handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

// The configured port is parsed; an invalid port fails construction.
func TestNewHttpEventSource_Port(t *testing.T) {
	es, err := NewHttpEventSource("http-test", map[string]string{"port": "9000"}, "inst-1", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {}, nil, nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, 9000, es.Port)

	_, err = NewHttpEventSource("http-test", map[string]string{"port": "abc"}, "inst-1", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {}, nil, nil, nil)
	assert.Error(t, err)

	// Absent port falls back to the default.
	es, err = NewHttpEventSource("http-test", map[string]string{}, "inst-1", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {}, nil, nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, DEFAULT_HTTP_PORT, es.Port)
}
