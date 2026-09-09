// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestHttpSource builds an HTTP event source over a real JSON decoder with
// capturing callbacks, returning the source plus pointers to what the callbacks
// observed. allow is the rate-limit gate; a nil allow leaves ingest unmetered.
func newTestHttpSource(t *testing.T, allow RateGate) (*HttpEventSource, *capturedDecode, *capturedFailure) {
	t.Helper()
	return newTestHttpSourceWithIngest(t, allow, config.HttpIngest{}, nil)
}

// newTestHttpSourceWithIngest is newTestHttpSource with the device-facing listener
// bounds and the early-close accounting spelled out, for the tests that drive them.
func newTestHttpSourceWithIngest(t *testing.T, allow RateGate, ingest config.HttpIngest,
	earlyClose func(string)) (*HttpEventSource, *capturedDecode, *capturedFailure) {
	t.Helper()
	dec := &capturedDecode{}
	fail := &capturedFailure{}
	es, err := NewHttpEventSource("http-test", map[string]string{}, "inst-1", ingest,
		NewJsonDecoder(map[string]string{}, 0),
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
		allow, earlyClose)
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

// canonicalMeasurementBody is the wire shape the published contract specifies:
// entries under "payload", values as strings.
//
// Every body in this file was FLAT until the decoder was made to fail closed —
// measurements directly under "payload" with no entries wrapper — which decoded to
// zero entries and persisted nothing. That made the test named DecodeSuccess assert
// 202 for a body that stored none of its data, the exact silent success this
// transport's other tests exist to prevent. Hoisting it to one constant is
// deliberate: three copies is how the wrong shape survived three chances to be
// noticed.
const canonicalMeasurementBody = `{"device":"sensor-001","eventType":"Measurement",` +
	`"payload":{"entries":[{"measurements":{"temp":"21.5"}}]}}`

// A well-formed measurement event posted to /{instanceId}/{tenant}/events is
// decoded, the tenant is taken from the path, and the response is 202 Accepted.
func TestHttpEventSource_DecodeSuccess(t *testing.T) {
	es, dec, fail := newTestHttpSource(t, nil)

	body := canonicalMeasurementBody
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

	body := canonicalMeasurementBody
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
	es, dec, fail := newTestHttpSource(t, func(string, string, time.Time, bool) bool { return false })

	body := canonicalMeasurementBody
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
	newSource := func(cfg map[string]string) (*HttpEventSource, error) {
		return NewHttpEventSource("http-test", cfg, "inst-1", config.HttpIngest{},
			NewJsonDecoder(map[string]string{}, 0),
			func(string, []byte) {}, nil, nil, nil, nil)
	}

	es, err := newSource(map[string]string{"port": "9000"})
	assert.NoError(t, err)
	assert.Equal(t, 9000, es.Port)

	_, err = newSource(map[string]string{"port": "abc"})
	assert.Error(t, err)

	// A number outside the TCP port range is a configuration error, refused at
	// construction rather than carried as far as the bind.
	_, err = newSource(map[string]string{"port": "70000"})
	assert.Error(t, err)

	// 0 is IN range and is refused anyway: net.Listen reads it as "any free port", so
	// the source would bind an ephemeral port, start successfully, log a healthy
	// address and ingest nothing a device could be told to reach.
	_, err = newSource(map[string]string{"port": "0"})
	if assert.Error(t, err, "a device-facing listener must never be configured on port 0") {
		assert.Contains(t, err.Error(), "any free port")
	}

	// Absent port falls back to the default — NOT to 0, which is the value the
	// refusal above exists for.
	es, err = newSource(map[string]string{})
	assert.NoError(t, err)
	assert.Equal(t, DEFAULT_HTTP_PORT, es.Port)

	// An empty value is the same case as an absent key, and neither is 0.
	es, err = newSource(map[string]string{"port": ""})
	assert.NoError(t, err)
	assert.Equal(t, DEFAULT_HTTP_PORT, es.Port)
}

// postCanonicalEvent posts the canonical measurement body to a listening source over a
// REAL TCP connection, which is what makes the two tests below able to tell "serving"
// from "reported that it is serving". It returns the status code, or the transport error
// when nothing answered.
//
// The address comes from the bound listener rather than from the configured port,
// because these tests bind port 0 and let the operating system choose.
func postCanonicalEvent(addr string) (int, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("splitting listener address %q: %w", addr, err)
	}
	url := fmt.Sprintf("http://127.0.0.1:%s/inst-1/acme/events", port)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(url, "application/json", strings.NewReader(canonicalMeasurementBody))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// A port that cannot be bound FAILS THE START, and the failure names the address.
//
// The bind used to happen inside the serve goroutine, where its error could only be
// logged: the start returned nil regardless, the lifecycle recorded a started source,
// and HTTP ingest was simply absent with nothing in the component's state to say so.
//
// 🔴 Every assertion below runs on the test goroutine. An assertion made inside the
// serve goroutine would be a silent pass, because FailNow's runtime.Goexit unwinds only
// the goroutine it runs on — which is the same property that made the original defect
// invisible.
func TestHttpEventSource_StartFailsWhenPortIsAlreadyBound(t *testing.T) {
	es, _, _ := newTestHttpSource(t, nil)

	// Hold the port the source is about to ask for. Both listeners bind the wildcard
	// address, so this is the same addr:port pair and the second bind is refused.
	blocker, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	defer blocker.Close()
	es.Port = blocker.Addr().(*net.TCPAddr).Port

	ctx := context.Background()
	require.NoError(t, es.Initialize(ctx))

	err = es.Start(ctx)
	if assert.Error(t, err, "a start whose port cannot be bound must fail, not report success") {
		assert.Contains(t, err.Error(), fmt.Sprintf(":%d", es.Port),
			"the failure must name the address that could not be bound")
	}
	assert.Nil(t, es.server, "a failed start must retain no server")
	assert.Equal(t, core.Initialized, es.lifecycle.State,
		"a failed start must not leave the source reading as Started")
}

// initialize -> start -> stop -> start ends with a source that actually serves.
//
// A start after a stop is a permitted lifecycle sequence. The server used to be built
// once, in ExecuteInitialize, and net/http latches its shutting-down flag permanently —
// so the second Serve returned ErrServerClosed immediately, inside a goroutine that
// filtered that error out as a clean stop. The restart reported success and served
// nothing, which is why this test posts a real event rather than inspecting state.
func TestHttpEventSource_RestartsAndServesAgain(t *testing.T) {
	es, dec, _ := newTestHttpSource(t, nil)
	es.Port = 0 // let the OS choose, so the test needs no fixed port

	ctx := context.Background()
	require.NoError(t, es.Initialize(ctx))

	require.NoError(t, es.Start(ctx))
	require.NotNil(t, es.server)
	code, err := postCanonicalEvent(es.server.Addr())
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, code)
	require.True(t, dec.called)

	require.NoError(t, es.Stop(ctx))
	dec.called = false

	require.NoError(t, es.Start(ctx), "a start after a stop is a supported sequence")
	require.NotNil(t, es.server, "the restart must build a server")
	addr := es.server.Addr()
	assert.NotEmpty(t, addr, "the restarted source must be bound to something")

	code, err = postCanonicalEvent(addr)
	assert.NoError(t, err, "the restarted source must answer on the address it reports")
	assert.Equal(t, http.StatusAccepted, code, "the restarted source must still accept events")
	assert.True(t, dec.called, "the restarted source must hand the event to the pipeline")

	require.NoError(t, es.Stop(ctx))
}

// A stop that arrives on a source which was initialized but never started is a no-op.
// Initialized is on the permitted set for stop so that a component whose start failed
// halfway is still torn down, and the server is built only by a successful start — so
// this path meets a nil server and must not panic.
func TestHttpEventSource_StopWithoutStart(t *testing.T) {
	es, _, _ := newTestHttpSource(t, nil)

	ctx := context.Background()
	require.NoError(t, es.Initialize(ctx))
	assert.NoError(t, es.Stop(ctx))
}
